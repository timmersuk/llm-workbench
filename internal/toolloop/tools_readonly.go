package toolloop

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"sort"
	"strconv"
	"strings"

	"github.com/timmersuk/llm-workbench/internal/chat"
)

// lookPath is exec.LookPath, indirected so grepTool's missing-rg error path
// is deterministically testable without mutating the test process's PATH —
// the same seam claude_runner.go's lookPath provides for CheckHealth.
var lookPath = exec.LookPath

// ReadOnlyTools returns the Read/Grep/Glob toolset offered to the read-only
// Run instantiation — the same trust boundary agentrunner.readOnlyTools
// establishes for ClaudeRunner (no Write/Edit/Bash), enforced here by which
// Tools the engine is given rather than by a second implementation. Also
// includes the ref-aware read tools (docs/adr/0013): read_file/grep_search/
// glob only ever see the current working tree, so a Requirements-stage
// conversation reopened after a rejected review (milestone6.md's PR 5/PR 6)
// has no way to inspect that rejected attempt's actual code without them.
func ReadOnlyTools() []Tool {
	return []Tool{readTool{}, grepTool{}, globTool{}, readAtRefTool{}, listFilesAtRefTool{}}
}

// --- read_file ---

type readTool struct{}

func (readTool) Spec() chat.Tool {
	return chat.Tool{
		Type: "function",
		Function: chat.ToolSchema{
			Name:        "read_file",
			Description: "Read a UTF-8 text file from the workspace. Returns up to " + itoa(readDefaultLines) + " lines from `offset` (1-based, default 1); page further with `offset`/`limit`.",
			Parameters: json.RawMessage(`{"type":"object","required":["path"],"properties":{` +
				`"path":{"type":"string","description":"Path relative to the workspace root"},` +
				`"offset":{"type":"integer","description":"1-based first line to return (default 1)"},` +
				`"limit":{"type":"integer","description":"Max lines to return (default ` + itoa(readDefaultLines) + `)"}}}`),
		},
	}
}

func (readTool) Execute(_ context.Context, workspace, argumentsJSON string) (string, error) {
	var args struct {
		Path   string `json:"path"`
		Offset int    `json:"offset"`
		Limit  int    `json:"limit"`
	}
	if err := json.Unmarshal([]byte(argumentsJSON), &args); err != nil {
		return "", fmt.Errorf("invalid arguments: %w", err)
	}
	if args.Path == "" {
		return "", fmt.Errorf("path is required")
	}
	abs, err := resolveInWorkspace(workspace, args.Path)
	if err != nil {
		return "", err
	}
	b, err := os.ReadFile(abs)
	if err != nil {
		return "", err
	}

	offset := args.Offset
	if offset <= 0 {
		offset = 1
	}
	limit := args.Limit
	if limit <= 0 {
		limit = readDefaultLines
	}

	lines := strings.Split(string(b), "\n")
	if offset > len(lines) {
		return fmt.Sprintf("[file has %d lines; offset %d is past the end]", len(lines), offset), nil
	}
	end := offset - 1 + limit
	if end > len(lines) {
		end = len(lines)
	}
	page := lines[offset-1 : end]
	out := strings.Join(page, "\n")
	if end < len(lines) {
		out += fmt.Sprintf("\n[showing lines %d-%d of %d; page further with offset=%d]", offset, end, len(lines), end+1)
	}
	return truncateResult(out), nil
}

// --- grep_search ---

type grepTool struct{}

func (grepTool) Spec() chat.Tool {
	return chat.Tool{
		Type: "function",
		Function: chat.ToolSchema{
			Name:        "grep_search",
			Description: "Search workspace files for a regular expression using ripgrep. Gitignore-aware and binary files are skipped automatically. Returns matches (and optional context lines) as path:line:text; output beyond ~16KB is truncated.",
			Parameters: json.RawMessage(`{"type":"object","required":["pattern"],"properties":{` +
				`"pattern":{"type":"string","description":"Regular expression to search for"},` +
				`"path":{"type":"string","description":"Optional subdirectory (relative to the workspace) to restrict the search"},` +
				`"context":{"type":"integer","description":"Number of context lines to show before and after each match (default 0)"},` +
				`"ignore_case":{"type":"boolean","description":"Match case-insensitively (default false)"},` +
				`"glob":{"type":"string","description":"Restrict the search to files matching this glob, e.g. `+"`*.go`"+` or `+"`*.{go,ts}`"+`"}}}`),
		},
	}
}

func (grepTool) Execute(ctx context.Context, workspace, argumentsJSON string) (string, error) {
	var args struct {
		Pattern    string `json:"pattern"`
		Path       string `json:"path"`
		Context    int    `json:"context"`
		IgnoreCase bool   `json:"ignore_case"`
		Glob       string `json:"glob"`
	}
	if err := json.Unmarshal([]byte(argumentsJSON), &args); err != nil {
		return "", fmt.Errorf("invalid arguments: %w", err)
	}
	if args.Pattern == "" {
		return "", fmt.Errorf("pattern is required")
	}

	rg, err := rgPath()
	if err != nil {
		return "", err
	}

	root, err := filepath.Abs(workspace)
	if err != nil {
		return "", err
	}
	searchArg := "."
	if args.Path != "" {
		abs, err := resolveInWorkspace(workspace, args.Path)
		if err != nil {
			return "", err
		}
		rel, err := filepath.Rel(root, abs)
		if err != nil {
			return "", err
		}
		searchArg = rel
	}

	if args.Context < 0 {
		args.Context = 0
	}

	cmdArgs := []string{"--no-heading", "-n", "-C", strconv.Itoa(args.Context)}
	if args.IgnoreCase {
		cmdArgs = append(cmdArgs, "--ignore-case")
	}
	if args.Glob != "" {
		cmdArgs = append(cmdArgs, "-g", args.Glob)
	}
	cmdArgs = append(cmdArgs, "--color=never", "-e", args.Pattern, "--", searchArg)

	cmd := exec.CommandContext(ctx, rg, cmdArgs...)
	cmd.Dir = root
	var stdout, stderr strings.Builder
	cmd.Stdout = &stdout
	cmd.Stderr = &stderr
	runErr := cmd.Run()

	var exitErr *exec.ExitError
	if errors.As(runErr, &exitErr) {
		if exitErr.ExitCode() == 1 {
			return "no matches", nil
		}
		msg := strings.TrimSpace(stderr.String())
		if msg == "" {
			msg = fmt.Sprintf("rg exited with status %d", exitErr.ExitCode())
		}
		return "", fmt.Errorf("rg: %s", msg)
	}
	if runErr != nil {
		return "", fmt.Errorf("running rg: %w", runErr)
	}

	out := filepath.ToSlash(stdout.String())
	if strings.TrimSpace(out) == "" {
		return "no matches", nil
	}
	return truncateResult(out), nil
}

// rgPath resolves the ripgrep executable via PATH lookup only — no
// hardcoded fallback install locations. grep_search hard-requires a system
// rg binary; a missing one is a clear, actionable error rather than a
// silent degrade to a weaker built-in scanner.
func rgPath() (string, error) {
	if p, err := lookPath("rg"); err == nil {
		return p, nil
	}
	if runtime.GOOS == "windows" {
		return "", fmt.Errorf("rg (ripgrep) not found on PATH: install it with `winget install BurntSushi.ripgrep.MSVC` (or `choco install ripgrep`), then restart the server")
	}
	return "", fmt.Errorf("rg (ripgrep) not found on PATH: install it with your package manager (e.g. `apt install ripgrep`, `brew install ripgrep`, `dnf install ripgrep`), then restart the server")
}

// --- glob ---

type globTool struct{}

func (globTool) Spec() chat.Tool {
	return chat.Tool{
		Type: "function",
		Function: chat.ToolSchema{
			Name:        "glob",
			Description: "List workspace files matching a glob pattern (e.g. `**/*.go`, `internal/*/*.md`). Returns up to " + itoa(globMaxMatches) + " workspace-relative paths.",
			Parameters: json.RawMessage(`{"type":"object","required":["pattern"],"properties":{` +
				`"pattern":{"type":"string","description":"Glob pattern relative to the workspace root; ** matches any depth"}}}`),
		},
	}
}

func (globTool) Execute(_ context.Context, workspace, argumentsJSON string) (string, error) {
	var args struct {
		Pattern string `json:"pattern"`
	}
	if err := json.Unmarshal([]byte(argumentsJSON), &args); err != nil {
		return "", fmt.Errorf("invalid arguments: %w", err)
	}
	if args.Pattern == "" {
		return "", fmt.Errorf("pattern is required")
	}

	var results []string
	err := filepath.WalkDir(workspace, func(p string, d os.DirEntry, err error) error {
		if err != nil {
			return nil
		}
		if d.IsDir() {
			if skipDir(d.Name()) {
				return filepath.SkipDir
			}
			return nil
		}
		rel, relErr := filepath.Rel(workspace, p)
		if relErr != nil {
			return nil
		}
		rel = filepath.ToSlash(rel)
		if ok, _ := matchGlob(args.Pattern, rel); ok {
			results = append(results, rel)
		}
		return nil
	})
	if err != nil {
		return "", err
	}
	if len(results) == 0 {
		return "no matches", nil
	}
	sort.Strings(results)
	truncated := false
	if len(results) > globMaxMatches {
		results = results[:globMaxMatches]
		truncated = true
	}
	out := strings.Join(results, "\n")
	if truncated {
		out += "\n[truncated: hit the match cap — narrow the pattern]"
	}
	return truncateResult(out), nil
}

// matchGlob matches a slash-separated path against a glob pattern, supporting
// a `**` segment that spans any number of path segments (which
// filepath.Match does not). Without `**` it defers to path.Match semantics
// per segment.
func matchGlob(pattern, name string) (bool, error) {
	if !strings.Contains(pattern, "**") {
		return filepath.Match(filepath.FromSlash(pattern), filepath.FromSlash(name))
	}
	pat := strings.Split(pattern, "/")
	seg := strings.Split(name, "/")
	return matchSegments(pat, seg), nil
}

func matchSegments(pat, seg []string) bool {
	if len(pat) == 0 {
		return len(seg) == 0
	}
	if pat[0] == "**" {
		// `**` matches zero or more segments.
		for i := 0; i <= len(seg); i++ {
			if matchSegments(pat[1:], seg[i:]) {
				return true
			}
		}
		return false
	}
	if len(seg) == 0 {
		return false
	}
	if ok, _ := filepath.Match(pat[0], seg[0]); !ok {
		return false
	}
	return matchSegments(pat[1:], seg[1:])
}

func skipDir(name string) bool {
	switch name {
	case ".git", "node_modules", ".claude":
		return true
	}
	return false
}

func itoa(n int) string { return fmt.Sprintf("%d", n) }

package toolloop

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"

	"github.com/timmersuk/llm-workbench/internal/chat"
)

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
			Description: "Search workspace text files for a case-sensitive substring. Returns up to " + itoa(grepMaxMatches) + " matches as path:line:text.",
			Parameters: json.RawMessage(`{"type":"object","required":["pattern"],"properties":{` +
				`"pattern":{"type":"string","description":"Case-sensitive substring to search for"},` +
				`"path":{"type":"string","description":"Optional subdirectory (relative to the workspace) to restrict the search"}}}`),
		},
	}
}

func (grepTool) Execute(_ context.Context, workspace, argumentsJSON string) (string, error) {
	var args struct {
		Pattern string `json:"pattern"`
		Path    string `json:"path"`
	}
	if err := json.Unmarshal([]byte(argumentsJSON), &args); err != nil {
		return "", fmt.Errorf("invalid arguments: %w", err)
	}
	if args.Pattern == "" {
		return "", fmt.Errorf("pattern is required")
	}
	root := workspace
	if args.Path != "" {
		abs, err := resolveInWorkspace(workspace, args.Path)
		if err != nil {
			return "", err
		}
		root = abs
	}

	var sb strings.Builder
	matches := 0
	err := filepath.WalkDir(root, func(p string, d os.DirEntry, err error) error {
		if err != nil {
			return nil // skip unreadable entries
		}
		if matches >= grepMaxMatches {
			return filepath.SkipAll
		}
		if d.IsDir() {
			if skipDir(d.Name()) {
				return filepath.SkipDir
			}
			return nil
		}
		if !isTextFile(p) {
			return nil
		}
		b, err := os.ReadFile(p)
		if err != nil {
			return nil
		}
		rel, _ := filepath.Rel(workspace, p)
		for i, line := range strings.Split(string(b), "\n") {
			if strings.Contains(line, args.Pattern) {
				fmt.Fprintf(&sb, "%s:%d:%s\n", filepath.ToSlash(rel), i+1, strings.TrimSpace(line))
				matches++
				if matches >= grepMaxMatches {
					sb.WriteString("[truncated: hit the match cap — narrow the pattern or scope with `path`]\n")
					break
				}
			}
		}
		return nil
	})
	if err != nil {
		return "", err
	}
	if sb.Len() == 0 {
		return "no matches", nil
	}
	return truncateResult(sb.String()), nil
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

// isTextFile is a conservative allow-list of extensions grep will read, so a
// search doesn't stream binary noise back into the model's context.
func isTextFile(p string) bool {
	switch strings.ToLower(filepath.Ext(p)) {
	case ".go", ".md", ".yaml", ".yml", ".json", ".txt", ".toml", ".ts", ".tsx", ".js", ".jsx", ".css", ".html", ".sh", ".py", ".rs", ".java", ".sql", ".env", ".mod", ".sum", ".gitignore", ".dockerfile":
		return true
	}
	base := strings.ToLower(filepath.Base(p))
	switch base {
	case "dockerfile", "makefile", "go.mod", "go.sum", ".gitignore":
		return true
	}
	return false
}

func itoa(n int) string { return fmt.Sprintf("%d", n) }

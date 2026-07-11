package main

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"
)

// The probe's tool surface mirrors the read-only Run instantiation the
// engine will offer (Read/Grep), implemented directly here so modelprobe
// stays self-contained. Output caps match the small-context stance from the
// Milestone 8 grilling: bounded results with an explicit truncation marker
// the model can act on.
const maxToolOutput = 8 * 1024

func truncate(s string) string {
	if len(s) > maxToolOutput {
		return s[:maxToolOutput] + "\n[TRUNCATED: output exceeded limit, narrow your query]"
	}
	return s
}

// resolveInWorkspace enforces the same path-confinement guarantee the engine
// requires: a tool call must not read outside the target directory.
func resolveInWorkspace(root, rel string) (string, error) {
	abs, err := filepath.Abs(filepath.Join(root, rel))
	if err != nil {
		return "", err
	}
	rootAbs, err := filepath.Abs(root)
	if err != nil {
		return "", err
	}
	if abs != rootAbs && !strings.HasPrefix(abs, rootAbs+string(filepath.Separator)) {
		return "", fmt.Errorf("path %q escapes workspace", rel)
	}
	return abs, nil
}

func toolDefs() []tool {
	return []tool{
		{
			Type: "function",
			Function: toolFunction{
				Name:        "read_file",
				Description: "Read a file from the repository. Returns its contents as text.",
				Parameters: json.RawMessage(`{"type":"object","required":["path"],"properties":{` +
					`"path":{"type":"string","description":"Relative path of the file to read"}}}`),
			},
		},
		{
			Type: "function",
			Function: toolFunction{
				Name:        "grep_search",
				Description: "Search all .go and .md files in the repository for a substring. Returns matching lines as path:line:text.",
				Parameters: json.RawMessage(`{"type":"object","required":["pattern"],"properties":{` +
					`"pattern":{"type":"string","description":"Substring to search for (case-sensitive)"}}}`),
			},
		},
	}
}

// dispatchTool runs one tool call against root. Errors are returned as result
// text (not Go errors), so a model can self-correct rather than the run
// aborting — one of the framework-behavior differences the spike surfaced.
func dispatchTool(root string, tc toolCall) string {
	switch tc.Function.Name {
	case "read_file":
		var args struct {
			Path string `json:"path"`
		}
		if err := json.Unmarshal([]byte(tc.Function.Arguments), &args); err != nil {
			return "ERROR: invalid arguments: " + err.Error()
		}
		p, err := resolveInWorkspace(root, args.Path)
		if err != nil {
			return "ERROR: " + err.Error()
		}
		b, err := os.ReadFile(p)
		if err != nil {
			return "ERROR: " + err.Error()
		}
		return truncate(string(b))

	case "grep_search":
		var args struct {
			Pattern string `json:"pattern"`
		}
		if err := json.Unmarshal([]byte(tc.Function.Arguments), &args); err != nil {
			return "ERROR: invalid arguments: " + err.Error()
		}
		return grepSearch(root, args.Pattern)

	default:
		return "ERROR: unknown tool " + tc.Function.Name
	}
}

func grepSearch(root, pattern string) string {
	var sb strings.Builder
	matches := 0
	_ = filepath.WalkDir(root, func(path string, d os.DirEntry, err error) error {
		if err != nil {
			return nil
		}
		if matches >= 100 {
			return filepath.SkipAll
		}
		if d.IsDir() {
			if d.Name() == ".git" || d.Name() == "node_modules" {
				return filepath.SkipDir
			}
			return nil
		}
		ext := filepath.Ext(path)
		if ext != ".go" && ext != ".md" {
			return nil
		}
		b, err := os.ReadFile(path)
		if err != nil {
			return nil
		}
		rel, _ := filepath.Rel(root, path)
		for i, line := range strings.Split(string(b), "\n") {
			if strings.Contains(line, pattern) {
				fmt.Fprintf(&sb, "%s:%d:%s\n", rel, i+1, strings.TrimSpace(line))
				matches++
				if matches >= 100 {
					sb.WriteString("[TRUNCATED: 100-match cap reached]\n")
					break
				}
			}
		}
		return nil
	})
	if sb.Len() == 0 {
		return "no matches"
	}
	return truncate(sb.String())
}

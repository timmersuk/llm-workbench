package toolloop

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"github.com/timmersuk/llm-workbench/internal/chat"
)

// ExecutionTools returns the write-enabled toolset offered to the Execute
// instantiation: everything ReadOnlyTools offers, plus write_file, edit_file,
// and bash — the full implementation-stage toolset (milestone8.md's "Bash:
// scope and posture").
func ExecutionTools() []Tool {
	return append(ReadOnlyTools(), writeTool{}, editTool{}, bashTool{})
}

// ReviewTools returns the toolset offered to the Review conversation
// (Milestone 6's automated-checks phase): the read-only Read/Grep/Glob set
// plus the confined bash tool, so the reviewing agent can run the project's
// test command and probe the executed change, but not write or edit it —
// review inspects a completed execution, it doesn't author one.
func ReviewTools() []Tool {
	return append(ReadOnlyTools(), bashTool{})
}

// --- write_file ---

type writeTool struct{}

func (writeTool) Spec() chat.Tool {
	return chat.Tool{
		Type: "function",
		Function: chat.ToolSchema{
			Name:        "write_file",
			Description: "Write content to a file in the workspace, creating it (and any missing parent directories) if it doesn't exist, or overwriting it if it does.",
			Parameters: json.RawMessage(`{"type":"object","required":["path","content"],"properties":{` +
				`"path":{"type":"string","description":"Path relative to the workspace root"},` +
				`"content":{"type":"string","description":"Full file content to write"}}}`),
		},
	}
}

func (writeTool) Execute(_ context.Context, workspace, argumentsJSON string) (string, error) {
	var args struct {
		Path    string `json:"path"`
		Content string `json:"content"`
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
	if err := os.MkdirAll(filepath.Dir(abs), 0o755); err != nil {
		return "", fmt.Errorf("creating parent directories: %w", err)
	}
	if err := os.WriteFile(abs, []byte(args.Content), 0o644); err != nil {
		return "", err
	}
	return fmt.Sprintf("wrote %d bytes to %s", len(args.Content), args.Path), nil
}

// --- edit_file ---

type editTool struct{}

func (editTool) Spec() chat.Tool {
	return chat.Tool{
		Type: "function",
		Function: chat.ToolSchema{
			Name:        "edit_file",
			Description: "Replace exact text in an existing workspace file. old_string must match the file's current content exactly and, unless replace_all is set, must be unique in the file — include enough surrounding context to make it so.",
			Parameters: json.RawMessage(`{"type":"object","required":["path","old_string","new_string"],"properties":{` +
				`"path":{"type":"string","description":"Path relative to the workspace root"},` +
				`"old_string":{"type":"string","description":"Exact existing text to replace"},` +
				`"new_string":{"type":"string","description":"Replacement text"},` +
				`"replace_all":{"type":"boolean","description":"Replace every occurrence instead of requiring old_string to be unique (default false)"}}}`),
		},
	}
}

func (editTool) Execute(_ context.Context, workspace, argumentsJSON string) (string, error) {
	var args struct {
		Path       string `json:"path"`
		OldString  string `json:"old_string"`
		NewString  string `json:"new_string"`
		ReplaceAll bool   `json:"replace_all"`
	}
	if err := json.Unmarshal([]byte(argumentsJSON), &args); err != nil {
		return "", fmt.Errorf("invalid arguments: %w", err)
	}
	if args.Path == "" {
		return "", fmt.Errorf("path is required")
	}
	if args.OldString == "" {
		return "", fmt.Errorf("old_string is required")
	}
	abs, err := resolveInWorkspace(workspace, args.Path)
	if err != nil {
		return "", err
	}
	b, err := os.ReadFile(abs)
	if err != nil {
		return "", err
	}
	content := string(b)

	count := strings.Count(content, args.OldString)
	if count == 0 {
		return "", fmt.Errorf("old_string not found in %s", args.Path)
	}
	if count > 1 && !args.ReplaceAll {
		return "", fmt.Errorf("old_string matches %d times in %s; include more surrounding context to make it unique, or set replace_all", count, args.Path)
	}

	replaced := 1
	var updated string
	if args.ReplaceAll {
		updated = strings.ReplaceAll(content, args.OldString, args.NewString)
		replaced = count
	} else {
		updated = strings.Replace(content, args.OldString, args.NewString, 1)
	}

	if err := os.WriteFile(abs, []byte(updated), 0o644); err != nil {
		return "", err
	}
	return fmt.Sprintf("replaced %d occurrence(s) in %s", replaced, args.Path), nil
}

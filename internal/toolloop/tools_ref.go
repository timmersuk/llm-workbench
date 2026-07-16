package toolloop

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"

	"github.com/timmersuk/llm-workbench/internal/chat"
	"github.com/timmersuk/llm-workbench/internal/gitutil"
)

// --- read_file_at_ref ---

type readAtRefTool struct{}

func (readAtRefTool) Spec() chat.Tool {
	return chat.Tool{
		Type: "function",
		Function: chat.ToolSchema{
			Name: "read_file_at_ref",
			Description: "Read a file's content as of a specific git ref (branch, tag, or commit) in the workspace's repository — e.g. to inspect a rejected execution's actual code, unlike read_file which only sees the current working tree. Returns up to " +
				itoa(readDefaultLines) + " lines from `offset` (1-based, default 1); page further with `offset`/`limit`.",
			Parameters: json.RawMessage(`{"type":"object","required":["ref","path"],"properties":{` +
				`"ref":{"type":"string","description":"Git ref to read from: a branch name, tag, or commit hash"},` +
				`"path":{"type":"string","description":"Path within that ref's tree, relative to the repository root"},` +
				`"offset":{"type":"integer","description":"1-based first line to return (default 1)"},` +
				`"limit":{"type":"integer","description":"Max lines to return (default ` + itoa(readDefaultLines) + `)"}}}`),
		},
	}
}

func (readAtRefTool) Execute(ctx context.Context, workspace, argumentsJSON string) (string, error) {
	var args struct {
		Ref    string `json:"ref"`
		Path   string `json:"path"`
		Offset int    `json:"offset"`
		Limit  int    `json:"limit"`
	}
	if err := json.Unmarshal([]byte(argumentsJSON), &args); err != nil {
		return "", fmt.Errorf("invalid arguments: %w", err)
	}
	if args.Ref == "" {
		return "", fmt.Errorf("ref is required")
	}
	if args.Path == "" {
		return "", fmt.Errorf("path is required")
	}

	content, err := gitutil.RunGit(ctx, workspace, "show", args.Ref+":"+args.Path)
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

	lines := strings.Split(content, "\n")
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

// --- list_files_at_ref ---

type listFilesAtRefTool struct{}

func (listFilesAtRefTool) Spec() chat.Tool {
	return chat.Tool{
		Type: "function",
		Function: chat.ToolSchema{
			Name: "list_files_at_ref",
			Description: "List every file that exists at a specific git ref (branch, tag, or commit) in the workspace's repository. Returns up to " +
				itoa(globMaxMatches) + " paths. Use this to find a file a branch added that the current working tree doesn't have, before reading it with read_file_at_ref.",
			Parameters: json.RawMessage(`{"type":"object","required":["ref"],"properties":{` +
				`"ref":{"type":"string","description":"Git ref to list: a branch name, tag, or commit hash"}}}`),
		},
	}
}

func (listFilesAtRefTool) Execute(ctx context.Context, workspace, argumentsJSON string) (string, error) {
	var args struct {
		Ref string `json:"ref"`
	}
	if err := json.Unmarshal([]byte(argumentsJSON), &args); err != nil {
		return "", fmt.Errorf("invalid arguments: %w", err)
	}
	if args.Ref == "" {
		return "", fmt.Errorf("ref is required")
	}

	out, err := gitutil.RunGit(ctx, workspace, "ls-tree", "-r", "--name-only", args.Ref)
	if err != nil {
		return "", err
	}

	out = strings.TrimRight(out, "\n")
	if out == "" {
		return "no files at this ref", nil
	}
	paths := strings.Split(out, "\n")

	truncated := false
	if len(paths) > globMaxMatches {
		paths = paths[:globMaxMatches]
		truncated = true
	}
	result := strings.Join(paths, "\n")
	if truncated {
		result += "\n[truncated: hit the match cap — narrow with a more specific ref, or read known paths directly]"
	}
	return truncateResult(result), nil
}

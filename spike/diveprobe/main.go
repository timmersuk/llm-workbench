// diveprobe is the Milestone 8 Phase 0 spike probe for dive: the same
// two-tool (read_file, grep_search) agent loop as einoprobe, backed by
// internal/chat.ChatClient through the wbLLM adapter. Throwaway code —
// lives on an unmerged spike branch.
package main

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/deepnoodle-ai/dive"

	"github.com/timmersuk/llm-workbench/internal/chat"
)

func getenv(key, fallback string) string {
	if v := os.Getenv(key); v != "" {
		return v
	}
	return fallback
}

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

type readFileArgs struct {
	Path string `json:"path" description:"Relative path of the file to read"`
}

type grepArgs struct {
	Pattern string `json:"pattern" description:"Substring to search for (case-sensitive)"`
}

const maxToolOutput = 8 * 1024

func truncate(s string) string {
	if len(s) > maxToolOutput {
		return s[:maxToolOutput] + "\n[TRUNCATED: output exceeded limit, narrow your query]"
	}
	return s
}

func makeTools(root string) []dive.Tool {
	readFile := dive.FuncTool("read_file",
		"Read a file from the repository. Returns its contents as text.",
		func(_ context.Context, in *readFileArgs) (*dive.ToolResult, error) {
			p, err := resolveInWorkspace(root, in.Path)
			if err != nil {
				return dive.NewToolResultError(err.Error()), nil
			}
			b, err := os.ReadFile(p)
			if err != nil {
				return dive.NewToolResultError(err.Error()), nil
			}
			return dive.NewToolResultText(truncate(string(b))), nil
		})

	grep := dive.FuncTool("grep_search",
		"Search all .go and .md files in the repository for a substring. Returns matching lines as path:line:text.",
		func(_ context.Context, in *grepArgs) (*dive.ToolResult, error) {
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
					if strings.Contains(line, in.Pattern) {
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
				return dive.NewToolResultText("no matches"), nil
			}
			return dive.NewToolResultText(truncate(sb.String())), nil
		})

	return []dive.Tool{readFile, grep}
}

func main() {
	ctx := context.Background()
	baseURL := getenv("LLM_BASE_URL", "http://192.168.1.240:1234/v1")
	modelName := getenv("LLM_MODEL", "qwen3.6-35b-a3b-mtp")
	root := getenv("SPIKE_TARGET_DIR", ".")

	client := &debugClient{ChatClient: chat.NewOpenAIClient(baseURL, "", 10*time.Minute)}

	agent, err := dive.NewAgent(dive.AgentOptions{
		Name:            "diveprobe",
		SystemPrompt:    "You are a code exploration assistant. Use the read_file and grep_search tools to ground every answer in the repository's actual contents. Keep answers short.",
		Model:           newWBLLM(client, modelName),
		Tools:           makeTools(root),
		ResponseTimeout: 5 * time.Minute,
	})
	if err != nil {
		fmt.Fprintln(os.Stderr, "agent setup:", err)
		os.Exit(1)
	}

	question := "Which Go interface in this repository defines the agent executor abstraction, and what methods does it have? Read whatever files you need."
	if len(os.Args) > 1 {
		question = strings.Join(os.Args[1:], " ")
	}

	fmt.Printf("=== diveprobe: %s @ %s ===\nQ: %s\n\n", modelName, baseURL, question)
	start := time.Now()

	var mode string
	callback := func(_ context.Context, item *dive.ResponseItem) error {
		switch {
		case item.Event != nil && item.Event.Delta != nil:
			d := item.Event.Delta
			switch {
			case d.Thinking != "":
				if mode != "reasoning" {
					fmt.Print("\n[reasoning] ")
					mode = "reasoning"
				}
				fmt.Print(d.Thinking)
			case d.Text != "":
				if mode != "content" {
					fmt.Print("\n[answer] ")
					mode = "content"
				}
				fmt.Print(d.Text)
			}
		case item.ToolCall != nil:
			mode = "tool"
			fmt.Printf("\n[tool call] %s(%s)\n", item.ToolCall.Name, string(item.ToolCall.Input))
		case item.ToolCallResult != nil:
			fmt.Printf("[tool result] %d bytes\n", len(fmt.Sprint(item.ToolCallResult.Result)))
		}
		return nil
	}

	resp, err := agent.CreateResponse(ctx,
		dive.WithInput(question),
		dive.WithEventCallback(callback),
	)
	if err != nil {
		fmt.Fprintln(os.Stderr, "\ncreate response:", err)
		os.Exit(1)
	}

	fmt.Printf("\n\n=== done in %s | usage: %+v ===\n", time.Since(start).Round(time.Millisecond), resp.Usage)
}

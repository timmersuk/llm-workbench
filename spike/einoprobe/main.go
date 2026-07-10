// einoprobe is the Milestone 8 Phase 0 spike probe for eino: a two-tool
// (read_file, grep_search) react-agent loop over the workbench repo, backed
// by internal/chat.ChatClient through the wbChatModel adapter, against the
// LM Studio endpoint. Throwaway code — lives on an unmerged spike branch.
package main

import (
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/cloudwego/eino/components/tool"
	"github.com/cloudwego/eino/components/tool/utils"
	"github.com/cloudwego/eino/compose"
	"github.com/cloudwego/eino/flow/agent/react"
	"github.com/cloudwego/eino/schema"

	"github.com/timmersuk/llm-workbench/internal/chat"
)

func getenv(key, fallback string) string {
	if v := os.Getenv(key); v != "" {
		return v
	}
	return fallback
}

// resolveInWorkspace mirrors the path-confinement rule the real engine will
// enforce: reject anything escaping the workspace root.
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
	Path string `json:"path" jsonschema:"description=Relative path of the file to read"`
}

type grepArgs struct {
	Pattern string `json:"pattern" jsonschema:"description=Substring to search for (case-sensitive)"`
}

const maxToolOutput = 8 * 1024 // deliberately small-context-friendly, per the milestone's result-shaping stance

func truncate(s string) string {
	if len(s) > maxToolOutput {
		return s[:maxToolOutput] + "\n[TRUNCATED: output exceeded limit, narrow your query]"
	}
	return s
}

func makeTools(root string) ([]tool.BaseTool, error) {
	readFile, err := utils.InferTool("read_file",
		"Read a file from the repository. Returns its contents as text.",
		func(_ context.Context, in *readFileArgs) (string, error) {
			p, err := resolveInWorkspace(root, in.Path)
			if err != nil {
				return "", err
			}
			b, err := os.ReadFile(p)
			if err != nil {
				return "", err
			}
			return truncate(string(b)), nil
		})
	if err != nil {
		return nil, err
	}

	grep, err := utils.InferTool("grep_search",
		"Search all .go and .md files in the repository for a substring. Returns matching lines as path:line:text.",
		func(_ context.Context, in *grepArgs) (string, error) {
			var sb strings.Builder
			matches := 0
			err := filepath.WalkDir(root, func(path string, d os.DirEntry, err error) error {
				if err != nil {
					return nil // skip unreadable entries
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
			if err != nil {
				return "", err
			}
			if sb.Len() == 0 {
				return "no matches", nil
			}
			return truncate(sb.String()), nil
		})
	if err != nil {
		return nil, err
	}

	return []tool.BaseTool{readFile, grep}, nil
}

// reasoningAwareToolCallChecker replaces eino's default first-chunk check:
// our model streams reasoning_content deltas long before any tool call, so
// the default would always conclude "no tool call". Drain reasoning-only
// chunks; a tool-call chunk means true, visible content or EOF means false.
func reasoningAwareToolCallChecker(_ context.Context, out *schema.StreamReader[*schema.Message]) (bool, error) {
	defer out.Close()
	for {
		msg, err := out.Recv()
		if errors.Is(err, io.EOF) {
			return false, nil
		}
		if err != nil {
			return false, err
		}
		if len(msg.ToolCalls) > 0 {
			return true, nil
		}
		if msg.Content != "" {
			return false, nil
		}
		// reasoning-only chunk: keep draining
	}
}

func main() {
	ctx := context.Background()
	baseURL := getenv("LLM_BASE_URL", "http://192.168.1.240:1234/v1")
	modelName := getenv("LLM_MODEL", "qwen3.6-35b-a3b-mtp")
	root := getenv("SPIKE_TARGET_DIR", ".") // workbench repo root when run via `go run ./spike/einoprobe` from the repo

	client := chat.NewOpenAIClient(baseURL, "", 10*time.Minute)
	base := newWBChatModel(client, modelName)

	tools, err := makeTools(root)
	if err != nil {
		fmt.Fprintln(os.Stderr, "tool setup:", err)
		os.Exit(1)
	}

	agent, err := react.NewAgent(ctx, &react.AgentConfig{
		ToolCallingModel:      base,
		ToolsConfig:           compose.ToolsNodeConfig{Tools: tools},
		MaxStep:               12,
		StreamToolCallChecker: reasoningAwareToolCallChecker,
	})
	if err != nil {
		fmt.Fprintln(os.Stderr, "agent setup:", err)
		os.Exit(1)
	}

	question := "Which Go interface in this repository defines the agent executor abstraction, and what methods does it have? Read whatever files you need."
	if len(os.Args) > 1 {
		question = strings.Join(os.Args[1:], " ")
	}

	fmt.Printf("=== einoprobe: %s @ %s ===\nQ: %s\n\n", modelName, baseURL, question)
	start := time.Now()

	out, err := agent.Stream(ctx, []*schema.Message{
		schema.SystemMessage("/no_think You are a code exploration assistant. Use the read_file and grep_search tools to ground every answer in the repository's actual contents. Keep answers short."),
		schema.UserMessage(question),
	})
	if err != nil {
		fmt.Fprintln(os.Stderr, "agent stream:", err)
		os.Exit(1)
	}
	defer out.Close()

	var mode string
	for {
		chunk, err := out.Recv()
		if errors.Is(err, io.EOF) {
			break
		}
		if err != nil {
			fmt.Fprintln(os.Stderr, "\nstream error:", err)
			os.Exit(1)
		}
		switch {
		case chunk.ReasoningContent != "":
			if mode != "reasoning" {
				fmt.Print("\n[reasoning] ")
				mode = "reasoning"
			}
			fmt.Print(chunk.ReasoningContent)
		case len(chunk.ToolCalls) > 0:
			mode = "tool"
			for _, tc := range chunk.ToolCalls {
				fmt.Printf("\n[tool call] %s(%s)\n", tc.Function.Name, tc.Function.Arguments)
			}
		case chunk.Content != "":
			if mode != "content" {
				fmt.Print("\n[answer] ")
				mode = "content"
			}
			fmt.Print(chunk.Content)
		}
	}
	fmt.Printf("\n\n=== done in %s ===\n", time.Since(start).Round(time.Millisecond))
}

// Command server runs the llm-workbench HTTP server: an API over the
// task/project repositories, a chat completions proxy to a local
// OpenAI-compatible LLM server, and the embedded frontend dashboard.
package main

import (
	"context"
	"io/fs"
	"net/http"
	"path/filepath"
	"time"

	"github.com/sirupsen/logrus"

	"github.com/timmersuk/llm-workbench/internal/agentrunner"
	"github.com/timmersuk/llm-workbench/internal/api"
	"github.com/timmersuk/llm-workbench/internal/chat"
	"github.com/timmersuk/llm-workbench/internal/knowledge"
	"github.com/timmersuk/llm-workbench/internal/project"
	"github.com/timmersuk/llm-workbench/internal/task"
	"github.com/timmersuk/llm-workbench/internal/utils"
	"github.com/timmersuk/llm-workbench/internal/web"
)

// BuildID is set via -ldflags at build time (see Makefile).
var BuildID = "dev"

func main() {
	httpAddr := utils.GetEnvDefault("HTTP_ADDR", ":8080")
	workspaceRoot := utils.GetEnvDefault("WORKSPACE_ROOT", "data")
	logLevel := utils.GetEnvDefault("LOG_LEVEL", "info")
	logFormat := utils.GetEnvDefault("LOG_FORMAT", "json")
	llmBaseURL := utils.GetEnvDefault("LLM_BASE_URL", "http://localhost:11434/v1")
	llmAPIKey := utils.GetEnvDefault("LLM_API_KEY", "")
	llmModel := utils.GetEnvDefault("LLM_MODEL", "llama3")
	// Idle timeout between streamed chunks (resets on every chunk received);
	// total-duration timeout for non-streaming calls.
	llmTimeout := utils.GetEnvDefault("LLM_TIMEOUT", 30*time.Second)
	agentTimeout := utils.GetEnvDefault("AGENT_TIMEOUT", 5*time.Minute)
	// Required: any agent runner that can introspect a task's reference
	// repository (claude-code today; codex/others later) needs to know
	// where to find/clone it, so the server refuses to start without one.
	agentReposRoot := utils.MustGetEnv("AGENT_REPOS_ROOT")

	configureLogging(logLevel, logFormat)

	projectStore := project.NewFileStore(filepath.Join(workspaceRoot, "projects"))
	chatClient := chat.NewOpenAIClient(llmBaseURL, llmAPIKey, llmTimeout)
	knowledgeReader := knowledge.NewFileReader(filepath.Join(workspaceRoot, "knowledge"))

	// One shared map: registered runners are selectable from both
	// Requirements/Planning stage conversations and the free-floating Chat
	// tab, and both consume it identically — there is no separate
	// stateless/bypass path for either. "local" wraps the same chatClient
	// used by defaultModelCompleter below, giving it session-held history
	// (chat.ChatClient.StreamSessionTurn) exactly like "claude-code".
	agentRunners := map[string]agentrunner.AgentRunner{
		"claude-code": agentrunner.NewClaudeRunner(agentTimeout, agentReposRoot),
		"local":       agentrunner.NewChatClientRunner(defaultModelCompleter{chatClient, llmModel}),
	}

	frontendFS, err := fs.Sub(web.Files, "dist")
	if err != nil {
		logrus.Fatalf("mounting embedded frontend: %v", err)
	}

	taskStores := func(root string) api.TaskStore { return task.NewFileStore(root) }
	router := api.NewRouter(projectStore, taskStores, knowledgeReader, agentRunners, agentReposRoot, frontendFS, BuildID)

	logrus.WithFields(logrus.Fields{
		"addr":           httpAddr,
		"workspaceRoot":  workspaceRoot,
		"llmBaseURL":     llmBaseURL,
		"llmModel":       llmModel,
		"agentReposRoot": agentReposRoot,
		"buildID":        BuildID,
	}).Info("starting llm-workbench server")

	if err := http.ListenAndServe(httpAddr, router); err != nil {
		logrus.Fatalf("server exited: %v", err)
	}
}

func configureLogging(level, format string) {
	lvl, err := logrus.ParseLevel(level)
	if err != nil {
		logrus.WithField("level", level).Warn("invalid LOG_LEVEL, defaulting to info")
		lvl = logrus.InfoLevel
	}
	logrus.SetLevel(lvl)

	if format == "json" {
		logrus.SetFormatter(&logrus.JSONFormatter{})
	} else {
		logrus.SetFormatter(&logrus.TextFormatter{})
	}
}

// defaultModelCompleter fills in the configured default model for requests
// that don't specify one, since M1 targets a single configured provider.
// It holds chat.ChatClient (the interface), never the concrete
// implementation, so it works regardless of which ChatClient is configured.
type defaultModelCompleter struct {
	client chat.ChatClient
	model  string
}

func (c defaultModelCompleter) CreateChatCompletion(ctx context.Context, req chat.CompletionRequest) (chat.CompletionResponse, error) {
	if req.Model == "" {
		req.Model = c.model
	}
	return c.client.CreateChatCompletion(ctx, req)
}

func (c defaultModelCompleter) StreamChatCompletion(ctx context.Context, req chat.CompletionRequest, onDelta func(chat.Delta) error) error {
	if req.Model == "" {
		req.Model = c.model
	}
	return c.client.StreamChatCompletion(ctx, req, onDelta)
}

func (c defaultModelCompleter) CheckHealth(ctx context.Context) error {
	return c.client.CheckHealth(ctx)
}

func (c defaultModelCompleter) ListModels(ctx context.Context) ([]string, error) {
	return c.client.ListModels(ctx)
}

func (c defaultModelCompleter) StreamSessionTurn(ctx context.Context, sessionKey, systemPrompt, model, userMessage string, tools []chat.Tool, onDelta func(chat.Delta) error) (string, *chat.ToolCall, error) {
	if model == "" {
		model = c.model
	}
	return c.client.StreamSessionTurn(ctx, sessionKey, systemPrompt, model, userMessage, tools, onDelta)
}

func (c defaultModelCompleter) CloseSession(sessionKey string) {
	c.client.CloseSession(sessionKey)
}

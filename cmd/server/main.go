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

	"github.com/timmersuk/llm-workbench/internal/api"
	"github.com/timmersuk/llm-workbench/internal/chat"
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
	llmTimeout := utils.GetEnvDefault("LLM_TIMEOUT", 30*time.Second)

	configureLogging(logLevel, logFormat)

	projectStore := project.NewFileStore(filepath.Join(workspaceRoot, "projects"))
	chatClient := chat.NewClient(llmBaseURL, llmAPIKey, llmTimeout)

	frontendFS, err := fs.Sub(web.Files, "dist")
	if err != nil {
		logrus.Fatalf("mounting embedded frontend: %v", err)
	}

	taskStores := func(root string) api.TaskStore { return task.NewFileStore(root) }
	router := api.NewRouter(projectStore, taskStores, defaultModelCompleter{chatClient, llmModel}, frontendFS, BuildID)

	logrus.WithFields(logrus.Fields{
		"addr":          httpAddr,
		"workspaceRoot": workspaceRoot,
		"llmBaseURL":    llmBaseURL,
		"llmModel":      llmModel,
		"buildID":       BuildID,
	}).Info("starting llm-workbench server")

	if err := http.ListenAndServe(httpAddr, router); err != nil {
		logrus.Fatalf("server exited: %v", err)
	}
}

func configureLogging(level, format string) {
	lvl, err := logrus.ParseLevel(level)
	if err != nil {
		logrus.Warnf("invalid LOG_LEVEL %q, defaulting to info", level)
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
type defaultModelCompleter struct {
	client *chat.Client
	model  string
}

func (c defaultModelCompleter) CreateChatCompletion(ctx context.Context, req chat.CompletionRequest) (chat.CompletionResponse, error) {
	if req.Model == "" {
		req.Model = c.model
	}
	return c.client.CreateChatCompletion(ctx, req)
}

func (c defaultModelCompleter) CheckHealth(ctx context.Context) error {
	return c.client.CheckHealth(ctx)
}

func (c defaultModelCompleter) ListModels(ctx context.Context) ([]string, error) {
	return c.client.ListModels(ctx)
}

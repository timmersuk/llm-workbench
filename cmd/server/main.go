// Command server runs the llm-workbench HTTP server: an API over the
// task/project repositories, a chat completions proxy to a local
// OpenAI-compatible LLM server, and the embedded frontend dashboard.
package main

import (
	"context"
	"fmt"
	"io/fs"
	"net/http"
	"os"
	"os/signal"
	"path/filepath"
	"runtime"
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

// config holds every value loadConfig reads from the environment — kept as
// a single struct (rather than a long parameter list) since run() needs
// nearly all of it.
type config struct {
	httpAddr              string
	workspaceRoot         string
	logLevel              string
	logFormat             string
	llmBaseURL            string
	llmAPIKey             string
	llmModel              string
	llmTimeout            time.Duration
	agentTimeout          time.Duration
	agentExecutionTimeout time.Duration
	agentReposRoot        string
	draftMCPPath          string
	shutdownTimeout       time.Duration
}

// loadConfig reads every environment-derived setting main() needs.
// AGENT_REPOS_ROOT is the one hard startup requirement (via
// utils.MustGetEnv, which itself calls logrus.Fatal and exits the process)
// — every agent runner capable of introspecting a task's reference
// repository needs it, so the server refuses to start without one. Kept as
// its own step (called directly from main(), before run()) rather than
// folded into run() so run() itself never has a Fatal-style exit path: it
// stays main()'s sole point of process termination.
func loadConfig() config {
	return config{
		httpAddr:      utils.GetEnvDefault("HTTP_ADDR", ":8080"),
		workspaceRoot: utils.GetEnvDefault("WORKSPACE_ROOT", "data"),
		logLevel:      utils.GetEnvDefault("LOG_LEVEL", "info"),
		logFormat:     utils.GetEnvDefault("LOG_FORMAT", "json"),
		llmBaseURL:    utils.GetEnvDefault("LLM_BASE_URL", "http://localhost:11434/v1"),
		llmAPIKey:     utils.GetEnvDefault("LLM_API_KEY", ""),
		llmModel:      utils.GetEnvDefault("LLM_MODEL", "llama3"),
		// Idle timeout between streamed chunks (resets on every chunk received);
		// total-duration timeout for non-streaming calls.
		llmTimeout:   utils.GetEnvDefault("LLM_TIMEOUT", 30*time.Second),
		agentTimeout: utils.GetEnvDefault("AGENT_TIMEOUT", 5*time.Minute),
		// Execute (an unattended, multi-step Implementation-stage run to
		// completion) needs a much larger budget than Run (one turn of a
		// human-paced conversation) — sharing AGENT_TIMEOUT between them cut
		// autonomous executions off mid-run well before they could finish (see
		// docs/engineering conventions.md's AGENT_TIMEOUT entry).
		agentExecutionTimeout: utils.GetEnvDefault("AGENT_EXECUTION_TIMEOUT", 30*time.Minute),
		// Required: any agent runner that can introspect a task's reference
		// repository (claude-code, codex) needs to know where to find/clone
		// it, so the server refuses to start without one.
		agentReposRoot: utils.MustGetEnv("AGENT_REPOS_ROOT"),
		// draftmcp is built as a sibling binary alongside this one (see
		// Makefile's build-go-local) — defaults to that convention, override
		// via DRAFTMCP_PATH for a non-standard layout (e.g. local `go run`).
		draftMCPPath: utils.GetEnvDefault("DRAFTMCP_PATH", defaultDraftMCPPath()),
		// Bounds how long graceful shutdown (http.Server.Shutdown plus
		// per-agent-runner cleanup, e.g. ClaudeRunner.CloseAll disconnecting
		// cached `claude` CLI subprocesses) is allowed to take after Ctrl+C
		// before the process gives up waiting. See docs/engineering
		// conventions.md's AGENT_TIMEOUT/LLM_TIMEOUT entries for the same
		// GetEnvDefault pattern.
		shutdownTimeout: utils.GetEnvDefault("SHUTDOWN_TIMEOUT", 10*time.Second),
	}
}

func main() {
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt)
	defer stop()

	cfg := loadConfig()

	if err := run(ctx, cfg); err != nil {
		logrus.WithError(err).Fatal("server exited")
	}
}

// run wires up and serves the application until ctx is cancelled (a Ctrl+C
// per main()'s signal.NotifyContext), then drains in-flight requests and
// releases held resources within cfg.shutdownTimeout before returning. It
// is the sole place the server's HTTP listener lives — main() itself has no
// os.Exit/logrus.Fatal path other than reporting run's returned error, so
// this function must never call one either (that would bypass shutdown
// entirely, e.g. leaving a `claude` CLI subprocess connected).
func run(ctx context.Context, cfg config) error {
	configureLogging(cfg.logLevel, cfg.logFormat)

	projectsRoot := filepath.Join(cfg.workspaceRoot, "projects")
	projectStore := project.NewFileStore(projectsRoot)
	// One process-wide task store, not one per project: every task.Store
	// method takes an explicit projectID, and this FileStore's Root is the
	// same shared projects directory project.FileStore uses (tasks nest
	// under <projectsRoot>/<projectID>/tasks/), so a single instance serves
	// every project's task routes (internal/api/tasks.go's
	// resolveTaskStore).
	taskStore := task.NewFileStore(projectsRoot)
	knowledgeRoot := filepath.Join(cfg.workspaceRoot, "knowledge")
	knowledgeStore := knowledge.NewFileStore(knowledgeRoot)

	// One shared map: registered runners are selectable from both
	// Requirements/Planning stage conversations and the free-floating Chat
	// tab, and both consume it identically — there is no separate
	// stateless/bypass path for either. "local" wraps the same chatClient
	// used by defaultModelCompleter below, giving it session-held history
	// (chat.ChatClient.StreamSessionTurn) exactly like "claude-code". Every
	// runner is given the same knowledgeStore/knowledgeRoot so the
	// always-available knowledge query tools (docs/milestones/done/milestone9.md)
	// answer identically regardless of which executor a conversation uses.
	agentRunners := map[string]agentrunner.AgentRunner{
		"claude-code": agentrunner.NewClaudeRunner(cfg.agentTimeout, cfg.agentExecutionTimeout, cfg.agentReposRoot, knowledgeStore),
		"codex":       agentrunner.NewCodexRunner(cfg.agentTimeout, cfg.agentExecutionTimeout, cfg.agentReposRoot, cfg.draftMCPPath, knowledgeRoot),
		"local": agentrunner.NewChatClientRunner(defaultModelCompleter{
			client: chat.NewOpenAIClient(cfg.llmBaseURL, cfg.llmAPIKey, cfg.llmTimeout),
			model:  cfg.llmModel,
		}, knowledgeStore),
	}

	frontendFS, err := fs.Sub(web.Files, "dist")
	if err != nil {
		return fmt.Errorf("mounting embedded frontend: %w", err)
	}

	router := api.NewRouter(projectStore, taskStore, knowledgeStore, agentRunners, cfg.agentReposRoot, agentrunner.NewGitHubPRClient(), agentrunner.NewDefaultBranchResolver(), frontendFS, BuildID)

	logrus.WithFields(logrus.Fields{
		"addr":           cfg.httpAddr,
		"workspaceRoot":  cfg.workspaceRoot,
		"llmBaseURL":     cfg.llmBaseURL,
		"llmModel":       cfg.llmModel,
		"agentReposRoot": cfg.agentReposRoot,
		"buildID":        BuildID,
	}).Info("starting llm-workbench server")

	srv := &http.Server{Addr: cfg.httpAddr, Handler: router}

	serveErr := make(chan error, 1)
	go func() { serveErr <- srv.ListenAndServe() }()

	select {
	case err := <-serveErr:
		if err != nil && err != http.ErrServerClosed {
			return fmt.Errorf("server exited: %w", err)
		}
		return nil
	case <-ctx.Done():
		logrus.Info("shutdown signal received, draining in-flight requests")
	}

	shutdownCtx, cancel := context.WithTimeout(context.Background(), cfg.shutdownTimeout)
	defer cancel()

	shutdownErr := srv.Shutdown(shutdownCtx)

	// Release any per-agent-runner resources (e.g. ClaudeRunner's cached
	// `claude` CLI subprocesses) that would otherwise be orphaned rather
	// than terminated when this process exits. Deliberately a type
	// assertion, not an AgentRunner interface method — CloseAll is
	// claude-CLI-specific cleanup, and ChatClientRunner/CodexRunner own no
	// such resource, so they'd need only a no-op stub to satisfy an
	// interface method.
	for _, r := range agentRunners {
		if closer, ok := r.(interface{ CloseAll() }); ok {
			closer.CloseAll()
		}
	}

	// Drain the ListenAndServe goroutine so it never leaks — Shutdown
	// guarantees ListenAndServe returns (with http.ErrServerClosed on the
	// clean-shutdown path), so this receive cannot block indefinitely.
	if err := <-serveErr; err != nil && err != http.ErrServerClosed {
		return fmt.Errorf("server exited: %w", err)
	}
	if shutdownErr != nil {
		return fmt.Errorf("graceful shutdown: %w", shutdownErr)
	}
	return nil
}

// defaultDraftMCPPath returns the expected path of cmd/draftmcp's compiled
// binary, sitting alongside this server binary (see Makefile's
// build-go-local). Falls back to a bare "draftmcp" (PATH-resolved by the
// codex CLI) if the server's own executable path can't be determined —
// CodexRunner.CheckHealth surfaces a clear error rather than this silently
// producing an unusable path.
func defaultDraftMCPPath() string {
	exe, err := os.Executable()
	if err != nil {
		return "draftmcp"
	}
	name := "draftmcp"
	if runtime.GOOS == "windows" {
		name += ".exe"
	}
	return filepath.Join(filepath.Dir(exe), name)
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

func (c defaultModelCompleter) CloseSession(sessionKey string) {
	c.client.CloseSession(sessionKey)
}

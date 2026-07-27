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
	"sync"
	"time"

	"github.com/sirupsen/logrus"

	"github.com/timmersuk/llm-workbench/internal/agentrunner"
	"github.com/timmersuk/llm-workbench/internal/api"
	"github.com/timmersuk/llm-workbench/internal/chat"
	"github.com/timmersuk/llm-workbench/internal/gitstore"
	"github.com/timmersuk/llm-workbench/internal/knowledge"
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
	dataRepoURL           string
	pushInterval          time.Duration
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
// AGENT_REPOS_ROOT and DATA_REPO_URL are hard startup requirements (via
// utils.MustGetEnv, which itself calls logrus.Fatal and exits the process)
// — every agent runner capable of introspecting a task's reference
// repository needs the former; the latter is gitstore.Open's mandatory
// "origin" remote (see run()'s doc comment below for the subprocess-boot
// provisioning pattern a caller must follow). Kept as its own step (called
// directly from main(), before run()) rather than folded into run() so
// run() itself never has a Fatal-style exit path: it stays main()'s sole
// point of process termination.
func loadConfig() config {
	return config{
		httpAddr:      utils.GetEnvDefault("HTTP_ADDR", ":8080"),
		workspaceRoot: utils.GetEnvDefault("WORKSPACE_ROOT", "data"),
		// DATA_REPO_URL is treated strictly as a local filesystem path this
		// milestone — no network transport, no auth (remote hosting/
		// credentials are punted to a future milestone). See gitstore.Open's
		// doc comment for the clone/resume/ambiguous-workspace contract this
		// drives.
		dataRepoURL: utils.MustGetEnv("DATA_REPO_URL"),
		// How often the background push worker (gitstore.Store.RunPushWorker)
		// attempts to push accumulated local commits to DATA_REPO_URL.
		pushInterval: utils.GetEnvDefault("PUSH_INTERVAL", 30*time.Second),
		logLevel:     utils.GetEnvDefault("LOG_LEVEL", "info"),
		logFormat:    utils.GetEnvDefault("LOG_FORMAT", "json"),
		llmBaseURL:   utils.GetEnvDefault("LLM_BASE_URL", "http://localhost:11434/v1"),
		llmAPIKey:    utils.GetEnvDefault("LLM_API_KEY", ""),
		llmModel:     utils.GetEnvDefault("LLM_MODEL", "llama3"),
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
//
// Design note — subprocess-boot provisioning for tests: gitstore.Open
// (internal/gitstore/gitstore.go) requires cfg.dataRepoURL to already name
// a real git remote before this process starts — it never creates one
// itself. Any harness that boots this binary as a real subprocess (a
// future e2e-tests task, per this milestone's plan — not implemented or
// verified here) must therefore provision that remote first:
//
//  1. Create an empty bare repository in a throwaway directory, e.g. via
//     `git init --bare <tmp>/data-remote.git` (or go-git's
//     `git.PlainInit(dir, true)`, exactly as this package's own tests do —
//     see internal/gitstore/gitstore_test.go's newBareRemote helper).
//  2. Export DATA_REPO_URL=<tmp>/data-remote.git (and WORKSPACE_ROOT
//     pointing at another empty throwaway directory) in the subprocess's
//     environment before starting it.
//
// gitstore.Open's clone step (an empty bare remote) falls back to a local
// PlainInit + origin remote, exactly the "empty WORKSPACE_ROOT, fresh
// clone" startup path documented on gitstore.Open — so this is the
// harness-side equivalent of a brand-new deployment's first boot, not a
// special test-only code path in the server itself.
func run(ctx context.Context, cfg config) error {
	configureLogging(cfg.logLevel, cfg.logFormat)

	store, err := gitstore.Open(cfg.workspaceRoot, cfg.dataRepoURL)
	if err != nil {
		return fmt.Errorf("opening git-backed data store: %w", err)
	}

	// Push worker outlives no request — it runs for exactly as long as run()
	// itself does. pushCtx is its own child of ctx (not ctx directly) so
	// that every one of run()'s exit paths — not just "ctx cancelled" — stops
	// it: a real ListenAndServe failure, say, must return promptly rather
	// than block up to ctx's own deadline waiting for the worker to notice.
	// The deferred cancel-then-wait guarantees the worker is both told to
	// stop and actually stopped before run() returns, so no push is left
	// mid-flight when the process exits.
	pushCtx, cancelPush := context.WithCancel(ctx)
	var pushWG sync.WaitGroup
	pushWG.Add(1)
	go func() {
		defer pushWG.Done()
		store.RunPushWorker(pushCtx, cfg.pushInterval)
	}()
	defer func() {
		cancelPush()
		pushWG.Wait()
	}()

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

	router := api.NewRouter(store.Projects, store.Tasks, knowledgeStore, agentRunners, cfg.agentReposRoot, agentrunner.NewGitHubPRClient(), agentrunner.NewDefaultBranchResolver(), frontendFS, BuildID)

	logrus.WithFields(logrus.Fields{
		"addr":           cfg.httpAddr,
		"workspaceRoot":  cfg.workspaceRoot,
		"dataRepoURL":    cfg.dataRepoURL,
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

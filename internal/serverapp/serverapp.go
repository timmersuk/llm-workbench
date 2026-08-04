// Package serverapp holds the llm-workbench server's config loading and
// wiring (config/LoadConfig/Run), extracted out of cmd/server/main.go so a
// second binary (cmd/tray) can host the exact same server in-process
// alongside a systray icon, with zero duplicated env-parsing or
// shutdown-signal logic. cmd/server itself becomes a thin wrapper: it calls
// LoadConfig, stamps its own build-time BuildID into the returned Config,
// and calls Run — see cmd/server/main.go and cmd/tray/main.go.
package serverapp

import (
	"context"
	"errors"
	"fmt"
	"io/fs"
	"net/http"
	"path"
	"path/filepath"
	"regexp"
	"strings"
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

// Config holds every value LoadConfig reads from the environment — kept as
// a single struct (rather than a long parameter list) since Run needs
// nearly all of it. Fields are exported (unlike the pre-extraction
// cmd/server-private config struct) since Config is now a cross-package
// boundary: cmd/server and cmd/tray both construct/read it directly rather
// than reaching into serverapp internals. BuildID is deliberately not
// populated by LoadConfig itself — it comes from each binary's own
// linker-stamped var (see cmd/server/main.go, cmd/tray/main.go), so each
// caller sets it on the returned Config before calling Run.
type Config struct {
	HTTPAddr                      string
	ReposRoot                     string
	WorkspaceRoot                 string
	DataRepoURL                   string
	PushInterval                  time.Duration
	LogLevel                      string
	LogFormat                     string
	LLMBaseURL                    string
	LLMAPIKey                     string
	LLMDefaultEffort              agentrunner.ReasoningEffort
	StageConversationSeedExecutor string
	ExecutionSeedExecutor         string
	LLMTimeout                    time.Duration
	AgentTimeout                  time.Duration
	AgentExecutionTimeout         time.Duration
	ShutdownTimeout               time.Duration
	// BuildID identifies the running binary (git rev-parse --short HEAD by
	// convention, see each cmd/*/main.go's BuildID var). Not read from the
	// environment — callers set it on the Config LoadConfig returns, before
	// calling Run.
	BuildID string
}

// LoadConfig reads every environment-derived setting Run needs.
// REPOS_ROOT and DATA_REPO_URL are hard startup requirements (via
// utils.MustGetEnv, which itself calls logrus.Fatal and exits the process)
// — REPOS_ROOT is the shared parent directory every sibling repo (the
// gitstore data checkout, and every project's code repository an agent
// runner needs to introspect) is checked out under; DATA_REPO_URL is
// gitstore.Open's mandatory "origin" remote (see Run's doc comment below
// for the subprocess-boot provisioning pattern a caller must follow).
// WorkspaceRoot is derived, not read directly: REPOS_ROOT joined with a
// directory name derived from DATA_REPO_URL (repoDirName), the same
// convention `git clone` itself uses to name a destination directory from
// a remote URL — so the data checkout is just another sibling repo under
// REPOS_ROOT. Kept as its own step (called directly from main(), before
// Run()) rather than folded into Run() so Run() itself never has a
// Fatal-style exit path: it stays main()'s sole point of process
// termination.
func LoadConfig() Config {
	reposRoot := utils.MustGetEnv("REPOS_ROOT")
	// DATA_REPO_URL is a real git remote URL — anything `git clone`
	// itself accepts (a GitHub HTTPS/SSH URL, a local path, etc.).
	// gitstore shells out to the `git` binary rather than a pure-Go
	// library specifically so auth is never this process's problem:
	// clone/push run through whatever credential helper or SSH agent
	// the operator's machine already has configured, exactly as if
	// they'd typed the command themselves. See gitstore.Open's doc
	// comment for the clone/resume/ambiguous-workspace contract this
	// drives.
	dataRepoURL := utils.MustGetEnv("DATA_REPO_URL")

	dataDirName, err := repoDirName(dataRepoURL)
	if err != nil {
		logrus.WithError(err).WithField("dataRepoURL", dataRepoURL).
			Fatal("cannot derive a workspace directory name from DATA_REPO_URL")
	}

	return Config{
		HTTPAddr:  utils.GetEnvDefault("HTTP_ADDR", ":8080"),
		ReposRoot: reposRoot,
		// The data checkout is just another sibling repo under reposRoot,
		// named the same way `git clone` itself would name its destination
		// directory from dataRepoURL (see repoDirName).
		WorkspaceRoot: filepath.Join(reposRoot, dataDirName),
		DataRepoURL:   dataRepoURL,
		// How often the background push worker (gitstore.Store.RunPushWorker)
		// attempts to push accumulated local commits to DATA_REPO_URL.
		PushInterval:                  utils.GetEnvDefault("PUSH_INTERVAL", 30*time.Second),
		LogLevel:                      utils.GetEnvDefault("LOG_LEVEL", "info"),
		LogFormat:                     utils.GetEnvDefault("LOG_FORMAT", "json"),
		LLMBaseURL:                    utils.GetEnvDefault("LLM_BASE_URL", "http://localhost:11434/v1"),
		LLMAPIKey:                     utils.GetEnvDefault("LLM_API_KEY", ""),
		LLMDefaultEffort:              agentrunner.ReasoningEffort(utils.GetEnvDefault("LLM_DEFAULT_EFFORT", "medium")),
		StageConversationSeedExecutor: utils.GetEnvDefault("STAGE_CONVERSATION_SEED_EXECUTOR", "local"),
		ExecutionSeedExecutor:         utils.GetEnvDefault("EXECUTION_SEED_EXECUTOR", "claude-code"),
		// Idle timeout between streamed chunks (resets on every chunk received);
		// total-duration timeout for non-streaming calls.
		LLMTimeout:   utils.GetEnvDefault("LLM_TIMEOUT", 30*time.Second),
		AgentTimeout: utils.GetEnvDefault("AGENT_TIMEOUT", 5*time.Minute),
		// Execute (an unattended, multi-step Implementation-stage run to
		// completion) needs a much larger budget than Run (one turn of a
		// human-paced conversation) — sharing AGENT_TIMEOUT between them cut
		// autonomous executions off mid-run well before they could finish (see
		// docs/engineering conventions.md's AGENT_TIMEOUT entry).
		AgentExecutionTimeout: utils.GetEnvDefault("AGENT_EXECUTION_TIMEOUT", 30*time.Minute),
		// Bounds how long graceful shutdown (http.Server.Shutdown plus
		// per-agent-runner cleanup, e.g. ClaudeRunner.CloseAll disconnecting
		// cached `claude` CLI subprocesses) is allowed to take after Ctrl+C
		// before the process gives up waiting. See docs/engineering
		// conventions.md's AGENT_TIMEOUT/LLM_TIMEOUT entries for the same
		// GetEnvDefault pattern.
		ShutdownTimeout: utils.GetEnvDefault("SHUTDOWN_TIMEOUT", 10*time.Second),
	}
}

// scpLikeSSH matches the scp-like SSH shorthand git accepts as a clone URL
// ("user@host:path/repo.git"). Requiring an "@" before the colon is what
// keeps this from misfiring on a Windows drive-letter path ("D:\repo"),
// which has a colon but no "@".
var scpLikeSSH = regexp.MustCompile(`^[^/@\s]+@[^/\s]+:`)

// repoDirName derives the local directory name a git remote (or a plain
// local filesystem path, as used directly by tests and local dev) should
// be checked out under as a sibling repo beneath REPOS_ROOT — mirroring
// the convention `git clone` itself uses: a clone's directory takes its
// name from the remote's last path segment with a trailing ".git"
// stripped (e.g. both "https://github.com/timmersuk/llm-workbench-data.git"
// and "git@github.com:timmersuk/llm-workbench-data.git" name the
// directory "llm-workbench-data"). Handles every form DATA_REPO_URL's own
// doc comment says `git clone` itself accepts: a scheme'd URL
// (https/http/ssh/git/...), the scp-like ssh shorthand, or a plain local
// filesystem path (POSIX or Windows, absolute or relative). Returns an
// error if the derived name would be empty, ".", or "/".
func repoDirName(url string) (string, error) {
	trimmed := strings.TrimSpace(url)

	switch {
	case strings.Contains(trimmed, "://"):
		trimmed = trimmed[strings.Index(trimmed, "://")+len("://"):]
	case scpLikeSSH.MatchString(trimmed):
		trimmed = trimmed[strings.Index(trimmed, ":")+1:]
	}

	// Normalize Windows separators so path.Base sees forward slashes
	// regardless of whether trimmed came from a URL or a native path.
	trimmed = strings.ReplaceAll(trimmed, `\`, "/")

	base := strings.TrimSuffix(path.Base(trimmed), ".git")
	if base == "" || base == "." || base == "/" {
		return "", fmt.Errorf("cannot derive a directory name from %q", url)
	}
	return base, nil
}

// Run wires up and serves the application until ctx is cancelled (a Ctrl+C
// per main()'s signal.NotifyContext, or an explicit cancel from cmd/tray's
// Quit action), then drains in-flight requests and releases held resources
// within cfg.ShutdownTimeout before returning. It is the sole place the
// server's HTTP listener lives — every caller's main() has no
// os.Exit/logrus.Fatal path other than reporting Run's returned error, so
// this function must never call one either (that would bypass shutdown
// entirely, e.g. leaving a `claude` CLI subprocess connected).
//
// Design note — subprocess-boot provisioning for tests: gitstore.Open
// (internal/gitstore/gitstore.go) requires cfg.DataRepoURL to already name
// a real git remote before this process starts — it never creates one
// itself. Any harness that boots a caller binary as a real subprocess (a
// future e2e-tests task, per this milestone's plan — not implemented or
// verified here) must therefore provision that remote first:
//
//  1. Create an empty bare repository in a throwaway directory, e.g. via
//     `git init --bare <tmp>/data-remote.git`, exactly as this package's
//     own tests do — see internal/gitstore/gitstore_test.go's
//     newBareRemote helper.
//  2. Export DATA_REPO_URL=<tmp>/data-remote.git and REPOS_ROOT pointing at
//     another empty throwaway directory in the subprocess's environment
//     before starting it — LoadConfig derives WorkspaceRoot as REPOS_ROOT
//     joined with a name derived from DATA_REPO_URL (see repoDirName), so
//     an empty REPOS_ROOT still produces an empty, absent WorkspaceRoot
//     dir, satisfying gitstore.Open's "clone" path.
//
// gitstore.Open's clone step (an empty bare remote) is just `git clone`,
// which succeeds against an empty remote the same way it would for a
// human — no special-case fallback needed, so this is the harness-side
// equivalent of a brand-new deployment's first boot, not a special
// test-only code path in the server itself.
func Run(ctx context.Context, cfg Config) error {
	configureLogging(cfg.LogLevel, cfg.LogFormat)

	store, err := gitstore.Open(cfg.WorkspaceRoot, cfg.DataRepoURL)
	if err != nil {
		return fmt.Errorf("opening git-backed data store: %w", err)
	}

	// Push worker outlives no request — it runs for exactly as long as Run()
	// itself does. pushCtx is its own child of ctx (not ctx directly) so
	// that every one of Run()'s exit paths — not just "ctx cancelled" — stops
	// it: a real ListenAndServe failure, say, must return promptly rather
	// than block up to ctx's own deadline waiting for the worker to notice.
	// The deferred cancel-then-wait guarantees the worker is both told to
	// stop and actually stopped before Run() returns, so no push is left
	// mid-flight when the process exits.
	pushCtx, cancelPush := context.WithCancel(ctx)
	var pushWG sync.WaitGroup
	pushWG.Add(1)
	go func() {
		defer pushWG.Done()
		store.RunPushWorker(pushCtx, cfg.PushInterval)
	}()
	defer func() {
		cancelPush()
		pushWG.Wait()
	}()

	knowledgeRoot := filepath.Join(cfg.WorkspaceRoot, "knowledge")
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
		"claude-code": agentrunner.NewClaudeRunner(cfg.AgentTimeout, cfg.AgentExecutionTimeout, cfg.ReposRoot, knowledgeStore),
		"codex":       agentrunner.NewCodexRunner(cfg.AgentTimeout, cfg.AgentExecutionTimeout, cfg.ReposRoot, knowledgeStore),
		"local": agentrunner.NewChatClientRunner(defaultModelCompleter{
			client: chat.NewOpenAIClient(cfg.LLMBaseURL, cfg.LLMAPIKey, cfg.LLMTimeout),
		}, knowledgeStore, agentrunner.Selection{Executor: "local", Effort: cfg.LLMDefaultEffort}),
	}
	if cfg.StageConversationSeedExecutor == "" {
		cfg.StageConversationSeedExecutor = "local"
	}
	if cfg.ExecutionSeedExecutor == "" {
		cfg.ExecutionSeedExecutor = "claude-code"
	}
	for _, seed := range []string{cfg.StageConversationSeedExecutor, cfg.ExecutionSeedExecutor} {
		runner, ok := agentRunners[seed]
		if !ok {
			return fmt.Errorf("agent default seed executor %q is not configured", seed)
		}
		capability, err := runner.Capabilities(ctx)
		if err != nil {
			return fmt.Errorf("loading capabilities for seed executor %q: %w", seed, err)
		}
		capability.Name = seed
		if err := agentrunner.ValidateSelection(capability.DefaultSelection(), capability); err != nil {
			return fmt.Errorf("invalid defaults for seed executor %q: %w", seed, err)
		}
	}

	frontendFS, err := fs.Sub(web.Files, "dist")
	if err != nil {
		return fmt.Errorf("mounting embedded frontend: %w", err)
	}

	router := api.NewRouterWithSeeds(store.Projects, store.Tasks, knowledgeStore, agentRunners, cfg.ReposRoot, agentrunner.NewGitHubPRClient(), agentrunner.NewDefaultBranchResolver(), frontendFS, cfg.BuildID, cfg.StageConversationSeedExecutor, cfg.ExecutionSeedExecutor)

	logrus.WithFields(logrus.Fields{
		"addr":          cfg.HTTPAddr,
		"reposRoot":     cfg.ReposRoot,
		"workspaceRoot": cfg.WorkspaceRoot,
		"dataRepoURL":   cfg.DataRepoURL,
		"llmBaseURL":    cfg.LLMBaseURL,
		"buildID":       cfg.BuildID,
	}).Info("starting llm-workbench server")

	srv := &http.Server{Addr: cfg.HTTPAddr, Handler: router}

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

	shutdownCtx, cancel := context.WithTimeout(context.Background(), cfg.ShutdownTimeout)
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

// defaultModelCompleter fills in a default model for requests that don't
// specify one, by asking the wrapped client which models it actually serves
// (client.ListModels) and using the first one — there is no separately
// configured "default model" to drift out of sync with the live endpoint.
// It holds chat.ChatClient (the interface), never the concrete
// implementation, so it works regardless of which ChatClient is configured.
type defaultModelCompleter struct {
	client chat.ChatClient
}

func (c defaultModelCompleter) resolveModel(ctx context.Context, model string) (string, error) {
	if model != "" {
		return model, nil
	}
	models, err := c.client.ListModels(ctx)
	if err != nil {
		return "", fmt.Errorf("resolving default model: %w", err)
	}
	if len(models) == 0 {
		return "", errors.New("resolving default model: LLM endpoint reports no available models")
	}
	return models[0], nil
}

func (c defaultModelCompleter) CreateChatCompletion(ctx context.Context, req chat.CompletionRequest) (chat.CompletionResponse, error) {
	model, err := c.resolveModel(ctx, req.Model)
	if err != nil {
		return chat.CompletionResponse{}, err
	}
	req.Model = model
	return c.client.CreateChatCompletion(ctx, req)
}

func (c defaultModelCompleter) StreamChatCompletion(ctx context.Context, req chat.CompletionRequest, onDelta func(chat.Delta) error) error {
	model, err := c.resolveModel(ctx, req.Model)
	if err != nil {
		return err
	}
	req.Model = model
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

package agentrunner

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"path/filepath"
	"slices"
	"strings"
	"sync"
	"time"

	codex "github.com/hishamkaram/codex-agent-sdk-go"
	"github.com/hishamkaram/codex-agent-sdk-go/types"

	"github.com/timmersuk/llm-workbench/internal/chat"
	"github.com/timmersuk/llm-workbench/internal/drafttool"
	"github.com/timmersuk/llm-workbench/internal/gitutil"
	"github.com/timmersuk/llm-workbench/internal/knowledgetool"
)

// codexDraftServerName is the MCP server name CodexRunner registers
// cmd/draftmcp's binary under (config key: mcp_servers.<name>). Chosen to
// be distinctive enough it won't collide with a user's own MCP servers.
const codexDraftServerName = "llm-workbench-draft"

// codexKickoffMessage is Execute's fixed opening turn — mirrors
// claude_runner.go's executionKickoffMessage; all real instructions live
// in ExecuteInput.SystemPrompt (sent via developerInstructionsArgs).
const codexKickoffMessage = "Begin executing the plan."

// codexThread narrows *codex.Thread to what CodexRunner needs from it — an
// interface so tests can substitute a fake thread without a live `codex
// app-server` subprocess, the same seam claude_runner.go's
// claudecode.Client interface already provides for ClaudeRunner.
type codexThread interface {
	ID() string
	RunStreamed(ctx context.Context, prompt string, opts *types.RunOptions) (<-chan types.ThreadEvent, error)
}

// codexClient narrows *codex.Client to what CodexRunner needs from it — an
// interface for the same testability reason as codexThread.
// realCodexClient (below) adapts a live *codex.Client to this interface;
// tests substitute their own fake implementation via
// CodexRunner.newCodexClient.
type codexClient interface {
	Connect(ctx context.Context) error
	Close(ctx context.Context) error
	StartThread(ctx context.Context, opts *types.ThreadOptions) (codexThread, error)
	ResumeThread(ctx context.Context, threadID string, opts *types.ResumeOptions) (codexThread, error)
}

// realCodexClient adapts a live *codex.Client to the codexClient
// interface — Connect/Close are promoted directly via embedding (their
// signatures already match); StartThread/ResumeThread need explicit
// adapters purely because the concrete SDK returns *codex.Thread rather
// than the codexThread interface (which it satisfies structurally, so the
// adapter is a one-line forward, not real logic).
type realCodexClient struct{ *codex.Client }

func (c realCodexClient) StartThread(ctx context.Context, opts *types.ThreadOptions) (codexThread, error) {
	return c.Client.StartThread(ctx, opts)
}

func (c realCodexClient) ResumeThread(ctx context.Context, threadID string, opts *types.ResumeOptions) (codexThread, error) {
	return c.Client.ResumeThread(ctx, threadID, opts)
}

// newRealCodexClient is CodexRunner.newCodexClient's production default —
// indirected purely so tests can substitute a fake client/thread pair
// without spawning a real `codex app-server` subprocess.
func newRealCodexClient(ctx context.Context, opts *types.CodexOptions) (codexClient, error) {
	c, err := codex.NewClient(ctx, opts)
	if err != nil {
		return nil, err
	}
	return realCodexClient{c}, nil
}

// CodexRunner implements AgentRunner backed by
// github.com/hishamkaram/codex-agent-sdk-go, which drives `codex
// app-server` as a subprocess over JSON-RPC. One client+thread pair is
// created lazily per RunInput.SessionKey and kept alive until CloseSession
// is called for that key — mirroring ClaudeRunner's clients cache exactly
// (developer_instructions, unlike severity1's system prompt, is still a
// process-level option fixed at client-construction time, so a client
// cannot be shared across keys with different prompts/workspaces either).
//
// codex-agent-sdk-go has no in-process "SDK MCP server" helper (unlike
// severity1's WithSdkMcpServer) and its per-thread WithMCPServers config
// does not reliably reach the underlying `codex` CLI (verified against
// codex-cli 0.143.0 — the registered tool never appeared in the model's
// tool list). The only mechanism that actually works is a *statically*
// registered MCP server (persisted to the user's codex config, the same
// as running `codex mcp add`) plus a persisted per-tool
// `approval_mode: "approve"` — both confirmed to let a brand-new,
// never-before-seen tool succeed on its very first non-interactive call,
// with no human approval step. ensureRegistered does this once, lazily,
// via Client.WriteConfigBatch.
type CodexRunner struct {
	mu       sync.Mutex
	inFlight map[string]bool
	clients  map[string]codexClient
	threads  map[string]codexThread

	timeout        time.Duration
	executeTimeout time.Duration
	reposRoot      string
	draftMCPPath   string
	knowledgeRoot  string

	registerOnce sync.Once
	registerErr  error

	// newCodexClient constructs a codexClient from ctx/opts — indirected
	// (defaulting to newRealCodexClient) purely so tests can substitute a
	// fake client without spawning a real `codex app-server` subprocess,
	// the same seam ClaudeRunner.newClient provides.
	newCodexClient func(ctx context.Context, opts *types.CodexOptions) (codexClient, error)
}

// NewCodexRunner returns a CodexRunner whose Run calls are each bounded by
// timeout and whose Execute calls are separately bounded by executeTimeout
// — split the same way and for the same reason as NewClaudeRunner's
// timeout/executeTimeout, including runTimeout's EnableBashTool exception
// (see NewClaudeRunner's doc comment). reposRoot is the configured REPOS_ROOT
// value (same role as NewClaudeRunner's). draftMCPPath is the absolute path
// to the compiled cmd/draftmcp binary; CodexRunner registers it as an MCP
// server the first time Run or Execute is actually called (see
// ensureRegistered), not at construction time. knowledgeRoot, if non-empty,
// is passed to that same draftmcp process as its --knowledge-root flag
// (docs/milestones/done/milestone9.md), so codex threads get the same real
// list_knowledge_concepts/get_knowledge_concept tools ClaudeRunner and
// ChatClientRunner do — an empty knowledgeRoot just omits the flag, the
// same as running draftmcp directly with no --knowledge-root.
func NewCodexRunner(timeout, executeTimeout time.Duration, reposRoot string, draftMCPPath string, knowledgeRoot string) *CodexRunner {
	return &CodexRunner{
		inFlight:       make(map[string]bool),
		clients:        make(map[string]codexClient),
		threads:        make(map[string]codexThread),
		timeout:        timeout,
		executeTimeout: executeTimeout,
		reposRoot:      reposRoot,
		draftMCPPath:   draftMCPPath,
		knowledgeRoot:  knowledgeRoot,
		newCodexClient: newRealCodexClient,
	}
}

// runTimeout picks Run's per-call budget — see ClaudeRunner.runTimeout,
// which this mirrors exactly.
func (r *CodexRunner) runTimeout(in RunInput) time.Duration {
	if in.EnableBashTool {
		return r.executeTimeout
	}
	return r.timeout
}

// CheckHealth implements AgentRunner. reposRoot and draftMCPPath must both
// be configured, and the `codex` CLI must be discoverable on PATH — the
// cheapest real signal available without spawning a subprocess per check
// (mirrors ClaudeRunner.CheckHealth). This deliberately does not attempt
// ensureRegistered: that spawns a real `codex app-server` subprocess, too
// expensive to run on every health poll.
func (r *CodexRunner) CheckHealth(_ context.Context) error {
	if r.reposRoot == "" {
		return errors.New("REPOS_ROOT is not configured")
	}
	if r.draftMCPPath == "" {
		return errors.New("codex draft MCP server binary is not configured")
	}
	if _, err := lookPath("codex"); err != nil {
		return fmt.Errorf("codex CLI not found on PATH: %w", err)
	}
	return nil
}

// ListModels implements AgentRunner. codex's model is configured via
// -c/--model per call (RunInput.Model/ExecuteInput.Model, honored directly
// by Run/Execute), not discovered through a listing call here — mirrors
// ClaudeRunner.ListModels' "no models" convention.
func (r *CodexRunner) ListModels(_ context.Context) ([]string, error) {
	return nil, nil
}

// CloseSession implements AgentRunner: disconnects and forgets the cached
// client+thread for sessionKey, if one exists. CodexRunner now caches a
// live thread per SessionKey the same way ClaudeRunner caches a
// claudecode.Client (see the type doc comment), so this is a real
// disconnect+forget — no longer a no-op.
func (r *CodexRunner) CloseSession(sessionKey string) {
	r.mu.Lock()
	client, ok := r.clients[sessionKey]
	if ok {
		delete(r.clients, sessionKey)
		delete(r.threads, sessionKey)
	}
	r.mu.Unlock()
	if ok {
		_ = client.Close(context.Background())
	}
}

// CloseAll disconnects every cached codex client this runner is holding,
// regardless of session key — mirrors ClaudeRunner.CloseAll exactly (see
// its doc comment for the full process-shutdown rationale); discovered by
// main.go's shutdown via the same `interface{ CloseAll() }` type
// assertion. Without it, a `codex app-server` subprocess left connected
// when the server exits is orphaned rather than terminated.
func (r *CodexRunner) CloseAll() {
	r.mu.Lock()
	clients := r.clients
	r.clients = make(map[string]codexClient)
	r.threads = make(map[string]codexThread)
	r.mu.Unlock()

	var wg sync.WaitGroup
	for _, client := range clients {
		wg.Add(1)
		go func(c codexClient) {
			defer wg.Done()
			_ = c.Close(context.Background())
		}(client)
	}
	wg.Wait()
}

// Run implements AgentRunner. Unlike an earlier version of this method,
// which connected a fresh client+thread on every single call (see the type
// doc comment's superseded framing), this now reuses a cached thread per
// SessionKey the same way ClaudeRunner.Run does — see threadFor.
func (r *CodexRunner) Run(ctx context.Context, in RunInput, onDelta func(chat.Delta) error) (RunOutput, error) {
	if err := r.ensureRegistered(ctx); err != nil {
		return RunOutput{}, fmt.Errorf("registering codex draft MCP server: %w", err)
	}
	if in.Workspace == "" {
		return RunOutput{}, errors.New("codex requires a project repository checked out under REPOS_ROOT")
	}

	key := in.SessionKey
	if !r.tryLock(key) {
		return RunOutput{}, ErrRunInProgress
	}
	defer r.unlock(key)

	runCtx, cancel := context.WithTimeout(ctx, r.runTimeout(in))
	defer cancel()

	thread, err := r.threadFor(runCtx, key, in)
	if err != nil {
		return RunOutput{}, err
	}

	names := toolNames(in.Tools)
	prompt := in.UserMessage
	if len(names) > 0 {
		prompt = prompt + "\n\n" + draftToolInstruction(names)
	}

	events, err := thread.RunStreamed(runCtx, prompt, nil)
	if err != nil {
		return RunOutput{}, fmt.Errorf("running codex turn for %s: %w", key, err)
	}

	var out RunOutput
	// Captured regardless of resumed-vs-fresh: a fresh (fallback) thread
	// still gets its own real thread id from codex, which is exactly what
	// the caller should persist as this SessionKey's next
	// RunInput.ResumeSessionID (see threadFor's doc comment).
	out.SessionID = thread.ID()
	var content assistantText
	for ev := range events {
		done, err := processCodexRunEvent(ev, names, &content, &out, onDelta, in.OnToolCall, in.OnToolResult)
		if err != nil {
			return out, err
		}
		if done {
			return out, nil
		}
	}
	return out, runCtx.Err()
}

// threadFor returns the cached thread for key, or creates one — mirroring
// ClaudeRunner.clientFor's cache/resume-first contract exactly, just for a
// codex client+thread pair instead of a single claudecode.Client. Callers
// must hold key's in-flight lock (via tryLock) so two calls for the same
// key never race to create one.
//
// When no thread is cached and in.ResumeSessionID is set, this attempts a
// real thread resume (client.ResumeThread) first — replaying the CLI's own
// transcript rather than an approximation rendered into
// developer_instructions (systemPromptWithHistory), and avoiding a fresh
// subprocess spawn every idle-turn reconnect. If that resume attempt fails
// with a "not found"-shaped error (isCodexSessionNotFoundError — the
// persisted id is stale, e.g. codex's own thread storage was pruned
// independently of this process), the id is abandoned and this falls
// through to a fresh, non-resumed thread with systemPromptWithHistory's
// replay, the same as if no id had ever been on record; any other error
// (auth, rate limit, an unreachable CLI) propagates directly, with no
// fallback retry. Either path's resulting client+thread are cached under
// key for reuse by later same-process turns (RunStreamed), instead of
// spawning a fresh subprocess every call the way this runner used to.
func (r *CodexRunner) threadFor(ctx context.Context, key string, in RunInput) (codexThread, error) {
	r.mu.Lock()
	thread, ok := r.threads[key]
	r.mu.Unlock()
	if ok {
		return thread, nil
	}

	if in.ResumeSessionID != "" {
		client, thread, err := r.connectAndResumeThread(ctx, in, in.ResumeSessionID)
		if err == nil {
			r.cacheClientAndThread(key, client, thread)
			return thread, nil
		}
		if !isCodexSessionNotFoundError(err) {
			return nil, fmt.Errorf("resuming codex thread for %s: %w", key, err)
		}
		// Stale/cleared thread id — fall through to a fresh, non-resumed
		// thread below, rather than failing this turn.
	}

	client, thread, err := r.connectAndStartThread(ctx, in)
	if err != nil {
		return nil, fmt.Errorf("starting codex thread for %s: %w", key, err)
	}
	r.cacheClientAndThread(key, client, thread)
	return thread, nil
}

// cacheClientAndThread records client/thread as key's live session.
func (r *CodexRunner) cacheClientAndThread(key string, client codexClient, thread codexThread) {
	r.mu.Lock()
	r.clients[key] = client
	r.threads[key] = thread
	r.mu.Unlock()
}

// connectAndStartThread connects a fresh codex client — developer_instructions
// set from in.SystemPrompt widened by systemPromptWithHistory's replay,
// since there is no session to resume — and starts a brand-new thread on
// it.
func (r *CodexRunner) connectAndStartThread(ctx context.Context, in RunInput) (codexClient, codexThread, error) {
	client, err := r.connectClient(ctx, in, systemPromptWithHistory(in.SystemPrompt, in.History))
	if err != nil {
		return nil, nil, err
	}
	thread, err := client.StartThread(ctx, &types.ThreadOptions{Cwd: in.Workspace})
	if err != nil {
		_ = client.Close(context.Background())
		return nil, nil, err
	}
	return client, thread, nil
}

// connectAndResumeThread connects a fresh codex client — developer_instructions
// set from in.SystemPrompt verbatim, since a resumed thread already has its
// own real history and replaying an approximation on top would just
// duplicate it — and resumes threadID on it. The returned error is left
// unwrapped so threadFor can pattern-match it via
// isCodexSessionNotFoundError before deciding whether to wrap and return it
// or fall through to a fresh thread.
func (r *CodexRunner) connectAndResumeThread(ctx context.Context, in RunInput, threadID string) (codexClient, codexThread, error) {
	client, err := r.connectClient(ctx, in, in.SystemPrompt)
	if err != nil {
		return nil, nil, err
	}
	thread, err := client.ResumeThread(ctx, threadID, &types.ResumeOptions{Cwd: in.Workspace})
	if err != nil {
		_ = client.Close(context.Background())
		return nil, nil, err
	}
	return client, thread, nil
}

// connectClient constructs and connects a fresh codex client with
// developer_instructions set to systemPrompt (already resolved by the
// caller — either a resumed thread's raw prompt, or a fresh thread's
// systemPromptWithHistory-widened one) and in.Model, if set. Closing the
// returned client on any later failure is the caller's responsibility
// (connectAndStartThread/connectAndResumeThread), since only they know
// whether a subsequent StartThread/ResumeThread call also failed.
func (r *CodexRunner) connectClient(ctx context.Context, in RunInput, systemPrompt string) (codexClient, error) {
	opts := types.NewCodexOptions().
		WithSandbox(types.SandboxReadOnly).
		WithApprovalPolicy(types.ApprovalNever).
		WithExtraArgs(developerInstructionsArgs(systemPrompt)...)
	if in.Model != "" {
		opts = opts.WithModel(in.Model)
	}

	client, err := r.newCodexClient(ctx, opts)
	if err != nil {
		return nil, fmt.Errorf("creating codex client: %w", err)
	}
	if err := client.Connect(ctx); err != nil {
		return nil, fmt.Errorf("connecting codex agent: %w", err)
	}
	return client, nil
}

// isCodexSessionNotFoundError reports whether err indicates a
// client.ResumeThread(threadID, ...) attempt failed because codex itself
// has no record of that thread — its own thread storage was pruned,
// cleared, or otherwise diverged from what this process had persisted — as
// opposed to a genuine failure (bad auth, rate limit, an unreachable CLI)
// that retrying without resume wouldn't fix and must propagate directly.
// Detected by message content since neither the SDK
// (github.com/hishamkaram/codex-agent-sdk-go) nor the underlying `codex`
// CLI exposes a stable sentinel/error code for this — the same
// content-matching posture isSessionNotFoundError (claude_runner.go) takes
// for ClaudeRunner's equivalent failure mode, and the same posture the SDK
// itself already takes internally for a "not found"-shaped thread/read or
// thread/list error (thread_history.go's threadHistoryRPCError). This
// pattern-matching is inherently fragile across CLI/SDK version bumps
// (accepted tradeoff — see docs/architecture/agentrunner-session-resume.md).
func isCodexSessionNotFoundError(err error) bool {
	msg := strings.ToLower(err.Error())
	for _, substr := range []string{
		"thread not found",
		"no thread found",
		"session not found",
		"unknown thread",
		"unknown session",
	} {
		if strings.Contains(msg, substr) {
			return true
		}
	}
	return false
}

// Execute implements AgentRunner. Like ClaudeRunner.Execute, never reuses
// a client across calls — an execution is one autonomous run to
// completion with no further turns.
func (r *CodexRunner) Execute(ctx context.Context, in ExecuteInput, onEvent func(ExecuteEvent) error) (ExecuteOutput, error) {
	if err := r.ensureRegistered(ctx); err != nil {
		return ExecuteOutput{}, fmt.Errorf("registering codex draft MCP server: %w", err)
	}

	key := in.SessionKey
	if !r.tryLock(key) {
		return ExecuteOutput{}, ErrRunInProgress
	}
	defer r.unlock(key)

	if in.Workspace == "" {
		return ExecuteOutput{}, errors.New("codex requires a resolved execution workspace")
	}

	runCtx, cancel := context.WithTimeout(ctx, r.executeTimeout)
	defer cancel()

	start := time.Now()

	// A git worktree's own admin metadata (HEAD, index, refs — what `git
	// commit` actually writes to) always lives under the *original* repo's
	// .git/worktrees/<id>/, never inside the worktree's own working
	// directory (verified live: codex's workspace-write sandbox denied
	// writing there with "Permission denied" on index.lock, since it's
	// outside in.Workspace). --add-dir is CLI-flag syntax for `codex exec`
	// only — `codex app-server` (what this SDK actually drives) rejects it
	// outright ("unexpected argument '--add-dir' found"), which silently
	// hung our Connect() rather than failing fast (verified live: the
	// subprocess exits immediately on the bad flag, but the SDK's
	// initialize call has no liveness check and just waits for a response
	// that will never arrive, until our own context timeout fires).
	// sandbox_workspace_write.writable_roots is the config-level
	// equivalent app-server actually accepts.
	extraArgs := developerInstructionsArgs(in.SystemPrompt)
	if gitDir, err := worktreeGitDir(runCtx, in.Workspace); err == nil {
		extraArgs = append(extraArgs, "-c", "sandbox_workspace_write.writable_roots=["+tomlQuote(gitDir)+"]")
	} else {
		return ExecuteOutput{}, fmt.Errorf("resolving worktree git-dir for execution %s: %w", key, err)
	}

	opts := types.NewCodexOptions().
		WithSandbox(types.SandboxWorkspaceWrite).
		WithApprovalPolicy(types.ApprovalNever).
		WithExtraArgs(extraArgs...)
	if in.Model != "" {
		opts = opts.WithModel(in.Model)
	}

	client, err := codex.NewClient(runCtx, opts)
	if err != nil {
		return ExecuteOutput{}, fmt.Errorf("creating codex client for execution %s: %w", key, err)
	}
	if err := client.Connect(runCtx); err != nil {
		return ExecuteOutput{}, fmt.Errorf("connecting codex agent for execution %s: %w", key, err)
	}
	defer func() { _ = client.Close(context.Background()) }()

	thread, err := client.StartThread(runCtx, &types.ThreadOptions{Cwd: in.Workspace})
	if err != nil {
		return ExecuteOutput{}, fmt.Errorf("starting codex execution thread %s: %w", key, err)
	}

	events, err := thread.RunStreamed(runCtx, codexKickoffMessage, nil)
	if err != nil {
		return ExecuteOutput{}, fmt.Errorf("starting codex execution: %w", err)
	}

	var out ExecuteOutput
	var content assistantText
	for ev := range events {
		done, err := processCodexExecuteEvent(ev, &content, &out, onEvent)
		if err != nil {
			out.DurationSeconds = time.Since(start).Seconds()
			return out, err
		}
		if done {
			out.DurationSeconds = time.Since(start).Seconds()
			return out, nil
		}
	}
	out.DurationSeconds = time.Since(start).Seconds()
	return out, runCtx.Err()
}

func (r *CodexRunner) tryLock(key string) bool {
	r.mu.Lock()
	defer r.mu.Unlock()
	if r.inFlight[key] {
		return false
	}
	r.inFlight[key] = true
	return true
}

func (r *CodexRunner) unlock(key string) {
	r.mu.Lock()
	defer r.mu.Unlock()
	delete(r.inFlight, key)
}

// ensureRegistered registers cmd/draftmcp as a persistent MCP server
// (config key mcp_servers.<codexDraftServerName>) and grants every known
// Draft tool a persisted approval_mode="approve", exactly once per
// process lifetime. Both edits are equivalent to what a human would get
// running `codex mcp add` plus interactively choosing "Always allow" once
// per tool — done here so no human step is required on a fresh machine.
// A failed attempt is not retried (sync.Once) — restart the process to
// retry after fixing the underlying problem (e.g. installing codex).
func (r *CodexRunner) ensureRegistered(ctx context.Context) error {
	r.registerOnce.Do(func() {
		r.registerErr = r.registerDraftServer(ctx)
	})
	return r.registerErr
}

func (r *CodexRunner) registerDraftServer(ctx context.Context) error {
	if r.draftMCPPath == "" {
		return errors.New("codex draft MCP server binary path is not configured")
	}

	client, err := codex.NewClient(ctx, types.NewCodexOptions().WithSandbox(types.SandboxReadOnly))
	if err != nil {
		return fmt.Errorf("creating codex client: %w", err)
	}
	if err := client.Connect(ctx); err != nil {
		return fmt.Errorf("connecting codex client: %w", err)
	}
	defer func() { _ = client.Close(context.Background()) }()

	serverConfig := map[string]any{"command": r.draftMCPPath}
	if r.knowledgeRoot != "" {
		serverConfig["args"] = []string{"--knowledge-root", r.knowledgeRoot}
	}
	edits := []types.ConfigEntry{
		{
			KeyPath: "mcp_servers." + codexDraftServerName,
			Value:   serverConfig,
		},
	}
	// Every tool this same draftmcp process might serve — both the Draft
	// proposal tools and (when knowledgeRoot is configured) the knowledge
	// query tools — gets the same persisted "always allow" approval, so a
	// codex thread never blocks on an interactive approval prompt for
	// either kind, the first time it calls any of them.
	approvedNames := make([]string, 0, len(drafttool.All())+len(knowledgetool.All()))
	for _, def := range drafttool.All() {
		approvedNames = append(approvedNames, def.Name)
	}
	if r.knowledgeRoot != "" {
		for _, def := range knowledgetool.All() {
			approvedNames = append(approvedNames, def.Name)
		}
	}
	for _, name := range approvedNames {
		edits = append(edits, types.ConfigEntry{
			KeyPath: fmt.Sprintf("mcp_servers.%s.tools.%s.approval_mode", codexDraftServerName, name),
			Value:   "approve",
		})
	}
	if _, err := client.WriteConfigBatch(ctx, edits); err != nil {
		return fmt.Errorf("writing draft MCP server config: %w", err)
	}
	return nil
}

// worktreeGitDir resolves the real git directory backing an isolated
// execution worktree — for a linked worktree this is the original repo's
// .git/worktrees/<id>/, not a path under workspace itself, since that's
// where git actually writes commit-time state (index.lock, HEAD, refs).
func worktreeGitDir(ctx context.Context, workspace string) (string, error) {
	out, err := gitutil.RunGit(ctx, workspace, "rev-parse", "--git-dir")
	if err != nil {
		return "", fmt.Errorf("resolving git-dir: %w", err)
	}
	gitDir := strings.TrimSpace(out)
	if !filepath.IsAbs(gitDir) {
		gitDir = filepath.Join(workspace, gitDir)
	}
	return gitDir, nil
}

// draftToolInstruction tells the model which MCP tool(s) it may call once a
// proposal is ready — codex has no equivalent of severity1's
// WithAllowedTools scoping the conversation to a single expected tool, so
// the model needs to be told explicitly which of the statically registered
// Draft tools (propose_context, propose_plan, ...) applies here. Usually
// one name; Review offers two (propose_review, propose_knowledge).
func draftToolInstruction(toolNames []string) string {
	quoted := make([]string, len(toolNames))
	for i, name := range toolNames {
		quoted[i] = fmt.Sprintf("%q", name)
	}
	return fmt.Sprintf(
		"When (and only when) you are ready to submit a proposal, call the appropriate MCP tool named %s "+
			"(registered under the %q server) with your proposal as its arguments, matching its declared schema exactly.",
		strings.Join(quoted, " or "), codexDraftServerName,
	)
}

// developerInstructionsArgs renders systemPrompt as codex's
// `-c developer_instructions=<toml string>` override — codex has no
// dedicated --system-prompt flag/option (verified: neither ThreadOptions
// nor RunOptions expose one), but developer_instructions is honored the
// same way, confirmed live. Returns nil for an empty prompt so callers
// don't pass a meaningless "-c developer_instructions=\"\"" arg.
func developerInstructionsArgs(systemPrompt string) []string {
	if systemPrompt == "" {
		return nil
	}
	return []string{"-c", "developer_instructions=" + tomlQuote(systemPrompt)}
}

// tomlQuote renders s as a TOML basic string (RFC-ish: quoted, with
// backslashes, quotes, and control characters escaped) suitable for a
// `-c key=value` override, where value is parsed as TOML.
func tomlQuote(s string) string {
	var b strings.Builder
	b.Grow(len(s) + 2)
	b.WriteByte('"')
	for _, r := range s {
		switch r {
		case '"':
			b.WriteString(`\"`)
		case '\\':
			b.WriteString(`\\`)
		case '\n':
			b.WriteString(`\n`)
		case '\r':
			b.WriteString(`\r`)
		case '\t':
			b.WriteString(`\t`)
		default:
			b.WriteRune(r)
		}
	}
	b.WriteByte('"')
	return b.String()
}

// processCodexRunEvent folds one types.ThreadEvent from a codex thread
// into content/out for Run, mirroring claude_runner.go's processMessage.
// toolNames are the Draft tool(s) this turn offered (empty for free-chat
// callers, in which case no MCPToolCall is ever treated as the Draft
// proposal); a session offering more than one (Review's propose_review and
// propose_knowledge) surfaces whichever one the model actually called.
// onToolCall/onToolResult (docs/adr/0018) surface every other completed
// item — a shell command, a file change, or a non-Draft MCP call — as
// intermediate Tool Activity (CONTEXT.md), mirroring
// processCodexExecuteEvent's existing handling for Execute. Unlike
// claude_runner.go's processMessage (which needs a toolUseID->name
// correlation across two separate messages), a codex ItemCompleted event
// already carries the whole call-and-result together, so onToolCall/
// onToolResult are simply invoked back to back, no correlation state
// needed.
// codexItemToolCallID returns item's own id — a real, stable per-item id on
// every ThreadItem variant this codebase reads (*types.CommandExecution,
// *types.FileChange, *types.MCPToolCall), used identically by
// processCodexRunEvent and processCodexExecuteEvent so both derive a tool
// call's correlation id the same way rather than two independently-written
// copies. Codex resolves a call and its result together, synchronously, for
// the same completed item (both call sites below), so correctness never
// actually depends on this id — but every RunInput.OnToolCall/OnToolResult
// caller shares one signature across all three runners, so it still needs
// one to pass.
func codexItemToolCallID(item types.ThreadItem) string {
	switch it := item.(type) {
	case *types.CommandExecution:
		return it.ID
	case *types.FileChange:
		return it.ID
	case *types.MCPToolCall:
		return it.ID
	default:
		return ""
	}
}

func processCodexRunEvent(ev types.ThreadEvent, toolNames []string, content *assistantText, out *RunOutput, onDelta func(chat.Delta) error, onToolCall func(id, name, argsJSON string), onToolResult func(id, name, result string, isError bool)) (done bool, err error) {
	switch e := ev.(type) {
	case *types.ItemStarted:
		// ItemStarted for a new AgentMessage item is codex's equivalent of
		// claude_runner.go's message_start StreamEvent — the live-stream
		// signal that a new round's remarks are about to begin, arriving
		// before that item's own AgentMessageDelta chunks (see
		// assistantText.startNewRound).
		if _, ok := e.Item.(*types.AgentMessage); ok {
			content.startNewRound()
		}
	case *types.ItemUpdated:
		if delta, ok := e.Delta.(*types.AgentMessageDelta); ok {
			if err := content.appendDelta(delta.TextChunk, onDelta); err != nil {
				return true, err
			}
		}
	case *types.ItemCompleted:
		// AgentMessage's text is not handled here — it already accumulated
		// live via ItemUpdated/AgentMessageDelta (assistantText.appendDelta
		// above), which is both the complete and authoritative source (see
		// assistantText's doc comment).
		switch item := e.Item.(type) {
		case *types.CommandExecution:
			id := codexItemToolCallID(item)
			if onToolCall != nil {
				onToolCall(id, "Bash", item.Command)
			}
			if onToolResult != nil {
				isErr := item.Status == "failed" || item.Status == "denied"
				onToolResult(id, "Bash", item.AggregatedOutput, isErr)
			}
		case *types.FileChange:
			id := codexItemToolCallID(item)
			if onToolCall != nil {
				input, marshalErr := json.Marshal(item.Changes)
				if marshalErr != nil {
					return true, fmt.Errorf("encoding file change input: %w", marshalErr)
				}
				onToolCall(id, "FileChange", string(input))
			}
			if onToolResult != nil {
				onToolResult(id, "FileChange", item.Status, item.Status == "failed")
			}
		case *types.MCPToolCall:
			isDraft := slices.Contains(toolNames, item.ToolName)
			if isDraft {
				// handleDraftToolCall (draftmcp) may have rejected this
				// proposal (isError: true, surfaced here as item.Status ==
				// "failed") — a rejected call is neither trusted as
				// out.ToolCall nor genuine Tool Activity; drop it so a
				// later, valid retry can still be captured (mirrors
				// claude_runner.go's processMessage).
				if out.ToolCall == nil && item.Status != "failed" {
					out.ToolCall = &chat.ToolCall{
						ID:   item.ID,
						Type: "function",
						Function: chat.ToolCallFunction{
							Name:      item.ToolName,
							Arguments: string(item.Input),
						},
					}
				}
				break
			}
			// Not the Draft — genuine intermediate tool activity (e.g. a
			// knowledge-query call).
			id := codexItemToolCallID(item)
			if onToolCall != nil {
				onToolCall(id, item.ToolName, string(item.Input))
			}
			if onToolResult != nil {
				isErr := item.Status == "failed"
				resultText := string(item.Result)
				if isErr {
					resultText = item.ErrorText()
				}
				onToolResult(id, item.ToolName, resultText, isErr)
			}
		}
	case *types.TurnFailed:
		out.Content = content.String()
		return true, fmt.Errorf("codex agent run failed: %s", e.Message)
	case *types.TurnCompleted:
		out.Content = content.String()
		if e.Status == "failed" {
			return true, errors.New("codex agent run failed")
		}
		return true, nil
	}
	return false, nil
}

// processCodexExecuteEvent folds one types.ThreadEvent into content/out
// for Execute, mirroring claude_runner.go's processExecuteMessage: every
// tool action (shell command, file change, MCP tool call) is surfaced as
// a tool_call/tool_result ExecuteEvent pair, not just prose.
func processCodexExecuteEvent(ev types.ThreadEvent, content *assistantText, out *ExecuteOutput, onEvent func(ExecuteEvent) error) (done bool, err error) {
	switch e := ev.(type) {
	case *types.ItemStarted:
		// See processCodexRunEvent's identical comment: this is codex's
		// message_start equivalent, arriving before the item's own
		// AgentMessageDelta chunks.
		if _, ok := e.Item.(*types.AgentMessage); ok {
			content.startNewRound()
		}
	case *types.ItemUpdated:
		if delta, ok := e.Delta.(*types.AgentMessageDelta); ok {
			if err := content.appendExecuteText(delta.TextChunk, onEvent); err != nil {
				return true, err
			}
		}
	case *types.ItemCompleted:
		// AgentMessage's text is not handled here — see
		// processCodexRunEvent's identical comment; it already accumulated
		// live via ItemUpdated/AgentMessageDelta above.
		if onEvent == nil {
			break
		}
		switch item := e.Item.(type) {
		case *types.CommandExecution:
			id := codexItemToolCallID(item)
			if err := onEvent(ExecuteEvent{Kind: "tool_call", ID: id, ToolName: "Bash", ToolInput: item.Command}); err != nil {
				return true, err
			}
			isErr := item.Status == "failed" || item.Status == "denied"
			if err := onEvent(ExecuteEvent{Kind: "tool_result", ID: id, ToolResult: item.AggregatedOutput, IsError: isErr}); err != nil {
				return true, err
			}
		case *types.FileChange:
			id := codexItemToolCallID(item)
			input, marshalErr := json.Marshal(item.Changes)
			if marshalErr != nil {
				return true, fmt.Errorf("encoding file change input: %w", marshalErr)
			}
			if err := onEvent(ExecuteEvent{Kind: "tool_call", ID: id, ToolName: "FileChange", ToolInput: string(input)}); err != nil {
				return true, err
			}
			isErr := item.Status == "failed"
			if err := onEvent(ExecuteEvent{Kind: "tool_result", ID: id, ToolResult: item.Status, IsError: isErr}); err != nil {
				return true, err
			}
		case *types.MCPToolCall:
			id := codexItemToolCallID(item)
			if err := onEvent(ExecuteEvent{Kind: "tool_call", ID: id, ToolName: item.ToolName, ToolInput: string(item.Input)}); err != nil {
				return true, err
			}
			isErr := item.Status == "failed"
			resultText := string(item.Result)
			if isErr {
				resultText = item.ErrorText()
			}
			if err := onEvent(ExecuteEvent{Kind: "tool_result", ID: id, ToolResult: resultText, IsError: isErr}); err != nil {
				return true, err
			}
		}
	case *types.TurnFailed:
		out.Content = content.String()
		return true, fmt.Errorf("codex execution failed: %s", e.Message)
	case *types.TurnCompleted:
		out.Content = content.String()
		out.TokensUsed = int(e.Usage.TotalTokens)
		out.NumTurns = 1
		if e.Status == "failed" {
			return true, errors.New("codex execution failed")
		}
		return true, nil
	}
	return false, nil
}

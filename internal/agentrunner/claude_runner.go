package agentrunner

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync"
	"time"

	claudecode "github.com/severity1/claude-agent-sdk-go"

	"github.com/timmersuk/llm-workbench/internal/chat"
	"github.com/timmersuk/llm-workbench/internal/drafttool"
	"github.com/timmersuk/llm-workbench/internal/knowledgetool"
)

// draftDefinitionsByName indexes drafttool.All() by name for
// draftToolHandlerFor's per-tool schema lookup.
var draftDefinitionsByName = func() map[string]drafttool.Definition {
	m := make(map[string]drafttool.Definition, len(drafttool.All()))
	for _, d := range drafttool.All() {
		m[d.Name] = d
	}
	return m
}()

// lookPath is exec.LookPath, indirected so CheckHealth is deterministically
// testable without depending on whether `claude` is actually on the test
// machine's PATH.
var lookPath = exec.LookPath

// readOnlyTools is the fixed tool set for Requirements/Planning stage agent
// runs — no Write/Edit/Bash, so the agent can read the reference repository
// but never modify it. This is the guardrail described in docs/architectural
// invariants.md's "the new trust boundary is scoped to can read files in the
// reference repo, not can modify" framing. Enforced via WithTools (the
// CLI's --tools, which actually restricts the built-in tool surface); also
// passed to WithAllowedTools so those same tools don't require an
// interactive permission prompt (see docs/adr/0022 — --allowed-tools/
// --disallowed-tools are a permission auto-approve/deny list, a separate,
// narrower mechanism than --tools, and on their own don't stop the CLI's
// full default built-in surface — Agent subagent spawning, Bash, Write,
// Edit, Skill, LSP, etc. — from being visible and callable). WebFetch/
// WebSearch are both read-only (fetch/search, never write) and included
// deliberately (see architecture/agentrunner-tool-surface-control knowledge
// doc); Skill/Workflow/Monitor stay excluded (no supervision over what they
// could trigger), as does LSP (documented broken per the user's global
// lsp-first.md rule) and PowerShell (redundant with Bash, which Review/
// Execute already grant). Task (subagent spawning) is deliberately NOT in
// this list: it is admitted separately (see subagentToolName /
// withSubagentSupport and docs/adr/0022's Update) and confined to a single
// custom, tool-limited AgentDefinition, so the calling stage's trust
// boundary is preserved via the subagent's own Tools rather than by
// widening this set.
var readOnlyTools = []string{"Read", "Grep", "Glob", "WebFetch", "WebSearch"}

// executionTools is the tool set for Execute — the Implementation stage is
// the one place this trust boundary deliberately widens beyond
// readOnlyTools, because Execute always runs against an isolated git
// worktree (see ResolveExecutionWorkspace), never the shared checkout Run
// uses, so Write/Edit/Bash here can't touch anything a human or another
// stage's read-only agent has open.
var executionTools = append(append([]string{}, readOnlyTools...), "Write", "Edit", "Bash")

// claudePermissionMode is the explicit permission mode set on every Run and
// Execute connection, replacing the CLI's unset default. Only "default" routes
// a visible-but-not-auto-approved tool through WithCanUseTool for a per-call
// decision in our code; acceptEdits/bypassPermissions skip the callback, plan
// is read-only. See docs/adr/0024.
const claudePermissionMode = claudecode.PermissionModeDefault

// allowToolUse permits a tool call, echoing the proposed input back as
// UpdatedInput. NOT claudecode.NewPermissionResultAllow(): that omits
// updatedInput and CLI 2.1.206 rejects the response with a ZodError. See docs/adr/0024.
func allowToolUse(input map[string]any) claudecode.PermissionResult {
	if input == nil {
		input = map[string]any{}
	}
	return claudecode.PermissionResultAllow{Behavior: "allow", UpdatedInput: input}
}

// guardedWriteTools are the tools Execute keeps off --allowed-tools so every
// call routes through executeWriteGuard for a worktree-boundary check.
var guardedWriteTools = map[string]bool{"Write": true, "Edit": true}

// executeWriteGuard is Execute's WithCanUseTool callback: it allows a
// Write/Edit only when its file_path stays inside the execution worktree,
// denying+logging any that would escape it. The CLI does not confine Write to
// cwd on its own (live-verified). Non-guarded tools pass through. See docs/adr/0024.
func executeWriteGuard(workspace string) claudecode.CanUseToolCallback {
	return func(_ context.Context, toolName string, input map[string]any, _ claudecode.ToolPermissionContext) (claudecode.PermissionResult, error) {
		if !guardedWriteTools[toolName] {
			return allowToolUse(input), nil
		}
		rawPath, _ := input["file_path"].(string)
		if rawPath == "" {
			return claudecode.NewPermissionResultDeny(fmt.Sprintf(
				"%s denied: no file_path supplied to check against the execution worktree", toolName)), nil
		}
		if !withinWorkspace(workspace, rawPath) {
			slog.Warn("execute write guard denied out-of-worktree write",
				"tool", toolName, "file_path", rawPath, "workspace", workspace)
			return claudecode.NewPermissionResultDeny(fmt.Sprintf(
				"%s denied: %s is outside this execution's worktree (%s); only files within the worktree may be modified",
				toolName, rawPath, workspace)), nil
		}
		return allowToolUse(input), nil
	}
}

// withinWorkspace reports whether rawPath (resolved against workspace when
// relative) stays inside workspace. Lexical only — a Write target may not
// exist yet, so it must not stat or create it.
func withinWorkspace(workspace, rawPath string) bool {
	root, err := filepath.Abs(workspace)
	if err != nil {
		return false
	}
	target := rawPath
	if !filepath.IsAbs(target) {
		target = filepath.Join(root, target)
	}
	target = filepath.Clean(target)
	rel, err := filepath.Rel(root, target)
	if err != nil {
		return false
	}
	return rel != ".." && !strings.HasPrefix(rel, ".."+string(filepath.Separator))
}

// permissionEscalationCallback is Run's WithCanUseTool callback: it forwards a
// visible-but-not-auto-approved tool (Bash on a human-paced turn) to the turn's
// human decision hook. The hook is looked up lazily by key, not captured at
// connect time, because a cached Run client outlives the turn that created it.
// No hook installed -> deny (a visible-but-unapproved tool must not auto-run).
func (r *ClaudeRunner) permissionEscalationCallback(key string) claudecode.CanUseToolCallback {
	return func(ctx context.Context, toolName string, input map[string]any, _ claudecode.ToolPermissionContext) (claudecode.PermissionResult, error) {
		requester := r.permissionRequesterFor(key)
		if requester == nil {
			return claudecode.NewPermissionResultDeny(fmt.Sprintf(
				"%s denied: no human is available to approve this tool for the current turn", toolName)), nil
		}
		args, err := json.Marshal(input)
		if err != nil {
			return nil, fmt.Errorf("encoding permission request arguments: %w", err)
		}
		allow, err := requester(ctx, toolName, string(args))
		if err != nil {
			return nil, err
		}
		if !allow {
			return claudecode.NewPermissionResultDeny(fmt.Sprintf("%s denied by the human reviewer", toolName)), nil
		}
		return allowToolUse(input), nil
	}
}

// subagentToolName is the CLI *argument-syntax* name for subagent spawning:
// the value passed to WithTools/WithAllowedTools and the Task(<agent>)
// scoped-deny entries of WithDisallowedTools. This is the name the `claude`
// CLI expects on its --tools/--allowed-tools/--disallowed-tools flags,
// confirmed live (preset denial for general-purpose worked against claude
// 2.1.206). ADR-0022 originally blocked subagent spawning outright by omitting
// this from WithTools; its Update reverses that in favor of first-class,
// scoped support — Task is re-admitted but bound to a single custom
// AgentDefinition (readOnlyAgentName / executionAgentName), never the CLI's
// own built-in presets.
const subagentToolName = "Task"

// subagentToolCallName is the name the *live CLI streams* for a subagent spawn
// in a ToolUseBlock/tool_call — observed as "Agent" (NOT "Task") against claude
// 2.1.206. The two genuinely differ: the flag syntax above is "Task", but the
// runtime tool call is "Agent". The vendored SDK hardcodes neither (it's
// CLI-runtime-determined — no "Task"/"Agent" constant exists in it), so runtime
// correlation must match the streamed name, not the flag name. Getting this
// wrong silently breaks the Task-call correlation that persists a subagent's
// real output (matchers never fire, the result surfaces orphaned) — the exact
// live defect this constant split fixes.
const subagentToolCallName = "Agent"

// isSubagentToolCall reports whether a streamed ToolUseBlock.Name (Run) or
// ExecuteEvent.ToolName (Execute) is a subagent spawn. Both the observed live
// name ("Agent") and the flag-syntax name ("Task") are matched so correlation
// survives the CLI naming the call either way across versions — neither is
// pinned by the SDK, so defending against both is cheap insurance against a
// silent re-break.
func isSubagentToolCall(name string) bool {
	return name == subagentToolCallName || name == subagentToolName
}

// readOnlyAgentName/executionAgentName are the WithAgents keys for the two
// stage-scoped custom subagents. A model can only ever invoke these
// (Task(<name>)) — the CLI's built-in presets are denied via
// builtinSubagentPresets — so a spawned subagent's tool access can never
// exceed the calling stage's own boundary (its AgentDefinition.Tools is that
// exact set; see withSubagentSupport).
const (
	readOnlyAgentName  = "workbench-readonly-agent"
	executionAgentName = "workbench-execution-agent"
)

// sessionIdleTimeout is how long a cached Run client's connection is kept
// alive with no turns against it before reapIdleClients evicts it. Its
// subprocess otherwise has no other reason to ever exit — see
// buildAndConnectClient's doc comment.
const sessionIdleTimeout = 30 * time.Minute

// builtinSubagentPresets are the `claude` CLI's own built-in Task agent
// presets. Each is denied via WithDisallowedTools' Task(<name>) scoped-deny
// syntax so a model can't route around the custom, tool-scoped agent by
// spawning e.g. a general-purpose subagent that would inherit the full
// default tool surface (the exact loophole ADR-0022's Update closes). This is
// a small, CLI-shipped set — unlike the open-ended built-in *tool* surface
// ADR-0022 argued must be gated by allow-list, the built-in *agents* are few
// and named, so a deny-list is the right shape here.
var builtinSubagentPresets = []string{"Explore", "Plan", "general-purpose"}

// disallowedSubagentPresets renders builtinSubagentPresets into the
// Task(<name>) entries WithDisallowedTools expects.
func disallowedSubagentPresets() []string {
	denied := make([]string, len(builtinSubagentPresets))
	for i, name := range builtinSubagentPresets {
		denied[i] = subagentToolName + "(" + name + ")"
	}
	return denied
}

// scopedSubagent builds the custom AgentDefinition a stage exposes: its Tools
// is exactly the calling stage's own built-in boundary (readOnlyTools, +Bash
// for Review, or executionTools for Execute), so the trust-boundary parity
// invariant (docs/architectural invariants.md, docs/adr/0022's Update) is met
// by construction — the subagent can never touch a tool the parent stage
// couldn't. Task itself is intentionally omitted from the subagent's Tools so
// a subagent can't recursively spawn further subagents.
func scopedSubagent(description, prompt string, tools []string) claudecode.AgentDefinition {
	return claudecode.AgentDefinition{
		Description: description,
		Prompt:      prompt,
		Tools:       append([]string{}, tools...),
	}
}

// withSubagentSupport returns the options that admit first-class, scoped
// subagent spawning for a call: the custom stage-scoped agent (WithAgent),
// the built-in-preset deny entries (WithDisallowedTools), and the
// SubagentStart/SubagentStop hooks that let Run()/Execute synchronously await
// a spawned subagent and capture its real transcript output. getTracker is
// evaluated lazily at hook-fire time (not connect time) so a client cached
// across Run turns still routes each turn's subagent events to that turn's
// own tracker. Note: "Task" must also be added to WithTools/WithAllowedTools
// at the call site — those lists are built there and this only carries the
// agent/deny/hook wiring.
func withSubagentSupport(agentName string, agent claudecode.AgentDefinition, getTracker func() *subagentTracker) []claudecode.Option {
	return []claudecode.Option{
		claudecode.WithAgent(agentName, agent),
		claudecode.WithDisallowedTools(disallowedSubagentPresets()...),
		claudecode.WithHook(claudecode.HookEventSubagentStart, "", subagentStartHook(getTracker)),
		claudecode.WithHook(claudecode.HookEventSubagentStop, "", subagentStopHook(getTracker)),
	}
}

// executionKickoffMessage is the fixed user turn Execute sends to start an
// autonomous run — all real instructions live in the system prompt
// (agentrunner.ExecuteInput.SystemPrompt, built by the caller), the same
// split stage conversations already use for their own kickoffUserMessage.
const executionKickoffMessage = "Begin executing the plan."

// mcpServerName is the in-process MCP server name Draft tools are
// registered under (mcp__<mcpServerName>__<tool name> in WithAllowedTools).
const mcpServerName = "draft"

// knowledgeServerName is a second, always-registered in-process MCP server
// (independent of mcpServerName/in.Tools) carrying the read-only knowledge
// query tools (docs/milestones/done/milestone9.md) — kept separate from "draft"
// because these are genuinely executed for real on every call and never end
// a turn, unlike the Draft tools' fire-and-forget ack (draftToolHandler);
// mixing the two into one server would make processMessage's Draft-call
// matching (which only ever needs to recognize in.Tools' names) have to
// start distinguishing real tool calls from Draft proposals within a single
// server's tool set instead of by server.
const knowledgeServerName = "knowledge"

// ClaudeRunner implements AgentRunner backed by
// github.com/severity1/claude-agent-sdk-go, which drives the `claude` CLI
// as a subprocess. One claudecode.Client is created and connected lazily
// per RunInput.SessionKey and kept alive until CloseSession is called for
// that key — cwd, system prompt, and allowed tools are all
// claudecode.Client-scoped (fixed at connect time, not per-query), so a
// client cannot be shared across keys with different workspaces/prompts.
type ClaudeRunner struct {
	mu               sync.Mutex
	clients          map[string]claudecode.Client
	clientSelections map[string]Selection
	// clientCancels holds each cached client's own connection-lifetime
	// cancel func, keyed the same as clients — see buildAndConnectClient's
	// doc comment for why a client's connection outlives the turn that
	// created it. Absent for a client seeded directly into clients
	// (tests only); evictClient tolerates that.
	clientCancels map[string]context.CancelFunc
	// clientLastUsed tracks when each cached client last served a Run
	// call, so reapIdleClients can evict connections nobody's touched in
	// a while instead of holding them (and their subprocess) forever.
	clientLastUsed map[string]time.Time
	inFlight       map[string]bool
	// subagentTrackers holds the current turn's subagent tracker per
	// SessionKey. A Run client is cached and reused across turns, so its
	// SubagentStart/SubagentStop hooks (registered once at connect time)
	// can't close over a per-turn tracker directly — they look the current
	// one up here instead, which Run swaps each turn under the key's
	// in-flight lock. Execute uses a fresh client per call and captures its
	// tracker directly, so it never touches this map.
	subagentTrackers map[string]*subagentTracker
	// permissionRequesters holds the current turn's human decision hook per
	// SessionKey, looked up lazily by permissionEscalationCallback (a cached Run
	// client's callback is registered once but must route to the live turn).
	permissionRequesters map[string]func(ctx context.Context, toolName, argsJSON string) (bool, error)
	timeout              time.Duration
	executeTimeout       time.Duration
	reposRoot            string
	knowledgeStore       knowledgetool.Store
	// newClient constructs a claudecode.Client from the given options —
	// indirected (defaulting to claudecode.NewClient) purely so tests can
	// substitute a fake client without spawning a real `claude` subprocess,
	// the same seam lookPath provides for CheckHealth.
	newClient func(opts ...claudecode.Option) claudecode.Client
}

// NewClaudeRunner returns a ClaudeRunner whose Run calls are each bounded by
// timeout (covering client connection, the query, and draining the response
// stream), and whose Execute calls are separately bounded by executeTimeout.
// The two are split because they bound very different things: Run is one
// turn of a human-paced, read-only conversation, while Execute is an
// unattended multi-step implementation run to completion — reusing Run's
// budget for Execute cut autonomous executions off mid-run well before they
// could finish (see the blank-page bug this split was introduced to fix).
// A Run call with EnableBashTool set (the Review stage's automated-checks
// turn, up to reviewMaxTurns tool calls running the project's real test
// suite) is the same kind of unattended multi-step run as Execute, just
// dispatched through Run instead — runTimeout gives it Execute's longer
// budget instead of Run's, rather than reusing the short human-paced one
// meant for Requirements/Planning's read-only turns (the same
// "reusing the wrong budget cuts an autonomous run off mid-way" failure
// this comment already describes for Run vs Execute, just recurring one
// level down).
// reposRoot is the configured REPOS_ROOT value, held so CheckHealth
// can report unavailable when it's unset. knowledgeStore, if non-nil, is
// exposed on every Run call via a second always-registered in-process MCP
// server (docs/milestones/done/milestone9.md) — nil just means those two
// tools are never registered (e.g. tests that don't care).
func NewClaudeRunner(timeout, executeTimeout time.Duration, reposRoot string, knowledgeStore knowledgetool.Store) *ClaudeRunner {
	return &ClaudeRunner{
		clients:              make(map[string]claudecode.Client),
		clientSelections:     make(map[string]Selection),
		clientCancels:        make(map[string]context.CancelFunc),
		clientLastUsed:       make(map[string]time.Time),
		inFlight:             make(map[string]bool),
		subagentTrackers:     make(map[string]*subagentTracker),
		permissionRequesters: make(map[string]func(ctx context.Context, toolName, argsJSON string) (bool, error)),
		timeout:              timeout,
		executeTimeout:       executeTimeout,
		reposRoot:            reposRoot,
		knowledgeStore:       knowledgeStore,
		newClient:            claudecode.NewClient,
	}
}

// runTimeout picks Run's per-call budget: executeTimeout for a
// EnableBashTool turn (Review's unattended, many-tool-call automated
// checks), timeout otherwise (Requirements/Planning's human-paced,
// read-only turns). See NewClaudeRunner's doc comment for why.
func (r *ClaudeRunner) runTimeout(in RunInput) time.Duration {
	if in.EnableBashTool {
		return r.executeTimeout
	}
	return r.timeout
}

// CheckHealth implements AgentRunner. reposRoot must be configured — without
// one, ResolveWorkspace can never succeed regardless of CLI presence — and
// the `claude` CLI must be discoverable on PATH (the cheapest real signal
// available; the SDK has no standalone ping, and a full Connect+Disconnect
// would spawn a real subprocess per check).
func (r *ClaudeRunner) CheckHealth(_ context.Context) error {
	if r.reposRoot == "" {
		return errors.New("REPOS_ROOT is not configured")
	}
	if _, err := lookPath("claude"); err != nil {
		return fmt.Errorf("claude CLI not found on PATH: %w", err)
	}
	return nil
}

// ListModels implements AgentRunner. The claude CLI has no per-request
// selectable model list through this integration (its model is configured
// via the CLI/its own settings, not per Run call) — it always reports no
// models, which callers should treat as "model selection isn't offered by
// this executor," not an error.
func (r *ClaudeRunner) ListModels(_ context.Context) ([]string, error) {
	return []string{"sonnet", "opus", "haiku"}, nil
}

func (r *ClaudeRunner) Capabilities(_ context.Context) (ExecutorCapabilities, error) {
	return ExecutorCapabilities{Name: "claude-code", Models: []string{"sonnet", "opus", "haiku"}, Efforts: []ReasoningEffort{EffortLow, EffortMedium, EffortHigh}, DefaultModel: "sonnet", DefaultEffort: EffortHigh}, nil
}

func claudeSelectionOptions(model string, effort ReasoningEffort) []claudecode.Option {
	return []claudecode.Option{claudecode.WithModel(model), claudecode.WithEffort(claudecode.EffortLevel(effort))}
}

// CloseSession implements AgentRunner: disconnects and forgets the cached
// client for sessionKey, if one exists.
func (r *ClaudeRunner) CloseSession(sessionKey string) {
	r.mu.Lock()
	client, ok := r.clients[sessionKey]
	cancel := r.clientCancels[sessionKey]
	if ok {
		delete(r.clients, sessionKey)
		delete(r.clientSelections, sessionKey)
		delete(r.clientCancels, sessionKey)
		delete(r.clientLastUsed, sessionKey)
	}
	r.mu.Unlock()
	if ok {
		_ = client.Disconnect()
	}
	if cancel != nil {
		cancel()
	}
}

// reapIdleClients evicts every cached client whose connection has sat idle
// past sessionIdleTimeout. There's no background ticker for this (every
// ClaudeRunner in the test suite would leak one) — it runs inline at the
// top of clientFor instead, so idle connections get cleaned up on the next
// unrelated request rather than accumulating forever.
func (r *ClaudeRunner) reapIdleClients() {
	r.mu.Lock()
	var expired []string
	now := time.Now()
	for key, lastUsed := range r.clientLastUsed {
		if now.Sub(lastUsed) > sessionIdleTimeout {
			expired = append(expired, key)
		}
	}
	r.mu.Unlock()
	for _, key := range expired {
		r.CloseSession(key)
	}
}

// CloseAll disconnects every cached `claude` CLI client this runner is
// holding, regardless of session key. Unlike CloseSession (one key, called
// from real application logic — Finalize, "New chat"), this exists purely
// for process shutdown (main.go's run(), via a `interface{ CloseAll() }`
// type assertion — see docs/engineering conventions.md's AGENT_TIMEOUT/
// SHUTDOWN_TIMEOUT entry): without it, a `claude` subprocess left connected
// when the server exits is orphaned rather than terminated. Snapshots and
// clears the client map under the lock, then disconnects each client
// concurrently outside the lock — mirrors CloseSession's ignore-error
// posture (Disconnect() failures don't block shutdown) and its lock/call
// split (never call into the SDK while holding mu).
func (r *ClaudeRunner) CloseAll() {
	r.mu.Lock()
	clients := r.clients
	cancels := r.clientCancels
	r.clients = make(map[string]claudecode.Client)
	r.clientSelections = make(map[string]Selection)
	r.clientCancels = make(map[string]context.CancelFunc)
	r.clientLastUsed = make(map[string]time.Time)
	r.mu.Unlock()

	var wg sync.WaitGroup
	for _, client := range clients {
		wg.Add(1)
		go func(c claudecode.Client) {
			defer wg.Done()
			_ = c.Disconnect()
		}(client)
	}
	wg.Wait()
	for _, cancel := range cancels {
		cancel()
	}
}

// Run implements AgentRunner. Unlike the engine-backed ChatClientRunner
// (whose intermediate tool activity flows through toolloop.Config's
// OnToolCall/OnToolResult), the claude CLI drives its own subprocess and
// message stream — processMessage forwards in.OnToolCall/OnToolResult from
// that stream itself (docs/adr/0018), correlating each ToolResultBlock back
// to its call via toolUseID since the CLI reports them in separate
// messages, unlike the engine's single call-then-result step.
func (r *ClaudeRunner) Run(ctx context.Context, in RunInput, onDelta func(chat.Delta) error) (RunOutput, error) {
	key := in.SessionKey
	if !r.tryLock(key) {
		return RunOutput{}, ErrRunInProgress
	}
	defer r.unlock(key)

	runCtx, cancel := context.WithTimeout(ctx, r.runTimeout(in))
	defer cancel()

	// Install this turn's subagent tracker before connecting/querying so the
	// (possibly cached) client's SubagentStart/SubagentStop hooks route to it.
	tracker := newSubagentTracker()
	r.setSubagentTracker(key, tracker)
	defer r.clearSubagentTracker(key)

	// Install this turn's human decision hook before connecting so a cached
	// client's escalation callback routes to it.
	if in.OnPermissionRequest != nil {
		r.setPermissionRequester(key, in.OnPermissionRequest)
		defer r.clearPermissionRequester(key)
	}

	client, err := r.clientFor(runCtx, key, in)
	if err != nil {
		return RunOutput{}, err
	}

	if err := client.Query(runCtx, in.UserMessage); err != nil {
		if !isStaleClaudeConnectionError(err) {
			return RunOutput{}, fmt.Errorf("querying claude code agent: %w", err)
		}
		// A long idle gap between conversation turns (e.g. the human took
		// a while to reply) can outlive the cached `claude` CLI
		// subprocess — its stdin pipe is already gone, so the write fails
		// regardless of what's sent, on every future turn, until the
		// stale client is discarded. Evict it and reconnect exactly once
		// rather than surfacing a raw pipe error for something the human
		// did nothing to cause.
		r.CloseSession(key)
		client, err = r.clientFor(runCtx, key, in)
		if err != nil {
			return RunOutput{}, err
		}
		if err := client.Query(runCtx, in.UserMessage); err != nil {
			return RunOutput{}, fmt.Errorf("querying claude code agent: %w", err)
		}
	}

	// Wrap the caller's tool-activity callbacks so a Task subagent's opaque
	// "launched" acknowledgment is suppressed and its call tracked; the real
	// output is surfaced from the awaited transcript below, via the original
	// in.OnToolResult (not the wrapper, which would suppress it again).
	hooks := &toolActivityHooks{
		onCall:   tracker.wrapOnCall(in.OnToolCall),
		onResult: tracker.wrapOnResult(in.OnToolResult),
		pending:  make(pendingToolCalls),
	}
	var out RunOutput
	var content assistantText
	var done bool
	for msg := range client.ReceiveMessages(runCtx) {
		d, err := processMessage(msg, toolNames(in.Tools), &content, &out, onDelta, hooks)
		if err != nil {
			return out, err
		}
		if d {
			done = true
			break
		}
	}
	// Synchronously await every subagent spawned this turn and persist its
	// real output as tool activity before the turn returns (preserving the
	// turn-based, no-hidden-state contract — see docs/adr/0022's Update).
	awaitAndEmitRunSubagents(runCtx, tracker, in.OnToolResult)
	if done {
		return out, nil
	}
	if err := runCtx.Err(); err != nil {
		return out, err
	}
	// The message channel closed with no ResultMessage and no timeout —
	// a resumed session left with unfinished business (e.g. an
	// async-dispatched subagent) can do this silently, forever, on every
	// future resume. Evict it so the next call starts fresh instead of
	// repeating the same silent failure.
	r.CloseSession(key)
	return RunOutput{}, fmt.Errorf("claude code agent run for %s ended with no result and no error", key)
}

// Execute implements AgentRunner. Unlike Run, it never reuses or caches a
// claudecode.Client across calls — Run's client cache exists to resume a
// multi-turn human conversation across separate HTTP requests, but an
// execution is one autonomous run to completion with no further turns, so
// a fresh client connected and disconnected within this call is simpler
// and can't accidentally leak a write-enabled session into some other
// SessionKey. Still guarded by the same tryLock/unlock(key) pair Run uses,
// so two overlapping Execute (or Execute+Run) calls for the same
// SessionKey still can't race.
func (r *ClaudeRunner) Execute(ctx context.Context, in ExecuteInput, onEvent func(ExecuteEvent) error) (ExecuteOutput, error) {
	key := in.SessionKey
	if !r.tryLock(key) {
		return ExecuteOutput{}, ErrRunInProgress
	}
	defer r.unlock(key)

	if in.Workspace == "" {
		return ExecuteOutput{}, errors.New("claude-code requires a resolved execution workspace")
	}

	runCtx, cancel := context.WithTimeout(ctx, r.executeTimeout)
	defer cancel()

	tracker := newSubagentTracker()

	// executeTools deliberately does not admit Task — see docs/adr/0022's
	// second Update: a background-dispatched subagent never reports back
	// over this transport regardless of connection lifetime (live-verified),
	// so the tool is not offered at all.
	executeTools := append([]string{}, executionTools...)
	// Write/Edit stay visible (WithTools) but off --allowed-tools so every call
	// routes through executeWriteGuard for a worktree-boundary check; everything
	// else (incl. Bash) stays auto-approved. See docs/adr/0024.
	executeAllowed := make([]string, 0, len(executeTools))
	for _, t := range executeTools {
		if !guardedWriteTools[t] {
			executeAllowed = append(executeAllowed, t)
		}
	}

	opts := []claudecode.Option{
		claudecode.WithCwd(in.Workspace),
		// WithAppendSystemPrompt, not WithSystemPrompt: the latter replaces
		// the CLI's entire default system prompt, which is what normally
		// discloses the agent's own cwd to itself. Losing that disclosure let
		// an execution's agent `cd` into the wrong repo when its workspace
		// guess (via `git rev-parse --show-toplevel`) landed on a valid but
		// wrong git root — see execution-worktree-cleanup/exec-001 postmortem.
		claudecode.WithAppendSystemPrompt(in.SystemPrompt),
		claudecode.WithPartialStreaming(),
		// WithTools is the actual tool-surface gate (--tools); WithAllowedTools
		// (--allowed-tools) only auto-approves these without a permission
		// prompt — see docs/adr/0022. Both must be set, or the CLI's full
		// default built-in surface (Skill, LSP, ...) stays visible/callable
		// alongside executionTools.
		claudecode.WithTools(executeTools...),
		claudecode.WithAllowedTools(executeAllowed...),
		// Explicit mode + worktree write guard, replacing the CLI's unset
		// default: bounds Execute's writes to in.Workspace. See docs/adr/0024.
		claudecode.WithPermissionMode(claudePermissionMode),
		claudecode.WithCanUseTool(executeWriteGuard(in.Workspace)),
	}
	opts = append(opts, claudeSelectionOptions(in.Model, in.ReasoningEffort)...)
	// See clientFor's identical comment: omitting WithMaxTurns entirely
	// (rather than passing 0) is what tells the underlying `claude` CLI not
	// to cap turns at all.
	if in.MaxTurns > 0 {
		opts = append(opts, claudecode.WithMaxTurns(in.MaxTurns))
	}
	client := r.newClient(opts...)
	if err := client.Connect(runCtx); err != nil {
		return ExecuteOutput{}, fmt.Errorf("connecting claude code agent for execution %s: %w", key, err)
	}
	defer func() { _ = client.Disconnect() }()

	if err := client.Query(runCtx, executionKickoffMessage); err != nil {
		return ExecuteOutput{}, fmt.Errorf("starting claude code execution: %w", err)
	}

	// Wrap onEvent so a Task subagent's opaque "launched" tool_result is
	// suppressed and its call tracked; the real output is emitted from the
	// awaited transcript below, via the original onEvent.
	wrappedOnEvent := tracker.wrapOnEvent(onEvent)

	var out ExecuteOutput
	var content assistantText
	pending := make(pendingToolCalls)
	var done bool
	for msg := range client.ReceiveMessages(runCtx) {
		d, err := processExecuteMessage(msg, &content, &out, wrappedOnEvent, pending)
		if err != nil {
			return out, err
		}
		if d {
			done = true
			break
		}
	}
	// Synchronously await every subagent spawned this run and emit its real
	// output as a tool_result before returning (see docs/adr/0022's Update).
	if err := awaitAndEmitExecuteSubagents(runCtx, tracker, onEvent); err != nil {
		return out, err
	}
	if done {
		return out, nil
	}
	return out, runCtx.Err()
}

func (r *ClaudeRunner) tryLock(key string) bool {
	r.mu.Lock()
	defer r.mu.Unlock()
	if r.inFlight[key] {
		return false
	}
	r.inFlight[key] = true
	return true
}

func (r *ClaudeRunner) unlock(key string) {
	r.mu.Lock()
	defer r.mu.Unlock()
	delete(r.inFlight, key)
}

// setSubagentTracker installs t as key's current-turn tracker, read lazily by
// a cached Run client's SubagentStart/SubagentStop hooks. Safe against races
// because the caller holds key's in-flight lock, so only one turn per key is
// ever live.
func (r *ClaudeRunner) setSubagentTracker(key string, t *subagentTracker) {
	r.mu.Lock()
	r.subagentTrackers[key] = t
	r.mu.Unlock()
}

// subagentTrackerFor returns key's current-turn tracker, or nil between turns
// (a hook firing with no live turn — shouldn't happen, but nil is handled).
func (r *ClaudeRunner) subagentTrackerFor(key string) *subagentTracker {
	r.mu.Lock()
	defer r.mu.Unlock()
	return r.subagentTrackers[key]
}

// clearSubagentTracker forgets key's tracker once its turn returns.
func (r *ClaudeRunner) clearSubagentTracker(key string) {
	r.mu.Lock()
	delete(r.subagentTrackers, key)
	r.mu.Unlock()
}

// setPermissionRequester installs key's current-turn human decision hook, read
// lazily by permissionEscalationCallback. Race-safe: the caller holds key's
// in-flight lock (as with setSubagentTracker).
func (r *ClaudeRunner) setPermissionRequester(key string, fn func(ctx context.Context, toolName, argsJSON string) (bool, error)) {
	r.mu.Lock()
	r.permissionRequesters[key] = fn
	r.mu.Unlock()
}

// permissionRequesterFor returns key's current-turn hook, or nil.
func (r *ClaudeRunner) permissionRequesterFor(key string) func(ctx context.Context, toolName, argsJSON string) (bool, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	return r.permissionRequesters[key]
}

// clearPermissionRequester forgets key's hook once its turn returns.
func (r *ClaudeRunner) clearPermissionRequester(key string) {
	r.mu.Lock()
	delete(r.permissionRequesters, key)
	r.mu.Unlock()
}

// clientFor returns the cached client for key, or creates and connects one.
// Callers must hold key's in-flight lock (via tryLock) so two calls for the
// same key never race to create a client.
//
// When no client is cached and in.ResumeSessionID is set, this attempts a
// real session resume (claudecode.WithResume) first — replaying the CLI's
// own transcript rather than an approximation rendered into the system
// prompt (systemPromptWithHistory), and avoiding that rendering's own risk
// of hitting the OS argv-length ceiling on a long-running conversation (see
// maxHistoryReplayBytes). If that resume attempt fails with a "session not
// found"-shaped error (isSessionNotFoundError — the persisted id is stale,
// e.g. the CLI's own session storage was pruned independently of this
// process), the id is abandoned and this falls through to a fresh,
// non-resumed connect with systemPromptWithHistory's replay, the same as if
// no id had ever been on record; any other connect error (auth, rate limit,
// a genuinely unreachable CLI) propagates directly, with no fallback retry.
// A fresh connect's own resulting session id is picked up from the turn's
// ResultMessage (processMessage) regardless of which path produced it, so
// the caller's next persisted RunOutput.SessionID is always correct either
// way.
func (r *ClaudeRunner) clientFor(ctx context.Context, key string, in RunInput) (claudecode.Client, error) {
	r.reapIdleClients()

	r.mu.Lock()
	client, ok := r.clients[key]
	priorSelection, selectionKnown := r.clientSelections[key]
	r.mu.Unlock()
	currentSelection := Selection{Model: in.Model, Effort: in.ReasoningEffort}
	if ok && selectionKnown && priorSelection != currentSelection {
		r.CloseSession(key)
		ok = false
	}
	if ok {
		r.touchClient(key)
		return client, nil
	}

	if in.Workspace == "" {
		return nil, errors.New("claude-code requires a project repository checked out under REPOS_ROOT")
	}

	if in.ResumeSessionID != "" {
		client, cancel, err := r.buildAndConnectClient(ctx, in, in.ResumeSessionID)
		if err == nil {
			return r.cacheClient(key, client, cancel, currentSelection), nil
		}
		cancel()
		if !isSessionNotFoundError(err) {
			return nil, fmt.Errorf("connecting claude code agent for %s: %w", key, err)
		}
		// Stale/cleared session id — fall through to a fresh, non-resumed
		// connect below (systemPromptWithHistory's replay), rather than
		// surfacing this turn as a failure.
	}

	client, cancel, err := r.buildAndConnectClient(ctx, in, "")
	if err != nil {
		cancel()
		return nil, fmt.Errorf("connecting claude code agent for %s: %w", key, err)
	}
	return r.cacheClient(key, client, cancel, currentSelection), nil
}

// cacheClient records client as key's live session and returns it —
// factored out of clientFor purely so both the resume and fallback connect
// paths share the exact same lock/store sequence.
func (r *ClaudeRunner) cacheClient(key string, client claudecode.Client, cancel context.CancelFunc, selection Selection) claudecode.Client {
	r.mu.Lock()
	r.clients[key] = client
	r.clientCancels[key] = cancel
	r.clientLastUsed[key] = time.Now()
	r.clientSelections[key] = selection
	r.mu.Unlock()
	return client
}

// touchClient records that key's cached client just served a turn, so
// reapIdleClients doesn't evict a connection that's actually in use.
func (r *ClaudeRunner) touchClient(key string) {
	r.mu.Lock()
	r.clientLastUsed[key] = time.Now()
	r.mu.Unlock()
}

// buildAndConnectClient constructs a claudecode.Client from in's workspace/
// tools/MCP servers and connects it, returning the raw (unwrapped) Connect
// error so clientFor can pattern-match it via isSessionNotFoundError before
// deciding whether to wrap and return it or fall through to a retry.
// resumeSessionID selects between claudecode.WithResume(resumeSessionID)
// (the real session/thread's own transcript, nothing rendered into the
// system prompt) and, when empty, a brand-new session whose system prompt
// is widened by systemPromptWithHistory's replay — never both, since a
// resumed session already has its own real history and replaying an
// approximation on top would just duplicate it.
//
// The returned cancel func governs the connection's own lifetime, not this
// call's ctx — see the struct-level clientCancels doc comment for why: a
// client cached for reuse across turns must not have its subprocess killed
// by the turn that happened to create it. cancel is always non-nil, even on
// error, so callers can unconditionally call it for cleanup.
func (r *ClaudeRunner) buildAndConnectClient(_ context.Context, in RunInput, resumeSessionID string) (claudecode.Client, context.CancelFunc, error) {
	connCtx, cancel := context.WithCancel(context.Background())
	systemPrompt := in.SystemPrompt
	var opts []claudecode.Option
	if resumeSessionID != "" {
		opts = append(opts, claudecode.WithResume(resumeSessionID))
	} else {
		systemPrompt = systemPromptWithHistory(in.SystemPrompt, in.History)
	}
	opts = append(opts,
		claudecode.WithCwd(in.Workspace),
		// See Execute's identical comment: append, don't replace, so the
		// agent keeps the default prompt's cwd disclosure.
		claudecode.WithAppendSystemPrompt(systemPrompt),
		claudecode.WithPartialStreaming(),
	)
	opts = append(opts, claudeSelectionOptions(in.Model, in.ReasoningEffort)...)
	// WithMaxTurns is only added when the caller set a positive value —
	// omitting it entirely (rather than passing 0) is how this SDK's
	// underlying `claude` CLI is told not to cap turns at all (see
	// internal/cli/discovery.go in the claude-agent-sdk-go module: the
	// --max-turns flag is only emitted when MaxTurns > 0).
	if in.MaxTurns > 0 {
		opts = append(opts, claudecode.WithMaxTurns(in.MaxTurns))
	}

	allowedTools := append([]string{}, readOnlyTools...)
	// The Review stage (Milestone 6) widens the read-only boundary with Bash
	// so the reviewing agent can run the project's tests over the executed
	// change — confined to the execution worktree (in.Workspace), never the
	// shared checkout. Requirements/Planning leave EnableBashTool false and
	// stay strictly read-only.
	if in.EnableBashTool {
		allowedTools = append(allowedTools, "Bash")
	}
	// builtinTools snapshots allowedTools before the MCP-qualified names
	// below get appended — WithTools (--tools) only understands the CLI's
	// built-in tool names (its own --help example is "Bash,Edit,Read"), not
	// mcp__<server>__<tool> names, unlike WithAllowedTools/--allowed-tools
	// which is MCP-qualified-name-aware (see mcpServerName's doc comment).
	builtinTools := append([]string{}, allowedTools...)
	// Human-escalation turn: make Bash visible (WithTools) without auto-approving
	// it (allowedTools unchanged), so a Bash call reaches the human via
	// permissionEscalationCallback instead of being invisible. Excludes Review's
	// unattended EnableBashTool turn and free-chat/rehydration callers. See docs/adr/0024.
	escalate := !in.EnableBashTool && in.OnPermissionRequest != nil
	if escalate {
		builtinTools = append(builtinTools, "Bash")
	}
	// in.Tools is optional — free-chat callers (no Draft concept) leave it
	// empty, in which case no MCP tool/server is registered at all rather
	// than trying to build one from zero tools.
	if len(in.Tools) > 0 {
		tools := make([]*claudecode.McpTool, 0, len(in.Tools))
		for _, t := range in.Tools {
			schema, err := decodeToolSchema(t.Function.Parameters)
			if err != nil {
				return nil, cancel, err
			}
			tools = append(tools, claudecode.NewTool(t.Function.Name, t.Function.Description, schema, draftToolHandlerFor(t.Function.Name)))
			allowedTools = append(allowedTools, mcpQualifiedName(t.Function.Name))
		}
		server := claudecode.CreateSDKMcpServer(mcpServerName, "1.0.0", tools...)
		opts = append(opts, claudecode.WithSdkMcpServer(mcpServerName, server))
	}
	// The knowledge query tools are always registered when a store is
	// configured — independent of in.Tools/stage, per
	// docs/milestones/done/milestone9.md's "available at every task stage".
	// Unlike the Draft server's tools (acked only; the real handling is
	// Finalize), these have real handlers: the `claude` CLI's own turn loop
	// calls them, gets a real result, and continues — no change needed to
	// processMessage, which only ever inspects the "draft" server's calls.
	if r.knowledgeStore != nil {
		handlers := map[string]claudecode.McpToolHandler{
			knowledgetool.List.Name: r.knowledgeListHandler,
			knowledgetool.Get.Name:  r.knowledgeGetHandler,
		}
		knowledgeTools := make([]*claudecode.McpTool, 0, len(knowledgetool.All()))
		for _, d := range knowledgetool.All() {
			schema, err := decodeToolSchema(d.Schema)
			if err != nil {
				return nil, cancel, err
			}
			knowledgeTools = append(knowledgeTools, claudecode.NewTool(d.Name, d.Description, schema, handlers[d.Name]))
			allowedTools = append(allowedTools, qualifiedName(knowledgeServerName, d.Name))
		}
		server := claudecode.CreateSDKMcpServer(knowledgeServerName, "1.0.0", knowledgeTools...)
		opts = append(opts, claudecode.WithSdkMcpServer(knowledgeServerName, server))
	}
	// Task (subagent spawning) is deliberately NOT admitted here — see
	// docs/adr/0022's second Update. A background-dispatched subagent's
	// completion never reaches a caller of this SDK/transport regardless of
	// how long the connection lives (live-verified), so offering the tool
	// only ever costs a wasted turn or worse (a session left silently
	// unresumable — see the 2026-08-06 incident in the ADR). WithTools is
	// the actual tool-surface gate; without Task in it, the model can't see
	// or call the tool at all.
	opts = append(opts, claudecode.WithTools(builtinTools...))
	opts = append(opts, claudecode.WithAllowedTools(allowedTools...))
	// Explicit permission mode on every Run connection (see claudePermissionMode);
	// required for the escalation callback to be consulted at all.
	opts = append(opts, claudecode.WithPermissionMode(claudePermissionMode))
	if escalate {
		opts = append(opts, claudecode.WithCanUseTool(r.permissionEscalationCallback(in.SessionKey)))
	}

	client := r.newClient(opts...)
	if err := client.Connect(connCtx); err != nil {
		return nil, cancel, err
	}
	return client, cancel, nil
}

// maxHistoryReplayBytes bounds how much prior-conversation transcript
// systemPromptWithHistory replays into the system prompt. The claude CLI
// receives the system prompt as a literal --system-prompt argument (the
// claude-agent-sdk-go transport has no file-based alternative), and on
// Windows the whole command line (every flag combined, including the
// review system prompt, task/project text, and the maxReviewPatchBytes
// diff addendum from stage_conversation.go) shares a single ~32,767
// character CreateProcess limit — exceeding it fails Connect with
// "The filename or extension is too long" rather than any error naming the
// actual cause. Replaying an uncapped history was the one uncapped
// contributor to that budget, so it's kept comfortably below the other
// pieces' combined size rather than tuned to the platform limit exactly.
const maxHistoryReplayBytes = 8 * 1024

// systemPromptWithHistory returns systemPrompt unchanged when history is
// empty (the common case: an already-live session, or a brand-new
// conversation with nothing to replay), or with a rendered transcript of
// history appended when a fresh client is being created to replace one
// this process lost (e.g. a server restart wiped ClaudeRunner's cached
// clients even though the conversation's messages survived in
// conversation-{stage}.yaml). The CLI has no "resume with this history"
// primitive — only a system prompt fixed at connect time and a fresh
// Query — so a rendered transcript block is the only way a new session's
// agent learns what was already discussed. The transcript is capped to
// maxHistoryReplayBytes, keeping the most recent messages: a long-running
// review conversation replaying in full is what blows the CLI's
// command-line limit (see maxHistoryReplayBytes), and the recent turns are
// what the agent actually needs to pick the conversation back up.
func systemPromptWithHistory(systemPrompt string, history []chat.Message) string {
	if len(history) == 0 {
		return systemPrompt
	}
	kept, truncated := recentHistoryWithinBudget(history, maxHistoryReplayBytes)
	var b strings.Builder
	b.WriteString(systemPrompt)
	b.WriteString("\n\n## Prior conversation (restored after restart)\n")
	if truncated {
		b.WriteString("(earliest turns omitted to keep this within the CLI's command-line limit)\n")
	}
	for _, m := range kept {
		fmt.Fprintf(&b, "%s: %s\n", m.Role, m.Content)
	}
	return b.String()
}

// recentHistoryWithinBudget returns the longest chronological suffix of
// history whose rendered size (role + content, roughly matching
// systemPromptWithHistory's "%s: %s\n" line format) fits within budget
// bytes, and whether any earlier messages were dropped to get there. Always
// keeps at least the single most recent message, even if it alone exceeds
// budget, so a truncated replay is never empty.
func recentHistoryWithinBudget(history []chat.Message, budget int) (kept []chat.Message, truncated bool) {
	total := 0
	start := len(history) - 1
	for i := len(history) - 1; i >= 0; i-- {
		size := len(history[i].Role) + len(history[i].Content) + 4
		if total+size > budget && i != len(history)-1 {
			break
		}
		total += size
		start = i
	}
	return history[start:], start > 0
}

// isStaleClaudeConnectionError reports whether err indicates the cached
// `claude` CLI subprocess is already gone — its stdin pipe closed, or the
// SDK's own transport already noticed the process died — as opposed to a
// genuine query failure that reconnecting wouldn't fix. This is the
// specific failure a long idle gap between conversation turns produces:
// ClaudeRunner keeps a claudecode.Client cached indefinitely per
// SessionKey (see the type doc), but nothing keeps the underlying `claude`
// subprocess itself alive forever, so writing to a turn after it has
// exited fails with a low-level pipe error. Detected by message content
// since neither the OS-level error (its concrete type differs by
// platform) nor the SDK (github.com/severity1/claude-agent-sdk-go)
// exposes a stable sentinel for this.
func isStaleClaudeConnectionError(err error) bool {
	msg := err.Error()
	for _, substr := range []string{
		"pipe is being closed", // Windows: write to a pipe the peer closed
		"broken pipe",          // Unix: write to a pipe the peer closed
		"closed pipe",          // io.ErrClosedPipe's message
		"stdin closed",         // the SDK's own "transport not connected or stdin closed"
		"file already closed",  // os.ErrClosed's message
	} {
		if strings.Contains(msg, substr) {
			return true
		}
	}
	return false
}

// isSessionNotFoundError reports whether err indicates a
// claudecode.WithResume(sessionID) attempt failed because the CLI itself
// has no record of that session — its on-disk session storage was pruned,
// cleared, or otherwise diverged from what this process had persisted — as
// opposed to a genuine failure (bad auth, rate limit, an unreachable CLI)
// that retrying without resume wouldn't fix and must propagate directly.
// Detected by message content since neither the SDK
// (github.com/severity1/claude-agent-sdk-go) nor the underlying `claude`
// CLI exposes a stable sentinel/error code for this — the same
// content-matching posture isStaleClaudeConnectionError already takes for
// its own, differently-shaped failure mode. This pattern-matching is
// inherently fragile across CLI/SDK version bumps (accepted tradeoff — see
// docs/architecture/agentrunner-session-resume.md); a real-CLI resume
// failure text this doesn't yet match will surface as an ordinary Run
// error, never a silent misbehavior.
func isSessionNotFoundError(err error) bool {
	msg := strings.ToLower(err.Error())
	for _, substr := range []string{
		"no conversation found", // observed claude CLI wording for an unknown --resume id
		"session not found",
		"session id not found",
		"unknown session",
		"no session found",
	} {
		if strings.Contains(msg, substr) {
			return true
		}
	}
	return false
}

// qualifiedName returns the fully-qualified tool name the `claude` CLI
// reports in a ToolUseBlock for an in-process MCP tool (mcp__<server>__
// <tool>) — WithAllowedTools and ToolUseBlock.Name both use this qualified
// form, never a bare tool name.
func qualifiedName(server, toolName string) string {
	return "mcp__" + server + "__" + toolName
}

// mcpQualifiedName is qualifiedName scoped to the "draft" server — the only
// server processMessage ever matches a Draft proposal against.
func mcpQualifiedName(toolName string) string {
	return qualifiedName(mcpServerName, toolName)
}

// knowledgeListHandler/knowledgeGetHandler are the real, functional MCP
// handlers for the knowledge query tools (unlike draftToolHandler's
// fire-and-forget ack) — bound methods rather than free functions since
// they need r.knowledgeStore. Errors surface to the model as an MCP tool
// error result rather than aborting the CLI's turn, the same "the model can
// see and recover from it" posture internal/toolloop's executeCall takes.
func (r *ClaudeRunner) knowledgeListHandler(_ context.Context, _ map[string]any) (*claudecode.McpToolResult, error) {
	text, err := knowledgetool.ExecuteList(r.knowledgeStore)
	if err != nil {
		return &claudecode.McpToolResult{Content: []claudecode.McpContent{{Type: "text", Text: err.Error()}}, IsError: true}, nil
	}
	return &claudecode.McpToolResult{Content: []claudecode.McpContent{{Type: "text", Text: text}}}, nil
}

func (r *ClaudeRunner) knowledgeGetHandler(_ context.Context, args map[string]any) (*claudecode.McpToolResult, error) {
	conceptID, _ := args["concept_id"].(string)
	text, err := knowledgetool.ExecuteGet(r.knowledgeStore, conceptID)
	if err != nil {
		return &claudecode.McpToolResult{Content: []claudecode.McpContent{{Type: "text", Text: err.Error()}}, IsError: true}, nil
	}
	return &claudecode.McpToolResult{Content: []claudecode.McpContent{{Type: "text", Text: text}}}, nil
}

func decodeToolSchema(parameters json.RawMessage) (map[string]any, error) {
	var schema map[string]any
	if err := json.Unmarshal(parameters, &schema); err != nil {
		return nil, fmt.Errorf("decoding tool schema: %w", err)
	}
	return schema, nil
}

// draftToolHandler acknowledges a Draft-proposing tool call so the CLI's
// turn can complete. The actual proposal is extracted from the
// ToolUseBlock in the message stream (processMessage) — matching how the
// local-LLM chat path never applies a Draft itself (Finalize does) — this
// handler has no side effects on task state.
func draftToolHandler(_ context.Context, _ map[string]any) (*claudecode.McpToolResult, error) {
	return &claudecode.McpToolResult{
		Content: []claudecode.McpContent{{Type: "text", Text: "Draft proposed to user for review."}},
	}, nil
}

// draftToolHandlerFor returns an McpToolHandler that validates a Draft
// proposal's arguments against name's own JSON Schema (internal/drafttool)
// before acking it. A call whose required fields don't match — e.g. one
// silently missing because the model's JSON generation glitched mid-value
// — is rejected with IsError: true instead: the claude CLI's own turn loop
// feeds that back to the model and lets it retry, rather than the caller
// (processMessage) capturing and persisting a malformed proposal. Falls
// back to the unconditional ack for an unrecognized name, which never
// happens in practice — every RunInput.Tools entry originates from a
// drafttool.Definition (stageTool, internal/api/stage_conversation.go) —
// so an unexpected mismatch fails open rather than blocking every
// proposal.
func draftToolHandlerFor(name string) claudecode.McpToolHandler {
	def, ok := draftDefinitionsByName[name]
	if !ok {
		return draftToolHandler
	}
	return func(ctx context.Context, args map[string]any) (*claudecode.McpToolResult, error) {
		if err := def.Validate(args); err != nil {
			return &claudecode.McpToolResult{
				IsError: true,
				Content: []claudecode.McpContent{{Type: "text", Text: fmt.Sprintf(
					"%s proposal rejected: %s. Retry %s with a corrected payload matching its schema.", name, err, name,
				)}},
			}, nil
		}
		return draftToolHandler(ctx, args)
	}
}

// toolNames returns the bare (non-MCP-qualified) names of tools.
func toolNames(tools []chat.Tool) []string {
	if len(tools) == 0 {
		return nil
	}
	names := make([]string, len(tools))
	for i, t := range tools {
		names[i] = t.Function.Name
	}
	return names
}

// pendingToolCalls correlates a ToolUseBlock to its later ToolResultBlock
// by ToolUseID — the claude CLI reports a call and its result in two
// separate messages (an AssistantMessage, then a later UserMessage) with
// no other shared identity, so this is the one place that correlation
// happens. Used identically by processMessage (Run) and
// processExecuteMessage (Execute), since both walk the exact same
// underlying message shape — a single shared implementation rather than
// two independently-written copies that can drift (as processExecuteMessage
// once did: it had no correlation at all).
type pendingToolCalls map[string]string // ToolUseID -> name

// track records a call awaiting its result.
func (p pendingToolCalls) track(id, name string) { p[id] = name }

// resolve returns and forgets the name tracked for id — id is the
// ToolResultBlock's own ToolUseID, unconditionally trusted as a match
// since the CLI never reuses one within a session.
func (p pendingToolCalls) resolve(id string) string {
	name := p[id]
	delete(p, id)
	return name
}

// toolActivityHooks carries the callbacks (and cross-message correlation
// state) needed to surface a claude CLI turn's intermediate tool calls and
// results live, as processMessage walks the message stream (docs/adr/0018).
// nil onCall/onResult (the RunInput fields left unset) mean the caller
// doesn't want this — processMessage checks each independently before
// invoking it.
type toolActivityHooks struct {
	onCall   func(id, name, argsJSON string)
	onResult func(id, name, result string, isError bool)
	pending  pendingToolCalls

	// pendingDraft correlates a Draft ToolUseBlock to its later
	// ToolResultBlock — draftToolHandlerFor's ack/reject — the same way
	// pending does for genuine intermediate tools, but keyed to the call's
	// full chat.ToolCall (not just its name), since a validated call
	// becomes out.ToolCall verbatim once resolveDraft confirms it wasn't
	// rejected. Kept separate from pending (rather than reusing it) so the
	// two kinds of ToolResultBlock — a Draft's ack/reject vs. a real tool's
	// result — are never ambiguous when both maps could otherwise hold the
	// same id.
	pendingDraft map[string]chat.ToolCall
}

// trackDraft records a candidate Draft proposal awaiting its MCP tool
// result before it's trusted enough to become out.ToolCall.
func (h *toolActivityHooks) trackDraft(id, name, argsJSON string) {
	if h.pendingDraft == nil {
		h.pendingDraft = make(map[string]chat.ToolCall)
	}
	h.pendingDraft[id] = chat.ToolCall{
		ID:       id,
		Type:     "function",
		Function: chat.ToolCallFunction{Name: name, Arguments: argsJSON},
	}
}

// resolveDraft returns and forgets the call tracked for id, if any —
// distinguishing "this ToolResultBlock is the ack/reject for a Draft
// proposal" from "this is a result for a genuine intermediate tool" so
// out.ToolCall and hooks.onResult never both fire for the same id.
func (h *toolActivityHooks) resolveDraft(id string) (chat.ToolCall, bool) {
	call, ok := h.pendingDraft[id]
	if ok {
		delete(h.pendingDraft, id)
	}
	return call, ok
}

// assistantText accumulates one turn's assistant text purely from the live
// incremental delta stream — the single source both the persisted Content
// string (out.Content) and the live relay to the caller (onDelta/onEvent)
// are built from, so there is exactly one implementation of "where do
// paragraph breaks go" instead of two that can silently drift apart. That
// drift is exactly how the run-on-paragraph bug this replaced happened: an
// earlier fix added a paragraph break only where the buffered, final
// AssistantMessage/AgentMessage text was assembled, never touching the
// separate live-delta relay, so a still-streaming turn kept rendering as
// one run-on paragraph even though the persisted version (built from the
// fixed path) was already correct. The buffered final message is no longer
// read for text at all — WithPartialStreaming (claudecode) and codex's
// streamed item deltas both guarantee incremental deltas reconstruct the
// final text exactly, so the buffered blocks are only still needed for
// data that's genuinely nowhere else (tool-use blocks, thinking blocks).
type assistantText struct {
	strings.Builder
	pendingBreak bool
}

// startNewRound marks that a paragraph break is due before this round's
// first text delta, once one arrives — called the moment the stream
// signals a new assistant-message/agent-message round has begun (Claude's
// message_start StreamEvent; Codex's ItemStarted for a new AgentMessage
// item), which always arrives before that round's own text deltas.
func (a *assistantText) startNewRound() {
	a.pendingBreak = true
}

// consumeBreak reports whether a paragraph break is due before the next
// text write, clearing pendingBreak either way — but only actually calls
// for a break if there's prior content to separate from, so the turn's
// very first round never gets a leading break.
func (a *assistantText) consumeBreak() bool {
	if !a.pendingBreak {
		return false
	}
	a.pendingBreak = false
	return a.Len() > 0
}

// appendDelta writes text (preceded by a paragraph break, if one is due)
// to the accumulated Content, relaying the identical bytes through onDelta
// if non-nil — onDelta may be nil (content still accumulates; nothing is
// relayed), matching Run's existing contract for callers that don't stream.
func (a *assistantText) appendDelta(text string, onDelta func(chat.Delta) error) error {
	if text == "" {
		return nil
	}
	if a.consumeBreak() {
		a.WriteString("\n\n")
		if onDelta != nil {
			if err := onDelta(chat.Delta{Content: "\n\n"}); err != nil {
				return err
			}
		}
	}
	a.WriteString(text)
	if onDelta != nil {
		return onDelta(chat.Delta{Content: text})
	}
	return nil
}

// appendExecuteText mirrors appendDelta for Execute's onEvent callback
// shape (ExecuteEvent{Kind: "text"}) instead of Run's onDelta/chat.Delta.
func (a *assistantText) appendExecuteText(text string, onEvent func(ExecuteEvent) error) error {
	if text == "" {
		return nil
	}
	if a.consumeBreak() {
		a.WriteString("\n\n")
		if onEvent != nil {
			if err := onEvent(ExecuteEvent{Kind: "text", Text: "\n\n"}); err != nil {
				return err
			}
		}
	}
	a.WriteString(text)
	if onEvent != nil {
		return onEvent(ExecuteEvent{Kind: "text", Text: text})
	}
	return nil
}

// isMessageStart reports whether ev is the message_start StreamEvent that
// opens a new assistant-message round — the live-stream signal for "a new
// round is beginning," available before any of that round's own text
// deltas arrive (see assistantText.startNewRound).
func isMessageStart(ev *claudecode.StreamEvent) bool {
	evType, _ := ev.Event["type"].(string)
	return evType == claudecode.StreamEventTypeMessageStart
}

// processMessage folds one message from a claudecode.Client's message
// stream into content/out, and reports whether the turn is complete
// (msg was a ResultMessage). toolNames are the bare names of every Draft
// tool this turn offered (RunInput.Tools) — a ToolUseBlock is only ever
// treated as the turn's Draft proposal if its MCP-qualified name matches
// one of them; a session with several offered tools (e.g. Review's
// propose_review and propose_knowledge) can have the model call more than
// one in the same turn, and every one of them is captured, in order, into
// out.ToolCalls (out.ToolCall stays the first, for single-draft callers).
// Every other ToolUseBlock/ToolResultBlock (Read/Grep/Glob, bash, the
// knowledge-query tools) is intermediate tool activity, forwarded through
// hooks rather than treated as a Draft. hooks may be nil, or have either
// callback nil, for callers that don't care. Split out from Run's loop so
// it's testable against hand-built claudecode.Message values without a
// live subprocess.
func processMessage(msg claudecode.Message, toolNames []string, content *assistantText, out *RunOutput, onDelta func(chat.Delta) error, hooks *toolActivityHooks) (done bool, err error) {
	switch m := msg.(type) {
	case *claudecode.StreamEvent:
		if isMessageStart(m) {
			content.startNewRound()
			break
		}
		if text, ok := streamDeltaText(m); ok {
			if err := content.appendDelta(text, onDelta); err != nil {
				return true, err
			}
		} else if reasoning, ok := streamReasoningDeltaText(m); ok && onDelta != nil {
			if err := onDelta(chat.Delta{ReasoningContent: reasoning}); err != nil {
				return true, err
			}
		}
	case *claudecode.AssistantMessage:
		for _, block := range m.Content {
			switch b := block.(type) {
			case *claudecode.ThinkingBlock:
				if onDelta != nil {
					if err := onDelta(chat.Delta{ReasoningContent: b.Thinking}); err != nil {
						return true, err
					}
				}
			case *claudecode.ToolUseBlock:
				name, isDraft := matchQualifiedToolName(b.Name, toolNames)
				if isDraft {
					// Not trusted yet: draftToolHandlerFor may still reject
					// this call (bad JSON shape) once its ToolResultBlock
					// arrives — see the UserMessage case below, which is
					// what actually sets out.ToolCall.
					if hooks != nil {
						args, err := json.Marshal(b.Input)
						if err != nil {
							return true, fmt.Errorf("encoding tool call arguments: %w", err)
						}
						hooks.trackDraft(b.ToolUseID, name, string(args))
					}
					continue
				}
				// Not the Draft — genuine intermediate tool activity
				// (Read/Grep/Glob, bash, or a knowledge-query call).
				if hooks != nil && hooks.onCall != nil {
					args, err := json.Marshal(b.Input)
					if err != nil {
						return true, fmt.Errorf("encoding tool call arguments: %w", err)
					}
					hooks.onCall(b.ToolUseID, b.Name, string(args))
				}
				if hooks != nil && hooks.pending != nil {
					hooks.pending.track(b.ToolUseID, b.Name)
				}
			}
		}
	case *claudecode.UserMessage:
		if hooks == nil {
			break
		}
		blocks, ok := m.Content.([]claudecode.ContentBlock)
		if !ok {
			break
		}
		for _, block := range blocks {
			b, ok := block.(*claudecode.ToolResultBlock)
			if !ok {
				continue
			}
			isError := b.IsError != nil && *b.IsError
			// A Draft's own ack/reject never also counts as Tool Activity
			// (CONTEXT.md) — resolve it here instead of falling through to
			// hooks.onResult below.
			if call, ok := hooks.resolveDraft(b.ToolUseID); ok {
				if !isError {
					out.ToolCalls = append(out.ToolCalls, call)
					if out.ToolCall == nil {
						first := call
						out.ToolCall = &first
					}
				}
				continue
			}
			if hooks.onResult == nil {
				continue
			}
			name := hooks.pending.resolve(b.ToolUseID)
			hooks.onResult(b.ToolUseID, name, toolResultText(b.Content), isError)
		}
	case *claudecode.ResultMessage:
		out.Content = content.String()
		// Captured regardless of resumed-vs-fresh: a fresh (fallback)
		// connect still gets its own real session id from the CLI, which is
		// exactly what the caller should persist as this SessionKey's next
		// ResumeSessionID (see clientFor's doc comment).
		out.SessionID = m.SessionID
		if m.IsError {
			// Errors is sometimes left empty by the CLI even when is_error is
			// true — the real diagnostic lands in Result or, failing that,
			// Subtype instead. Fall back through them so this never surfaces
			// a blank error.
			detail := strings.Join(m.Errors, "; ")
			if detail == "" && m.Result != nil {
				detail = *m.Result
			}
			if detail == "" {
				detail = m.Subtype
			}
			return true, fmt.Errorf("claude code agent run failed: %s", detail)
		}
		return true, nil
	}
	return false, nil
}

// matchQualifiedToolName reports whether qualifiedName (a ToolUseBlock's
// mcp__<server>__<tool> name) matches any of names' MCP-qualified form,
// returning that bare name if so.
func matchQualifiedToolName(qualifiedName string, names []string) (string, bool) {
	for _, name := range names {
		if qualifiedName == mcpQualifiedName(name) {
			return name, true
		}
	}
	return "", false
}

// processExecuteMessage folds one message from a claudecode.Client's
// message stream into content/out for an Execute call, emitting an
// ExecuteEvent for every meaningful thing that happens along the way, and
// reporting whether the run is complete (msg was a ResultMessage). Unlike
// processMessage (which only surfaces one Draft-matching ToolUseBlock and
// silently drops everything else), this surfaces every tool call and its
// result — an Execute run's real actions (files written, commands run) are
// the point, not incidental. pending correlates each ToolResultBlock back
// to its ToolUseID (see pendingToolCalls) — the same correlation
// processMessage needs and for the same reason, just consumed by ID alone
// here since ExecuteEvent's "tool_result" kind carries no tool name.
func processExecuteMessage(msg claudecode.Message, content *assistantText, out *ExecuteOutput, onEvent func(ExecuteEvent) error, pending pendingToolCalls) (done bool, err error) {
	switch m := msg.(type) {
	case *claudecode.StreamEvent:
		if isMessageStart(m) {
			content.startNewRound()
			break
		}
		if text, ok := streamDeltaText(m); ok {
			if err := content.appendExecuteText(text, onEvent); err != nil {
				return true, err
			}
		} else if reasoning, ok := streamReasoningDeltaText(m); ok && onEvent != nil {
			if err := onEvent(ExecuteEvent{Kind: "reasoning", Text: reasoning}); err != nil {
				return true, err
			}
		}
	case *claudecode.AssistantMessage:
		for _, block := range m.Content {
			switch b := block.(type) {
			case *claudecode.ThinkingBlock:
				if onEvent != nil {
					if err := onEvent(ExecuteEvent{Kind: "reasoning", Text: b.Thinking}); err != nil {
						return true, err
					}
				}
			case *claudecode.ToolUseBlock:
				if pending != nil {
					pending.track(b.ToolUseID, b.Name)
				}
				if onEvent == nil {
					continue
				}
				input, err := json.Marshal(b.Input)
				if err != nil {
					return true, fmt.Errorf("encoding tool call input: %w", err)
				}
				if err := onEvent(ExecuteEvent{Kind: "tool_call", ID: b.ToolUseID, ToolName: b.Name, ToolInput: string(input)}); err != nil {
					return true, err
				}
			}
		}
	case *claudecode.UserMessage:
		blocks, ok := m.Content.([]claudecode.ContentBlock)
		if !ok || onEvent == nil {
			break
		}
		for _, block := range blocks {
			b, ok := block.(*claudecode.ToolResultBlock)
			if !ok {
				continue
			}
			if pending != nil {
				pending.resolve(b.ToolUseID)
			}
			isError := b.IsError != nil && *b.IsError
			if err := onEvent(ExecuteEvent{Kind: "tool_result", ID: b.ToolUseID, ToolResult: toolResultText(b.Content), IsError: isError}); err != nil {
				return true, err
			}
		}
	case *claudecode.ResultMessage:
		out.Content = content.String()
		out.DurationSeconds = float64(m.DurationMs) / 1000
		out.NumTurns = m.NumTurns
		if m.TotalCostUSD != nil {
			out.CostEstimate = *m.TotalCostUSD
		}
		out.TokensUsed = sumUsageTokens(m.Usage)
		if m.IsError {
			return true, fmt.Errorf("claude code execution failed: %s", strings.Join(m.Errors, "; "))
		}
		return true, nil
	}
	return false, nil
}

// toolResultText renders a ToolResultBlock's Content (documented as
// "string or structured data") as a string for ExecuteEvent.ToolResult —
// the common case is already a string; anything else is JSON-encoded
// best-effort rather than dropped.
func toolResultText(content any) string {
	if s, ok := content.(string); ok {
		return s
	}
	data, err := json.Marshal(content)
	if err != nil {
		return fmt.Sprintf("%v", content)
	}
	return string(data)
}

// sumUsageTokens adds up every numeric token-count field in a
// ResultMessage's Usage map (e.g. "input_tokens", "output_tokens",
// "cache_read_input_tokens") — the SDK models Usage as an untyped
// map[string]any (it mirrors the CLI's own JSON verbatim rather than
// defining a fixed struct), so this sums whatever numeric fields are
// actually present rather than assuming specific key names. Returns 0 for
// a nil map rather than erroring, per ExecuteOutput's "leave unreported
// metrics at zero" convention.
func sumUsageTokens(usage *map[string]any) int {
	if usage == nil {
		return 0
	}
	total := 0
	for _, v := range *usage {
		switch n := v.(type) {
		case float64:
			total += int(n)
		case int:
			total += n
		}
	}
	return total
}

// streamDeltaText extracts incremental assistant text from a partial
// streaming event (emitted because clientFor sets WithPartialStreaming),
// or reports ok=false for any event that isn't a text content delta.
func streamDeltaText(ev *claudecode.StreamEvent) (text string, ok bool) {
	delta, ok := contentBlockDelta(ev)
	if !ok {
		return "", false
	}
	text, ok = delta["text"].(string)
	return text, ok
}

// streamReasoningDeltaText mirrors streamDeltaText for a thinking_delta
// event's "thinking" field — extended-thinking's incremental counterpart to
// a text_delta, emitted only when the session was connected with
// WithMaxThinkingTokens (not done anywhere in this codebase today; see
// docs/adr/0018 — this is forward-compatible dead code until that changes).
func streamReasoningDeltaText(ev *claudecode.StreamEvent) (text string, ok bool) {
	delta, ok := contentBlockDelta(ev)
	if !ok {
		return "", false
	}
	text, ok = delta["thinking"].(string)
	return text, ok
}

// contentBlockDelta returns ev's "delta" payload, or ok=false for any event
// that isn't a content_block_delta at all — the shared prefix
// streamDeltaText/streamReasoningDeltaText both need before inspecting
// which specific delta field (text vs thinking) is actually present.
func contentBlockDelta(ev *claudecode.StreamEvent) (map[string]any, bool) {
	if evType, _ := ev.Event["type"].(string); evType != claudecode.StreamEventTypeContentBlockDelta {
		return nil, false
	}
	delta, ok := ev.Event["delta"].(map[string]any)
	return delta, ok
}

// maxSubagentOutputBytes caps a subagent's captured transcript output so a
// verbose subagent can't bloat the persisted Conversation state (or, when a
// later turn replays history into the system prompt, the CLI's command-line
// limit — see maxHistoryReplayBytes).
const maxSubagentOutputBytes = 16 * 1024

// subagentIncompleteNote is the result surfaced for a Task call whose subagent
// neither reported completion (no SubagentStop) nor left a launch
// acknowledgment to fall back on before the turn's deadline — so the call
// never dangles without a result.
const subagentIncompleteNote = "(subagent did not report completion before the turn deadline)"

// subagentResult is one completed subagent's captured output, in stop order.
type subagentResult struct {
	agentID string
	output  string
	isError bool
}

// observedTask is one suppressed subagent-spawn call: its tool-use id and the
// real name the CLI streamed it under ("Agent"), kept so the awaited result
// emits paired with its call on name as well as id.
type observedTask struct {
	id   string
	name string
}

// subagentActivity is a completed subagent's output correlated to the Task
// tool-use id (and the tool name that call streamed under) that triggered it,
// ready to emit as tool activity. name carries the originating call's real
// streamed name ("Agent") so the emitted result pairs with its call on both id
// AND name downstream — never a hard-coded "Task" that never matches the live
// "Agent" call.
type subagentActivity struct {
	id      string
	name    string
	output  string
	isError bool
}

// subagentTracker correlates a turn's Task subagent spawns with their real
// output so Run()/Execute can synchronously await completion and persist that
// output as inspectable tool activity — never the CLI's opaque "launched"
// acknowledgment (docs/architectural invariants.md "No hidden state";
// docs/adr/0022's Update). It is shared between the message-processing
// goroutine (which observes Task ToolUseBlocks / their placeholder results)
// and the SDK's SubagentStart/SubagentStop hook goroutines (which count
// spawns and read transcripts), so every field is guarded by mu.
type subagentTracker struct {
	mu      sync.Mutex
	cond    *sync.Cond
	started int
	stopped int
	// tasks are the subagent-spawn tool calls observed this turn whose
	// placeholder was suppressed, in call order — each carrying the call's
	// tool-use id and its real streamed name ("Agent"). Correlated FIFO with
	// results at collect time (the CLI's SubagentStart/SubagentStop payloads
	// carry an AgentID, not the parent call's tool_use_id, so order is the
	// correlation we have).
	tasks []observedTask
	// placeholders holds each suppressed subagent call's launch acknowledgment
	// (keyed by tool-use id), used as a fallback output if that subagent never
	// reports completion.
	placeholders map[string]string
	// results are completed subagent outputs, in SubagentStop order.
	results []subagentResult
}

func newSubagentTracker() *subagentTracker {
	t := &subagentTracker{placeholders: make(map[string]string)}
	t.cond = sync.NewCond(&t.mu)
	return t
}

// observeTask records a subagent-spawn tool call (its id and real streamed
// name, e.g. "Agent") whose opaque placeholder result is being suppressed
// pending its subagent's real output.
func (t *subagentTracker) observeTask(id, name string) {
	t.mu.Lock()
	t.tasks = append(t.tasks, observedTask{id: id, name: name})
	t.mu.Unlock()
}

// setPlaceholder stashes a suppressed Task call's launch acknowledgment as a
// fallback (see subagentIncompleteNote for when no fallback exists either).
func (t *subagentTracker) setPlaceholder(id, text string) {
	t.mu.Lock()
	t.placeholders[id] = text
	t.mu.Unlock()
}

// resolveTask forgets a subagent-spawn call — used when its placeholder result
// was an error (the launch failed, so no subagent will run and the error is
// surfaced inline), so collect() neither waits for nor synthesizes a result
// for it.
func (t *subagentTracker) resolveTask(id string) {
	t.mu.Lock()
	defer t.mu.Unlock()
	for i, existing := range t.tasks {
		if existing.id == id {
			t.tasks = append(t.tasks[:i], t.tasks[i+1:]...)
			break
		}
	}
	delete(t.placeholders, id)
}

// start records a subagent spawn (SubagentStart). toolUseID is the generic
// hook correlation id the CLI may attach; it's accepted for forward
// compatibility but not currently relied upon (order is — see tasks).
func (t *subagentTracker) start(_ string, _ *string) {
	t.mu.Lock()
	t.started++
	t.mu.Unlock()
}

// stop records a subagent's completion (SubagentStop) and its captured output,
// waking any goroutine blocked in wait().
func (t *subagentTracker) stop(agentID, output string, isError bool) {
	t.mu.Lock()
	t.stopped++
	t.results = append(t.results, subagentResult{agentID: agentID, output: output, isError: isError})
	t.cond.Broadcast()
	t.mu.Unlock()
}

// wait blocks until every started subagent has stopped, or ctx is done
// (bounding the wait by the turn's existing timeout budget — a hung subagent
// can't block a turn indefinitely). In async fire-and-forget spawning a
// subagent's SubagentStart fires around its Task call, before the turn's
// ResultMessage, so by the time Run/Execute reaches here started already
// counts every spawn this turn.
func (t *subagentTracker) wait(ctx context.Context) {
	done := make(chan struct{})
	defer close(done)
	go func() {
		select {
		case <-ctx.Done():
			t.mu.Lock()
			t.cond.Broadcast()
			t.mu.Unlock()
		case <-done:
		}
	}()
	t.mu.Lock()
	for t.started != t.stopped && ctx.Err() == nil {
		t.cond.Wait()
	}
	t.mu.Unlock()
}

// collect returns each observed Task call's real output, correlated FIFO with
// completed subagent results. A Task call with no matching result (its
// subagent never stopped before the deadline) falls back to its launch
// acknowledgment, or subagentIncompleteNote — never nothing, so an emitted
// onCall is never left without a result. Any subagent result beyond the
// observed Task calls (more stops than tracked calls) still surfaces, keyed by
// its AgentID. Call only after wait().
func (t *subagentTracker) collect() []subagentActivity {
	t.mu.Lock()
	defer t.mu.Unlock()
	out := make([]subagentActivity, 0, len(t.tasks)+len(t.results))
	for i, task := range t.tasks {
		switch {
		case i < len(t.results):
			r := t.results[i]
			out = append(out, subagentActivity{id: task.id, name: task.name, output: r.output, isError: r.isError})
		case t.placeholders[task.id] != "":
			out = append(out, subagentActivity{id: task.id, name: task.name, output: t.placeholders[task.id], isError: false})
		default:
			out = append(out, subagentActivity{id: task.id, name: task.name, output: subagentIncompleteNote, isError: true})
		}
	}
	// Any subagent result beyond the observed calls (more stops than tracked
	// calls — e.g. the call was never correlated) still surfaces, keyed by its
	// AgentID and the canonical streamed name so downstream isn't left without
	// a result.
	for i := len(t.tasks); i < len(t.results); i++ {
		r := t.results[i]
		out = append(out, subagentActivity{id: r.agentID, name: subagentToolCallName, output: r.output, isError: r.isError})
	}
	return out
}

// wrapOnCall wraps a Run caller's OnToolCall so each Task spawn is tracked,
// while still forwarding the call (with its real arguments) to onCall if set.
func (t *subagentTracker) wrapOnCall(onCall func(id, name, argsJSON string)) func(id, name, argsJSON string) {
	return func(id, name, argsJSON string) {
		if isSubagentToolCall(name) {
			t.observeTask(id, name)
		}
		if onCall != nil {
			onCall(id, name, argsJSON)
		}
	}
}

// wrapOnResult wraps a Run caller's OnToolResult so a Task subagent's opaque
// "launched" acknowledgment is suppressed (its real output is emitted later
// from the awaited transcript); a failed launch (isError) is forwarded so the
// error is visible and the call doesn't dangle.
func (t *subagentTracker) wrapOnResult(onResult func(id, name, result string, isError bool)) func(id, name, result string, isError bool) {
	return func(id, name, result string, isError bool) {
		if isSubagentToolCall(name) {
			if isError {
				t.resolveTask(id)
			} else {
				t.setPlaceholder(id, result)
				return
			}
		}
		if onResult != nil {
			onResult(id, name, result, isError)
		}
	}
}

// wrapOnEvent wraps an Execute caller's onEvent to do the same Task-placeholder
// suppression as wrapOnResult, but over ExecuteEvents — whose tool_result
// carries no tool name, so it remembers which ids were Task tool_calls. The
// returned closure is called only from the single message-loop goroutine, so
// its taskIDs map needs no lock (the tracker's own methods handle the
// cross-goroutine state).
func (t *subagentTracker) wrapOnEvent(onEvent func(ExecuteEvent) error) func(ExecuteEvent) error {
	taskIDs := make(map[string]bool)
	return func(ev ExecuteEvent) error {
		switch ev.Kind {
		case "tool_call":
			if isSubagentToolCall(ev.ToolName) {
				taskIDs[ev.ID] = true
				t.observeTask(ev.ID, ev.ToolName)
			}
		case "tool_result":
			if taskIDs[ev.ID] {
				if ev.IsError {
					t.resolveTask(ev.ID)
				} else {
					t.setPlaceholder(ev.ID, ev.ToolResult)
					return nil
				}
			}
		}
		if onEvent != nil {
			return onEvent(ev)
		}
		return nil
	}
}

// subagentStartHook builds the SubagentStart HookCallback: it counts a spawn
// against the current turn's tracker (fetched lazily, since a cached Run
// client's hooks outlive any single turn).
func subagentStartHook(getTracker func() *subagentTracker) claudecode.HookCallback {
	return func(_ context.Context, input any, toolUseID *string, _ claudecode.HookContext) (claudecode.HookJSONOutput, error) {
		tracker := getTracker()
		if tracker == nil {
			return claudecode.HookJSONOutput{}, nil
		}
		var agentID string
		if in, ok := input.(*claudecode.SubagentStartHookInput); ok {
			agentID = in.AgentID
		}
		tracker.start(agentID, toolUseID)
		return claudecode.HookJSONOutput{}, nil
	}
}

// subagentStopHook builds the SubagentStop HookCallback: it reads the
// stopping subagent's transcript for its real output and records it, unblocking
// the turn's synchronous wait.
func subagentStopHook(getTracker func() *subagentTracker) claudecode.HookCallback {
	return func(_ context.Context, input any, _ *string, _ claudecode.HookContext) (claudecode.HookJSONOutput, error) {
		tracker := getTracker()
		if tracker == nil {
			return claudecode.HookJSONOutput{}, nil
		}
		var agentID, transcriptPath string
		if in, ok := input.(*claudecode.SubagentStopHookInput); ok {
			agentID = in.AgentID
			transcriptPath = in.AgentTranscriptPath
		}
		output, isError := readAgentTranscript(transcriptPath)
		tracker.stop(agentID, output, isError)
		return claudecode.HookJSONOutput{}, nil
	}
}

// awaitAndEmitRunSubagents blocks until this turn's subagents finish (bounded
// by ctx), then emits each one's real output through the caller's ORIGINAL
// OnToolResult (bypassing the suppression wrapper) so it lands in Conversation
// state as inspectable tool activity paired with the earlier Task call.
func awaitAndEmitRunSubagents(ctx context.Context, tracker *subagentTracker, onResult func(id, name, result string, isError bool)) {
	tracker.wait(ctx)
	if onResult == nil {
		return
	}
	for _, a := range tracker.collect() {
		onResult(a.id, a.name, a.output, a.isError)
	}
}

// awaitAndEmitExecuteSubagents is awaitAndEmitRunSubagents for Execute's
// onEvent shape, emitting each subagent's real output as a tool_result
// ExecuteEvent (which the execution log persists).
func awaitAndEmitExecuteSubagents(ctx context.Context, tracker *subagentTracker, onEvent func(ExecuteEvent) error) error {
	tracker.wait(ctx)
	if onEvent == nil {
		return nil
	}
	for _, a := range tracker.collect() {
		if err := onEvent(ExecuteEvent{Kind: "tool_result", ID: a.id, ToolResult: a.output, IsError: a.isError}); err != nil {
			return err
		}
	}
	return nil
}

// readAgentTranscript reads a stopped subagent's real output from its
// transcript file (SubagentStopHookInput.AgentTranscriptPath) — the whole
// point of awaiting SubagentStop rather than trusting the CLI's opaque
// "launched" acknowledgment. The transcript is JSONL (one CLI message per
// line); the subagent's output is its last assistant message's text. Any
// failure (missing path, unreadable file, unparseable content) is surfaced as
// an error result rather than dropped, so the tracker still signals completion
// and the turn never hangs.
func readAgentTranscript(path string) (output string, isError bool) {
	if path == "" {
		return "(subagent produced no transcript)", true
	}
	data, err := os.ReadFile(path)
	if err != nil {
		return fmt.Sprintf("(failed to read subagent transcript: %v)", err), true
	}
	entries := parseTranscriptEntries(data)
	text := lastAssistantTranscriptText(entries)
	if text == "" {
		// No assistant message parsed. Rather than dump the whole raw JSONL
		// transcript as "output" (unreadable, and the exact live defect this
		// replaces), fall back to the last readable text of ANY entry — still
		// never raw JSON — and only then to a clear note.
		text = lastTranscriptText(entries)
	}
	if text == "" {
		return "(subagent transcript contained no readable message)", true
	}
	if len(text) > maxSubagentOutputBytes {
		text = text[:maxSubagentOutputBytes] + "\n…(truncated)"
	}
	return text, false
}

// transcriptEntry is one parsed transcript record. It tolerates the shape
// variations the real `claude` CLI has been observed to emit: the documented
// top-level "type" discriminator with the message nested under "message"
// (whose own "role" is the more reliable signal on some versions), plus a
// flat role/content fallback in case a version carries them at the top level.
// A sidechain/subagent line also carries many extra fields (uuid, parentUuid,
// isSidechain, agentId, timestamp, ...) — all ignored here; only role and
// content matter.
type transcriptEntry struct {
	Type    string          `json:"type"`
	Role    string          `json:"role"`
	Content json.RawMessage `json:"content"`
	Message struct {
		Role    string          `json:"role"`
		Content json.RawMessage `json:"content"`
	} `json:"message"`
}

// isAssistant reports whether this entry is an assistant turn, checking every
// place the role/type has been observed rather than only the top-level "type"
// (which a live run found absent on the assistant entries — the root of the
// raw-JSON-dump defect).
func (e transcriptEntry) isAssistant() bool {
	return e.Type == "assistant" || e.Message.Role == "assistant" || e.Role == "assistant"
}

// text renders this entry's content (from message.content, or the flat
// top-level content fallback) to its concatenated text.
func (e transcriptEntry) text() string {
	if t := transcriptContentText(e.Message.Content); t != "" {
		return t
	}
	return transcriptContentText(e.Content)
}

// parseTranscriptEntries decodes a transcript into its records, tolerant of
// both JSONL (one object per line — the documented/common shape) and, if not a
// single line parses, a single JSON array/document (a defensive fallback for a
// pretty-printed or otherwise non-line-delimited transcript). Unparseable
// lines are skipped rather than aborting the whole read.
func parseTranscriptEntries(data []byte) []transcriptEntry {
	var entries []transcriptEntry
	for _, line := range strings.Split(string(data), "\n") {
		line = strings.TrimSpace(line)
		if line == "" {
			continue
		}
		var e transcriptEntry
		if json.Unmarshal([]byte(line), &e) == nil {
			entries = append(entries, e)
		}
	}
	if len(entries) > 0 {
		return entries
	}
	// No line parsed on its own — try the whole document as a single JSON
	// array (a pretty-printed transcript would land here).
	var arr []transcriptEntry
	if json.Unmarshal(data, &arr) == nil {
		return arr
	}
	return nil
}

// lastAssistantTranscriptText returns the text of the last assistant message in
// the transcript — the subagent's final answer to the calling agent.
func lastAssistantTranscriptText(entries []transcriptEntry) string {
	var last string
	for _, e := range entries {
		if !e.isAssistant() {
			continue
		}
		if text := e.text(); text != "" {
			last = text
		}
	}
	return last
}

// lastTranscriptText returns the last readable text of any entry (regardless of
// role) — a safety net so a transcript whose assistant entries we failed to
// recognize still surfaces readable text rather than raw JSON or nothing.
func lastTranscriptText(entries []transcriptEntry) string {
	var last string
	for _, e := range entries {
		if text := e.text(); text != "" {
			last = text
		}
	}
	return last
}

// transcriptContentText renders a transcript message's "content" (a plain
// string or an array of typed blocks — text, thinking, tool_use intermixed) to
// its concatenated text; only "text" blocks contribute.
func transcriptContentText(raw json.RawMessage) string {
	if len(raw) == 0 {
		return ""
	}
	var s string
	if json.Unmarshal(raw, &s) == nil {
		return strings.TrimSpace(s)
	}
	var blocks []struct {
		Type string `json:"type"`
		Text string `json:"text"`
	}
	if json.Unmarshal(raw, &blocks) != nil {
		return ""
	}
	var b strings.Builder
	for _, blk := range blocks {
		if blk.Type == "text" && blk.Text != "" {
			if b.Len() > 0 {
				b.WriteString("\n")
			}
			b.WriteString(blk.Text)
		}
	}
	return strings.TrimSpace(b.String())
}

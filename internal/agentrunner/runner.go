// Package agentrunner implements the Executor abstraction
// (docs/task schema v0.md, docs/provider abstraction.md's
// executor.type: claude-code | codex | local | human) for Requirements/
// Planning stage conversations: a tool-equipped coding agent that can read
// a task's reference repository directly, as an alternative to the
// text-only local-LLM chat path (internal/chat).
package agentrunner

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path"
	"path/filepath"
	"strings"
	"sync"

	"github.com/timmersuk/llm-workbench/internal/chat"
	"github.com/timmersuk/llm-workbench/internal/gitutil"
)

// ErrNoRepository is returned by ResolveWorkspace when the project has no
// configured repository to derive a workspace from.
var ErrNoRepository = errors.New("project has no configured repository")

// ErrInvalidRepository is returned by ResolveWorkspace when the project's
// repository identifier doesn't resolve to a valid, existing local
// checkout under reposRoot.
var ErrInvalidRepository = errors.New("invalid repository")

// ErrCloneFailed is returned by ResolveWorkspace when a project's
// repository has no existing local checkout and cloning one failed (e.g.
// network unavailable, or no ambient git credential for a private repo).
// Distinct from ErrInvalidRepository, which is about the repository
// identifier itself being malformed or unsafe, not a clone attempt's
// outcome.
var ErrCloneFailed = errors.New("cloning repository failed")

// ErrRunInProgress is returned by AgentRunner.Run when a run is already in
// flight for the same SessionKey — only one run per session is allowed at
// a time.
var ErrRunInProgress = errors.New("agent run already in progress for this session")

// RunInput carries everything an AgentRunner needs for one conversational
// turn. Workspace must already be resolved and validated (see
// ResolveWorkspace) — an AgentRunner never chooses its own cwd.
type RunInput struct {
	// SessionKey identifies which conversation this turn belongs to, for
	// AgentRunner implementations that hold per-session state (e.g.
	// ClaudeRunner's cached claudecode.Client, ChatClientRunner's
	// server-held history). Stage-conversation callers populate it as
	// taskID+":"+stage; free-chat callers populate it with a client-
	// generated id stable for the lifetime of one chat panel.
	SessionKey   string
	Workspace    string
	SystemPrompt string
	UserMessage  string
	// Model is the requested model identifier, honored only by AgentRunner
	// implementations backed by a model-selectable provider
	// (ChatClientRunner); implementations that don't support model
	// selection (ClaudeRunner) ignore it.
	Model string
	// ReasoningEffort is the provider-neutral effort requested for this
	// invocation. Adapters translate low/medium/high to their native wire
	// mechanism.
	ReasoningEffort ReasoningEffort
	// Tools are the Draft-proposing tool(s) the agent may call once a
	// proposal is ready (stageTool in internal/api/stage_conversation.go) —
	// the same tools the local-LLM chat path registers, so both paths
	// produce the same task.ConversationToolCall shape downstream. Usually
	// one; Review offers two at once (propose_review and propose_knowledge,
	// docs/milestones/done/milestone9.md) so a human can review either kind of
	// proposal without ending the conversation. An empty slice means no
	// tool is offered — free-chat callers leave this unset.
	Tools []chat.Tool
	// EnableBashTool widens the loop's toolset from read-only Read/Grep/Glob
	// to also include the confined bash tool, for the Review stage's
	// automated-checks phase (Milestone 6) — the reviewing agent runs the
	// project's test command over the executed change. Left false by
	// Requirements/Planning, whose agents stay strictly read-only. bash is
	// still confined to Workspace (the execution worktree for a review), so
	// this never reaches the project's shared checkout.
	EnableBashTool bool
	// MaxTurns bounds tool-call round-trips for this call, set explicitly by
	// the caller (e.g. internal/api/stage_conversation.go picks a value per
	// stage) rather than defaulted inside any AgentRunner implementation —
	// no runner may substitute a hardcoded constant of its own when this is
	// left at zero. Zero means no turn-count limit at all: the call is
	// still bounded by the runner's own configured timeout, just not by a
	// turn count.
	MaxTurns int
	// History is a stage conversation's persisted transcript so far
	// (internal/task.Conversation, mapped to chat.Message by the caller),
	// used to rehydrate an AgentRunner's in-memory session state after a
	// server restart wiped it. It is only ever consulted when the runner
	// has no live session for SessionKey already — replaying it into an
	// established session would duplicate history the runner already
	// holds — so callers always populate it from the durable record
	// regardless of whether a live session might still exist. Free-chat
	// callers, which have no durable transcript to replay from, leave this
	// nil.
	History []chat.Message
	// ResumeSessionID is the durable, per-executor session/thread id a prior
	// turn on this same SessionKey produced (RunOutput.SessionID), used to
	// rehydrate an AgentRunner's real underlying session/thread — rather
	// than an approximate systemPromptWithHistory replay — after a server
	// restart wiped its in-memory client cache. Mirrors History's own
	// contract exactly: only ever consulted when the runner has no live
	// in-memory session for SessionKey already (resuming into an
	// established session would be redundant, and could duplicate
	// history), so callers always populate it from the durable record
	// regardless of whether a live session might still exist. Empty means
	// "no id on record" — a brand-new conversation, an executor that never
	// recorded one, or one cleared after a prior "not found" resume
	// failure — in which case the runner falls back to
	// systemPromptWithHistory the same as if this were never set at all.
	// Free-chat callers, which have no durable record to resume from, leave
	// this "". ChatClientRunner ignores it unconditionally (no session/
	// thread concept of its own to resume).
	ResumeSessionID string
	// OnToolCall/OnToolResult, if non-nil, surface the loop's INTERMEDIATE
	// tool activity — each executed read_file/grep_search/glob/bash call and
	// its result — as it happens, so a stage-conversation stream can render
	// "ran go test ./... -> ok" live rather than only the model's prose. This
	// is distinct from RunOutput.ToolCall/RunInput.Tools: those carry the
	// single FINAL Draft-proposing stop call, which does not flow through
	// these hooks. Every AgentRunner implementation drives them today
	// (ChatClientRunner via toolloop.Config.OnToolCall/OnToolResult,
	// ClaudeRunner via processMessage's toolActivityHooks, CodexRunner via
	// processCodexSDKRunEvent) — both are nil-safe regardless, since free-chat
	// and rehydration callers leave them unset. id is a per-call correlation
	// key (the underlying provider's own call id, e.g. the claude CLI's
	// ToolUseID) — a call and its result share the same id, letting a caller
	// attach a result to the exact call it belongs to rather than assuming
	// results always arrive in the same order calls were declared. That
	// assumption is false for a runner that can declare several calls before
	// any of their results return (the claude CLI does, for parallel
	// read-only tool calls) — without the id, a result silently attaches to
	// whichever call happens to be positionally last.
	OnToolCall   func(id, name, argsJSON string)
	OnToolResult func(id, name, result string, isError bool)
}

// RunOutput is the result of one AgentRunner.Run call: the assistant's
// final text content, plus an optional decoded Draft tool call if the
// agent proposed one during the turn.
type RunOutput struct {
	Content  string
	ToolCall *chat.ToolCall
	// SessionID is this turn's resulting real session/thread id — the value
	// a caller should persist and later feed back as this SessionKey's next
	// RunInput.ResumeSessionID, so a subsequent turn with no live
	// in-memory session can resume the real underlying session/thread
	// instead of falling back to systemPromptWithHistory's replay. Empty
	// when the runner doesn't support a session/thread id at all
	// (ChatClientRunner, always), or when the turn errored before one was
	// produced. A caller persists this unconditionally after every turn —
	// including "", which correctly clears a stale id once a resume attempt
	// has already fallen back for this turn (see ClaudeRunner/CodexRunner's
	// "not found" handling).
	SessionID string
}

// ErrExecuteNotSupported is returned by an AgentRunner.Execute
// implementation that has no real execution capability (e.g.
// ChatClientRunner, until a tool loop exists for it — see
// data/projects/llm-workbench/tasks/chatclient-tool-loop/). Callers use
// this to distinguish "this executor can't run Execute at all" from a
// genuine execution failure.
var ErrExecuteNotSupported = errors.New("execution not supported by this executor")

// ExecuteInput carries everything an AgentRunner needs for one autonomous
// Implementation-stage execution attempt — distinct from RunInput, which is
// shaped for one turn of a human-paced, read-only conversation. Unlike
// Run, there is no History to rehydrate and no Draft Tool to register: an
// execution runs to completion unattended and is never resumed after a
// restart (see docs/milestones/milestone5.md's resolved decisions).
type ExecuteInput struct {
	// SessionKey guards against two overlapping executions of the same
	// task via the same tryLock/inFlight mechanism Run already uses.
	// Stage-conversation callers use taskID+":"+stage; execution callers
	// use taskID+":execute", which never collides with those.
	SessionKey string
	// Workspace is the isolated git worktree resolved by
	// ResolveExecutionWorkspace — never the shared checkout ResolveWorkspace
	// returns for Run, since Execute writes to disk and commits.
	Workspace    string
	SystemPrompt string
	// Model is honored only by AgentRunner implementations backed by a
	// model-selectable provider, same convention as RunInput.Model.
	Model           string
	ReasoningEffort ReasoningEffort
	// MaxTurns bounds tool-call round-trips for this execution, same
	// contract as RunInput.MaxTurns: set explicitly by the caller, never
	// defaulted inside an AgentRunner implementation. Zero means no
	// turn-count limit (bounded only by the runner's own configured
	// executeTimeout).
	MaxTurns int
}

// ExecuteEvent is one incremental unit of progress from an AgentRunner.Execute
// call, streamed via onEvent as the run proceeds. Unlike Run's onDelta
// (text only, one optional ToolCall returned at the end), Execute needs to
// surface every tool call and its result as they happen — a write-enabled
// agent's real actions (files written, commands run), not just its prose.
type ExecuteEvent struct {
	Kind string // "text" | "reasoning" | "tool_call" | "tool_result"

	// ID is set when Kind == "tool_call" or "tool_result" — a per-call
	// correlation key (see RunInput.OnToolCall's doc comment for why: a
	// call and its result can arrive out of declaration order). A "tool_call"
	// and its later "tool_result" share the same ID.
	ID string

	// Text is set when Kind == "text" or "reasoning". "reasoning" is only
	// ever emitted by ClaudeRunner, and only once extended thinking is
	// actually enabled for a session (not done anywhere in this codebase
	// today — see docs/adr/0018); no frontend currently renders it, the
	// same "typed but not yet rendered" posture ChatStreamEvent.ToolActivity
	// had before this ADR.
	Text string

	// ToolName/ToolInput are set when Kind == "tool_call". ToolInput is the
	// tool's raw JSON input.
	ToolName  string
	ToolInput string

	// ToolResult/IsError are set when Kind == "tool_result".
	ToolResult string
	IsError    bool
}

// ExecuteOutput is the result of one AgentRunner.Execute call: the
// assistant's final text content plus whatever run metrics the underlying
// executor actually reports. Fields an executor can't report (e.g. the
// claude CLI offering no token/cost figures) are left at zero rather than
// fabricated — see docs/task schema v0.md's execution.yaml.metrics.
type ExecuteOutput struct {
	Content         string
	DurationSeconds float64
	TokensUsed      int
	CostEstimate    float64
	NumTurns        int
}

// AgentRunner runs one turn of an agentic conversation against a
// tool-equipped coding agent or chat provider, isolated per
// RunInput.SessionKey, streaming assistant deltas via onDelta as they
// arrive. Satisfied by *ClaudeRunner and *ChatClientRunner; a
// codex_runner.go implementation is expected to follow the same interface.
type AgentRunner interface {
	Run(ctx context.Context, in RunInput, onDelta func(chat.Delta) error) (RunOutput, error)

	// Execute runs one autonomous Implementation-stage execution attempt to
	// completion, streaming tool activity via onEvent as it happens.
	// Implementations with no real execution capability (see
	// ErrExecuteNotSupported) return that error immediately rather than
	// silently no-oping.
	Execute(ctx context.Context, in ExecuteInput, onEvent func(ExecuteEvent) error) (ExecuteOutput, error)

	// CheckHealth reports whether this runner can actually be used right
	// now — a live probe, not a static configuration check (mirrors
	// chat.ChatClient.CheckHealth). Callers use this to decide whether to
	// offer the runner as a choice at all (internal/api/agent_executors.go).
	CheckHealth(ctx context.Context) error

	// ListModels returns the models this runner lets a caller select via
	// RunInput.Model, or (nil, nil) if this runner doesn't support
	// per-request model selection at all (not an error).
	ListModels(ctx context.Context) ([]string, error)

	// Capabilities is stable selection metadata, independent of transient
	// health. It is used both to render choices and validate persisted or
	// submitted selections.
	Capabilities(ctx context.Context) (ExecutorCapabilities, error)

	// CloseSession discards any state this runner holds for sessionKey
	// (cached subprocess connections, in-memory history, ...). Safe to
	// call for a key that never had any (no-op).
	CloseSession(sessionKey string)
}

// cloneRepository is indirected purely so tests can substitute a fake
// clone (e.g. creating dest locally) without a real network call or git
// subprocess — the same seam ClaudeRunner's lookPath/newClient provide
// (claude_runner.go). Production always uses gitutil.Clone.
var cloneRepository = gitutil.Clone

// cloneLocks serializes concurrent ResolveWorkspace calls that would
// otherwise race to clone the same workspace path — e.g. two stage
// conversations for the same brand-new project's first messages arriving
// close together. Keyed by the absolute workspace path.
var cloneLocks sync.Map // map[string]*sync.Mutex

// cloneIfAbsent clones repository (e.g. "github.com/timmersuk/llm-workbench")
// into workspace over HTTPS, guarded by a per-path lock so two concurrent
// callers resolving the same missing workspace don't race to clone it
// twice. Re-checks existence after acquiring the lock in case a concurrent
// caller already finished the clone while this one waited. No credential
// handling of any kind — cloning relies entirely on whatever ambient git
// credential setup (credential helper, cached PAT) the operator's machine
// already has, the same trust model gitutil.RunGit operates at everywhere
// else; a private repo with no working ambient credential simply fails to
// clone, visibly.
func cloneIfAbsent(ctx context.Context, root, workspace, repository string) error {
	lockVal, _ := cloneLocks.LoadOrStore(workspace, &sync.Mutex{})
	mu := lockVal.(*sync.Mutex)
	mu.Lock()
	defer mu.Unlock()

	if _, err := os.Stat(workspace); err == nil {
		return nil // a concurrent caller already cloned it while we waited
	}

	if err := os.MkdirAll(root, 0o755); err != nil {
		return fmt.Errorf("creating repos root %s: %w", root, err)
	}

	url := "https://" + repository
	if err := cloneRepository(ctx, url, workspace); err != nil {
		return fmt.Errorf("%w: %s: %v", ErrCloneFailed, repository, err)
	}
	return nil
}

// workspacePath derives the local filesystem path a project's first
// configured repository identifier resolves to under reposRoot (see
// ResolveWorkspace's doc comment for the naming convention and traversal
// protections), without touching the filesystem at all — no stat, no
// clone. GetWorkspaceStatus (workspace_status.go) uses this instead of
// ResolveWorkspace so a status check is never itself the trigger for a
// first clone: a checkout that doesn't exist yet just makes whatever git
// command runs against the returned path fail, which — per
// gitutil.BehindOrigin/DirtyWorkingTree's own "unknown as data" contract —
// resolves to "unknown" the same as any other unreadable git state, with
// no special-casing needed here.
func workspacePath(reposRoot string, repositories []string) (string, error) {
	if len(repositories) == 0 {
		return "", ErrNoRepository
	}

	// Reject any ".."/"." path component in the raw identifier before
	// path.Base cleans it away below. Previously a traversal attempt like
	// "github.com/x/../escaped" harmlessly fell through to a "workspace
	// doesn't exist" error once cleaned (path.Base already resolves ".."
	// segments, so the cleaned form never actually escapes reposRoot) —
	// but now that a missing workspace triggers a real clone attempt (a
	// subprocess and a network call) rather than just erroring, a
	// malformed identifier needs to be caught here, before it can reach
	// that clone, not after.
	for _, seg := range strings.Split(filepath.ToSlash(repositories[0]), "/") {
		if seg == "." || seg == ".." {
			return "", fmt.Errorf("%w: %q", ErrInvalidRepository, repositories[0])
		}
	}

	segment := path.Base(repositories[0])
	if segment == "" || segment == "." || segment == "/" || strings.ContainsAny(segment, `/\`) || strings.Contains(segment, "..") {
		return "", fmt.Errorf("%w: %q", ErrInvalidRepository, repositories[0])
	}

	root, err := filepath.Abs(reposRoot)
	if err != nil {
		return "", fmt.Errorf("resolving repos root %s: %w", reposRoot, err)
	}
	workspace := filepath.Join(root, segment)

	rel, err := filepath.Rel(root, workspace)
	if err != nil || rel == ".." || strings.HasPrefix(rel, ".."+string(filepath.Separator)) {
		return "", fmt.Errorf("%w: %q escapes repos root", ErrInvalidRepository, repositories[0])
	}
	return workspace, nil
}

// ResolveWorkspace derives a local filesystem workspace path from a
// project's first configured repository identifier (e.g.
// "github.com/timmersuk/llm-workbench"), by convention: the identifier's
// last path segment joined under reposRoot (so a repo checked out as a
// sibling directory of this workbench, e.g. "D:\projects\llm-workbench",
// resolves correctly). If no local checkout exists yet, one is cloned
// (over HTTPS, lazily, on this call) before proceeding — see
// cloneIfAbsent. The result is validated to exist as a directory and to
// never escape reposRoot — this is the only place an AgentRunner's cwd is
// decided, so a caller can never point an agent at an arbitrary path.
func ResolveWorkspace(ctx context.Context, reposRoot string, repositories []string) (string, error) {
	workspace, err := workspacePath(reposRoot, repositories)
	if err != nil {
		return "", err
	}

	info, err := os.Stat(workspace)
	if err != nil {
		if !os.IsNotExist(err) {
			return "", fmt.Errorf("%w: resolving workspace %s: %v", ErrInvalidRepository, workspace, err)
		}
		if err := cloneIfAbsent(ctx, filepath.Dir(workspace), workspace, repositories[0]); err != nil {
			return "", err
		}
		info, err = os.Stat(workspace)
		if err != nil {
			return "", fmt.Errorf("%w: resolving workspace %s after clone: %v", ErrInvalidRepository, workspace, err)
		}
	}
	if !info.IsDir() {
		return "", fmt.Errorf("%w: %s is not a directory", ErrInvalidRepository, workspace)
	}
	return workspace, nil
}

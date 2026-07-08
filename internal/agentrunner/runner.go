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

	"github.com/timmersuk/llm-workbench/internal/chat"
)

// ErrNoRepository is returned by ResolveWorkspace when the project has no
// configured repository to derive a workspace from.
var ErrNoRepository = errors.New("project has no configured repository")

// ErrInvalidRepository is returned by ResolveWorkspace when the project's
// repository identifier doesn't resolve to a valid, existing local
// checkout under reposRoot.
var ErrInvalidRepository = errors.New("invalid repository")

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
	// Tool is the Draft-proposing tool the agent should call once its
	// proposal is ready (stageTool in internal/api/stage_conversation.go) —
	// the same tool the local-LLM chat path registers, so both paths
	// produce the same task.ConversationToolCall shape downstream. The
	// zero value (Tool.Function.Name == "") means no tool is offered —
	// free-chat callers leave this unset.
	Tool chat.Tool
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
}

// RunOutput is the result of one AgentRunner.Run call: the assistant's
// final text content, plus an optional decoded Draft tool call if the
// agent proposed one during the turn.
type RunOutput struct {
	Content  string
	ToolCall *chat.ToolCall
}

// AgentRunner runs one turn of an agentic conversation against a
// tool-equipped coding agent or chat provider, isolated per
// RunInput.SessionKey, streaming assistant deltas via onDelta as they
// arrive. Satisfied by *ClaudeRunner and *ChatClientRunner; a
// codex_runner.go implementation is expected to follow the same interface.
type AgentRunner interface {
	Run(ctx context.Context, in RunInput, onDelta func(chat.Delta) error) (RunOutput, error)

	// CheckHealth reports whether this runner can actually be used right
	// now — a live probe, not a static configuration check (mirrors
	// chat.ChatClient.CheckHealth). Callers use this to decide whether to
	// offer the runner as a choice at all (internal/api/agent_executors.go).
	CheckHealth(ctx context.Context) error

	// ListModels returns the models this runner lets a caller select via
	// RunInput.Model, or (nil, nil) if this runner doesn't support
	// per-request model selection at all (not an error).
	ListModels(ctx context.Context) ([]string, error)

	// CloseSession discards any state this runner holds for sessionKey
	// (cached subprocess connections, in-memory history, ...). Safe to
	// call for a key that never had any (no-op).
	CloseSession(sessionKey string)
}

// ResolveWorkspace derives a local filesystem workspace path from a
// project's first configured repository identifier (e.g.
// "github.com/timmersuk/llm-workbench"), by convention: the identifier's
// last path segment joined under reposRoot (so a repo checked out as a
// sibling directory of this workbench, e.g. "D:\projects\llm-workbench",
// resolves correctly). The result is validated to exist as a directory and
// to never escape reposRoot — this is the only place an AgentRunner's cwd
// is decided, so a caller can never point an agent at an arbitrary path.
func ResolveWorkspace(reposRoot string, repositories []string) (string, error) {
	if len(repositories) == 0 {
		return "", ErrNoRepository
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

	info, err := os.Stat(workspace)
	if err != nil {
		return "", fmt.Errorf("%w: resolving workspace %s: %v", ErrInvalidRepository, workspace, err)
	}
	if !info.IsDir() {
		return "", fmt.Errorf("%w: %s is not a directory", ErrInvalidRepository, workspace)
	}
	return workspace, nil
}

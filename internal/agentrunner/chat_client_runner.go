package agentrunner

import (
	"context"
	"errors"
	"fmt"
	"os"
	"sync"
	"time"

	"github.com/timmersuk/llm-workbench/internal/chat"
	"github.com/timmersuk/llm-workbench/internal/toolloop"
)

// chatClientMaxResponseTokens caps tokens generated per model response in the
// tool loop. It bounds a misbehaving local model that spirals or repeats
// (Milestone 8 Phase 0) while leaving ample room for a full interview answer
// plus reasoning. Distinct from the loop's turn bound (claudeRunnerMaxTurns).
const chatClientMaxResponseTokens = 8192

// ChatClientRunner adapts a chat.ChatClient into the AgentRunner interface,
// so both stage-conversation agents and the free-floating Chat tab are
// selectable and health-checked through the same abstraction.
//
// It drives internal/toolloop's shared engine: when a run is given a valid
// workspace it offers the read-only Read/Grep/Glob toolset so the local-LLM
// path can ground its answers in the reference repository, the same way
// ClaudeRunner already can (closing the chatclient-tool-loop task). The
// engine is stateless, so this runner owns per-SessionKey conversation
// history: only the human turn and the assistant's final text persist across
// turns; the loop's intermediate tool-call/result messages are ephemeral,
// keeping the durable context flat (matching what a rehydration from
// RunInput.History produces — see api.conversationHistoryToChatMessages) and
// honoring the "no hidden state" invariant.
type ChatClientRunner struct {
	client chat.ChatClient
	engine *toolloop.Engine

	mu       sync.Mutex
	sessions map[string][]chat.Message // per-SessionKey history: user/assistant turns, no system message
}

// NewChatClientRunner returns an AgentRunner backed by client.
func NewChatClientRunner(client chat.ChatClient) *ChatClientRunner {
	return &ChatClientRunner{
		client:   client,
		engine:   toolloop.New(client),
		sessions: make(map[string][]chat.Message),
	}
}

// Run implements AgentRunner. It builds the message list (system prompt +
// held history + the new user message), runs the tool loop offering the
// read-only toolset when in.Workspace is a usable directory, and offers
// in.Tool (the Draft-proposing tool) as the loop's stop condition — so a
// Draft proposal surfaces as RunOutput.ToolCall exactly like ClaudeRunner's.
func (r *ChatClientRunner) Run(ctx context.Context, in RunInput, onDelta func(chat.Delta) error) (RunOutput, error) {
	if in.SessionKey == "" {
		return RunOutput{}, errors.New("chat client runner requires a non-empty SessionKey")
	}

	// Snapshot the session's held history, seeding it from in.History the
	// first time we see this key (rehydration after a restart). History is
	// ignored once a live session exists — replaying it would duplicate turns
	// the runner already holds.
	r.mu.Lock()
	base, live := r.sessions[in.SessionKey]
	if !live && len(in.History) > 0 {
		base = append([]chat.Message(nil), in.History...)
		r.sessions[in.SessionKey] = base
	}
	snapshot := append([]chat.Message(nil), base...)
	r.mu.Unlock()

	msgs := make([]chat.Message, 0, len(snapshot)+2)
	if in.SystemPrompt != "" {
		msgs = append(msgs, chat.Message{Role: "system", Content: in.SystemPrompt})
	}
	msgs = append(msgs, snapshot...)
	msgs = append(msgs, chat.Message{Role: "user", Content: in.UserMessage})

	cfg := toolloop.Config{
		Model:     in.Model,
		Workspace: in.Workspace,
		Tools:     loopToolsFor(in.Workspace, in.EnableBashTool),
		MaxTurns:  claudeRunnerMaxTurns,
		MaxTokens: chatClientMaxResponseTokens,
	}
	if in.Tool.Function.Name != "" {
		tool := in.Tool
		cfg.StopTool = &tool
	}

	res, err := r.engine.Run(ctx, cfg, msgs, onDelta)
	if err != nil {
		// A failed turn is not persisted — the history stays at its
		// pre-turn state so the next attempt rebuilds cleanly.
		return RunOutput{Content: res.Content, ToolCall: res.StopCall}, err
	}

	content := res.Content
	if res.Exhausted {
		content += "\n\n[Note: reached the tool-exploration turn limit; this answer may be incomplete.]"
	}

	// Persist the human turn and the assistant's final text. A Draft proposal
	// is folded into the assistant text (not a structured tool call), matching
	// api.conversationHistoryToChatMessages so the live store and a rehydrated
	// one are identical and no dangling tool_call is left for the next turn.
	assistantContent := content
	if res.StopCall != nil {
		assistantContent += fmt.Sprintf("\n(proposed a draft via %s: %s)", res.StopCall.Function.Name, res.StopCall.Function.Arguments)
	}
	r.mu.Lock()
	turns := append(r.sessions[in.SessionKey], chat.Message{Role: "user", Content: in.UserMessage})
	turns = append(turns, chat.Message{Role: "assistant", Content: assistantContent})
	r.sessions[in.SessionKey] = turns
	r.mu.Unlock()

	return RunOutput{Content: content, ToolCall: res.StopCall}, nil
}

// loopToolsFor returns the loop toolset for a usable workspace, else nil (a
// plain completion with no tools). When enableBash is set — the Review stage's
// automated-checks phase (Milestone 6) — it returns the read-only set plus the
// confined bash tool; otherwise the strictly read-only set Requirements/
// Planning and free chat use. A run with no valid workspace degrades to
// text-only regardless.
func loopToolsFor(workspace string, enableBash bool) []toolloop.Tool {
	if workspace == "" {
		return nil
	}
	info, err := os.Stat(workspace)
	if err != nil || !info.IsDir() {
		return nil
	}
	if enableBash {
		return toolloop.ReviewTools()
	}
	return toolloop.ReadOnlyTools()
}

// Execute implements AgentRunner. It drives internal/toolloop's engine with
// the write-enabled toolset (Read/Grep/Glob/Write/Edit; Bash follows in a
// later PR, per milestone8.md's "Bash: scope and posture") against the
// isolated execution worktree in.Workspace. Unlike Run, there is no history
// to rehydrate and no StopTool: the loop's only natural stop is the model
// finishing without a further tool call, matching an autonomous
// Implementation-stage run. Turn exhaustion is Execute's one meaningful
// failure mode distinct from Run's graceful degradation (milestone8.md's
// "Turn exhaustion" decision): it comes back as an error, with whatever
// partial ExecuteOutput accumulated, so the caller records a failed
// execution.yaml rather than a silently incomplete one.
func (r *ChatClientRunner) Execute(ctx context.Context, in ExecuteInput, onEvent func(ExecuteEvent) error) (ExecuteOutput, error) {
	if in.Workspace == "" {
		return ExecuteOutput{}, errors.New("chat client runner requires a resolved execution workspace")
	}

	msgs := make([]chat.Message, 0, 2)
	if in.SystemPrompt != "" {
		msgs = append(msgs, chat.Message{Role: "system", Content: in.SystemPrompt})
	}
	msgs = append(msgs, chat.Message{Role: "user", Content: executionKickoffMessage})

	cfg := toolloop.Config{
		Model:     in.Model,
		Workspace: in.Workspace,
		Tools:     toolloop.ExecutionTools(),
		MaxTurns:  claudeExecutionMaxTurns,
		MaxTokens: chatClientMaxResponseTokens,
	}
	var onDelta func(chat.Delta) error
	if onEvent != nil {
		cfg.OnToolCall = func(name, argumentsJSON string) error {
			return onEvent(ExecuteEvent{Kind: "tool_call", ToolName: name, ToolInput: argumentsJSON})
		}
		cfg.OnToolResult = func(name, result string, isError bool) error {
			return onEvent(ExecuteEvent{Kind: "tool_result", ToolResult: result, IsError: isError})
		}
		onDelta = func(d chat.Delta) error {
			if d.Content == "" {
				return nil
			}
			return onEvent(ExecuteEvent{Kind: "text", Text: d.Content})
		}
	}

	start := time.Now()
	res, err := r.engine.Run(ctx, cfg, msgs, onDelta)
	out := ExecuteOutput{
		Content:         res.Content,
		DurationSeconds: time.Since(start).Seconds(),
		TokensUsed:      res.TokensUsed,
		NumTurns:        res.Turns,
	}
	if err != nil {
		return out, err
	}
	if res.Exhausted {
		return out, fmt.Errorf("execution exhausted its %d-turn budget without finishing", cfg.MaxTurns)
	}
	return out, nil
}

// CheckHealth implements AgentRunner.
func (r *ChatClientRunner) CheckHealth(ctx context.Context) error {
	return r.client.CheckHealth(ctx)
}

// ListModels implements AgentRunner.
func (r *ChatClientRunner) ListModels(ctx context.Context) ([]string, error) {
	return r.client.ListModels(ctx)
}

// CloseSession implements AgentRunner, discarding this runner's held history
// for sessionKey (and any state the wrapped client may still hold).
func (r *ChatClientRunner) CloseSession(sessionKey string) {
	r.mu.Lock()
	delete(r.sessions, sessionKey)
	r.mu.Unlock()
	r.client.CloseSession(sessionKey)
}

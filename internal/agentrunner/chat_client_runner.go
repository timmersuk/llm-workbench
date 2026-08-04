package agentrunner

import (
	"context"
	"errors"
	"fmt"
	"os"
	"sync"
	"time"

	"github.com/timmersuk/llm-workbench/internal/chat"
	"github.com/timmersuk/llm-workbench/internal/knowledgetool"
	"github.com/timmersuk/llm-workbench/internal/toolloop"
)

// chatClientMaxResponseTokens caps tokens generated per model response in the
// tool loop. It bounds a misbehaving local model that spirals or repeats
// (Milestone 8 Phase 0) while leaving ample room for a full interview answer
// plus reasoning. Distinct from the loop's turn bound (RunInput.MaxTurns).
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
	client         chat.ChatClient
	engine         *toolloop.Engine
	knowledgeStore knowledgetool.Store

	mu            sync.Mutex
	sessions      map[string][]chat.Message // per-SessionKey history: user/assistant turns, no system message
	defaultModel  string
	defaultEffort ReasoningEffort
}

// NewChatClientRunner returns an AgentRunner backed by client.
// knowledgeStore, if non-nil, offers the always-available knowledge query
// tools (docs/milestones/done/milestone9.md) on every turn regardless of stage
// or workspace — a nil knowledgeStore (e.g. in tests that don't care about
// this) just means those two tools are never advertised.
func NewChatClientRunner(client chat.ChatClient, knowledgeStore knowledgetool.Store, defaults ...Selection) *ChatClientRunner {
	r := &ChatClientRunner{
		client:         client,
		engine:         toolloop.New(client),
		knowledgeStore: knowledgeStore,
		sessions:       make(map[string][]chat.Message),
		defaultEffort:  EffortMedium,
	}
	if len(defaults) > 0 {
		r.defaultModel = defaults[0].Model
		r.defaultEffort = defaults[0].Effort
	}
	return r
}

// Run implements AgentRunner. It builds the message list (system prompt +
// held history + the new user message), runs the tool loop offering the
// read-only toolset when in.Workspace is a usable directory, and offers
// in.Tools (the Draft-proposing tool(s)) as the loop's stop condition — so a
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
		Model:           in.Model,
		ReasoningEffort: string(in.ReasoningEffort),
		Workspace:       in.Workspace,
		Tools:           r.loopTools(in.Workspace, in.EnableBashTool),
		MaxTurns:        in.MaxTurns,
		MaxTokens:       chatClientMaxResponseTokens,
	}
	if len(in.Tools) > 0 {
		cfg.StopTools = in.Tools
	}
	// Surface the loop's intermediate tool activity (Read/Grep/Glob/bash) to
	// the caller as it happens, exactly as Execute already does. Only the
	// engine-executed tools flow through here — the final Draft stop call
	// surfaces separately as res.StopCall, not as a tool_call/tool_result
	// event.
	if in.OnToolCall != nil {
		cfg.OnToolCall = func(id, name, argumentsJSON string) error {
			in.OnToolCall(id, name, argumentsJSON)
			return nil
		}
	}
	if in.OnToolResult != nil {
		cfg.OnToolResult = func(id, name, result string, isError bool) error {
			in.OnToolResult(id, name, result, isError)
			return nil
		}
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

// loopTools returns the loop toolset for one turn: the always-available
// knowledge query tools (docs/milestones/done/milestone9.md — independent of
// workspace, since data/knowledge/ is a workspace-wide bundle, not part of
// any project's checked-out repository) plus, only when workspace actually
// resolves to a usable directory, the read-only Read/Grep/Glob set (and the
// confined bash tool too when enableBash is set — the Review stage's
// automated-checks phase, Milestone 6). A run with no valid workspace still
// offers the knowledge tools; it just degrades the file/bash tools to none,
// same as before this method existed.
func (r *ChatClientRunner) loopTools(workspace string, enableBash bool) []toolloop.Tool {
	tools := toolloop.KnowledgeTools(r.knowledgeStore)

	if workspace == "" {
		return tools
	}
	info, err := os.Stat(workspace)
	if err != nil || !info.IsDir() {
		return tools
	}
	if enableBash {
		return append(tools, toolloop.ReviewTools()...)
	}
	return append(tools, toolloop.ReadOnlyTools()...)
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
		Model:           in.Model,
		ReasoningEffort: string(in.ReasoningEffort),
		Workspace:       in.Workspace,
		Tools:           toolloop.ExecutionTools(),
		MaxTurns:        in.MaxTurns,
		MaxTokens:       chatClientMaxResponseTokens,
	}
	var onDelta func(chat.Delta) error
	if onEvent != nil {
		cfg.OnToolCall = func(id, name, argumentsJSON string) error {
			return onEvent(ExecuteEvent{Kind: "tool_call", ID: id, ToolName: name, ToolInput: argumentsJSON})
		}
		cfg.OnToolResult = func(id, name, result string, isError bool) error {
			return onEvent(ExecuteEvent{Kind: "tool_result", ID: id, ToolResult: result, IsError: isError})
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

func (r *ChatClientRunner) Capabilities(ctx context.Context) (ExecutorCapabilities, error) {
	models, err := r.client.ListModels(ctx)
	if err != nil {
		if r.defaultModel == "" {
			return ExecutorCapabilities{}, err
		}
		models = []string{r.defaultModel}
	}
	defaultModel := r.defaultModel
	if defaultModel == "" && len(models) > 0 {
		defaultModel = models[0]
	}
	return ExecutorCapabilities{Name: "local", Models: models, Efforts: []ReasoningEffort{EffortLow, EffortMedium, EffortHigh}, DefaultModel: defaultModel, DefaultEffort: r.defaultEffort}, nil
}

// CloseSession implements AgentRunner, discarding this runner's held history
// for sessionKey (and any state the wrapped client may still hold).
func (r *ChatClientRunner) CloseSession(sessionKey string) {
	r.mu.Lock()
	delete(r.sessions, sessionKey)
	r.mu.Unlock()
	r.client.CloseSession(sessionKey)
}

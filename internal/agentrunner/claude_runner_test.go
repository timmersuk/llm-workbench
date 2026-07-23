package agentrunner

import (
	"context"
	"errors"
	"strings"
	"testing"
	"time"

	claudecode "github.com/severity1/claude-agent-sdk-go"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/timmersuk/llm-workbench/internal/chat"
	"github.com/timmersuk/llm-workbench/internal/knowledge"
)

func TestClaudeRunner_TryLockRejectsConcurrentSameKey(t *testing.T) {
	r := NewClaudeRunner(time.Minute, "", nil)

	assert.True(t, r.tryLock("task-a:planning"))
	assert.False(t, r.tryLock("task-a:planning"), "a second lock for the same key must be rejected")
	assert.True(t, r.tryLock("task-b:planning"), "a different key must not be blocked")

	r.unlock("task-a:planning")
	assert.True(t, r.tryLock("task-a:planning"), "unlocking must free the key for reuse")
}

func TestProcessMessage_AccumulatesText(t *testing.T) {
	var content strings.Builder
	var out RunOutput

	msg := &claudecode.AssistantMessage{
		Content: []claudecode.ContentBlock{&claudecode.TextBlock{Text: "hello "}},
	}
	done, err := processMessage(msg, []string{"propose_plan"}, &content, &out, nil, nil)
	require.NoError(t, err)
	assert.False(t, done)
	assert.Equal(t, "hello ", content.String())
}

func TestProcessMessage_CapturesMatchingToolCall(t *testing.T) {
	var content strings.Builder
	var out RunOutput

	msg := &claudecode.AssistantMessage{
		Content: []claudecode.ContentBlock{&claudecode.ToolUseBlock{
			ToolUseID: "call-1",
			Name:      "mcp__draft__propose_plan",
			Input:     map[string]any{"approach": "do it"},
		}},
	}
	done, err := processMessage(msg, []string{"propose_plan"}, &content, &out, nil, nil)
	require.NoError(t, err)
	assert.False(t, done)
	require.NotNil(t, out.ToolCall)
	assert.Equal(t, "call-1", out.ToolCall.ID)
	// Function.Name is the bare tool name (matching stageTool's schema
	// constants and the frontend's tool_call.name matching), even though
	// the CLI reports the fully-qualified mcp__draft__propose_plan.
	assert.Equal(t, "propose_plan", out.ToolCall.Function.Name)
	assert.JSONEq(t, `{"approach":"do it"}`, out.ToolCall.Function.Arguments)
}

func TestProcessMessage_IgnoresNonMatchingToolCall(t *testing.T) {
	var content strings.Builder
	var out RunOutput

	msg := &claudecode.AssistantMessage{
		Content: []claudecode.ContentBlock{&claudecode.ToolUseBlock{Name: "Read", Input: map[string]any{}}},
	}
	_, err := processMessage(msg, []string{"propose_plan"}, &content, &out, nil, nil)
	require.NoError(t, err)
	assert.Nil(t, out.ToolCall)
}

// TestProcessMessage_ForwardsNonDraftToolCallToHooks locks in the docs/adr/0018
// fix: a ToolUseBlock that isn't the turn's Draft (Read/Grep/Glob, bash, a
// knowledge-query call) is genuine intermediate tool activity and must be
// forwarded through hooks.onCall, not silently dropped the way it was
// before this ADR (the claude CLI path "intentionally ignored" it).
func TestProcessMessage_ForwardsNonDraftToolCallToHooks(t *testing.T) {
	var content strings.Builder
	var out RunOutput
	var calls []string
	hooks := &toolActivityHooks{
		onCall:  func(name, argsJSON string) { calls = append(calls, name+":"+argsJSON) },
		pending: make(map[string]string),
	}

	msg := &claudecode.AssistantMessage{
		Content: []claudecode.ContentBlock{&claudecode.ToolUseBlock{
			ToolUseID: "call-1", Name: "Read", Input: map[string]any{"path": "a.go"},
		}},
	}
	_, err := processMessage(msg, []string{"propose_plan"}, &content, &out, nil, hooks)
	require.NoError(t, err)
	require.Len(t, calls, 1)
	assert.JSONEq(t, `{"path":"a.go"}`, strings.TrimPrefix(calls[0], "Read:"))
	assert.Nil(t, out.ToolCall, "a non-Draft call must never populate out.ToolCall")
	assert.Equal(t, "Read", hooks.pending["call-1"], "the call's name must be tracked for its later result to correlate against")
}

// TestProcessMessage_DraftToolCallNeverForwardedAsActivity ensures the two
// mechanisms stay disjoint: the turn's actual Draft proposal (matches
// in.Tools) must populate out.ToolCall as before, and must NOT also fire
// hooks.onCall — Tool Activity (CONTEXT.md) is explicitly "distinct from a
// Draft."
func TestProcessMessage_DraftToolCallNeverForwardedAsActivity(t *testing.T) {
	var content strings.Builder
	var out RunOutput
	var calls int
	hooks := &toolActivityHooks{onCall: func(string, string) { calls++ }, pending: make(map[string]string)}

	msg := &claudecode.AssistantMessage{
		Content: []claudecode.ContentBlock{&claudecode.ToolUseBlock{
			ToolUseID: "call-1", Name: "mcp__draft__propose_plan", Input: map[string]any{},
		}},
	}
	_, err := processMessage(msg, []string{"propose_plan"}, &content, &out, nil, hooks)
	require.NoError(t, err)
	require.NotNil(t, out.ToolCall)
	assert.Zero(t, calls, "the Draft's own proposal call must never also surface as tool activity")
	assert.Empty(t, hooks.pending, "a Draft call must not be tracked for result correlation either")
}

// TestProcessMessage_ForwardsToolResultCorrelatedByID covers the other half:
// the claude CLI reports a ToolResultBlock in a later UserMessage carrying
// only a toolUseID, not the tool's name — hooks.pending (populated when the
// matching ToolUseBlock was seen) is what lets onResult still report which
// tool this result belongs to.
func TestProcessMessage_ForwardsToolResultCorrelatedByID(t *testing.T) {
	var content strings.Builder
	var out RunOutput
	type result struct {
		name    string
		text    string
		isError bool
	}
	var results []result
	hooks := &toolActivityHooks{
		onResult: func(name, text string, isError bool) { results = append(results, result{name, text, isError}) },
		pending:  map[string]string{"call-1": "Grep"},
	}

	isError := true
	msg := &claudecode.UserMessage{
		Content: []claudecode.ContentBlock{
			&claudecode.ToolResultBlock{ToolUseID: "call-1", Content: "no matches", IsError: &isError},
		},
	}
	_, err := processMessage(msg, nil, &content, &out, nil, hooks)
	require.NoError(t, err)
	require.Len(t, results, 1)
	assert.Equal(t, result{"Grep", "no matches", true}, results[0])
	assert.Empty(t, hooks.pending, "a correlated result must be popped from pending, not left to leak")
}

// TestProcessMessage_ThinkingBlockForwardsAsReasoningDelta locks in the
// docs/adr/0018 forward-compatible fix: a ThinkingBlock (only ever present
// once extended thinking is enabled — not done anywhere in this codebase
// today) must feed onDelta's ReasoningContent, the same field/UI path the
// OpenAI-SDK executors' reasoning_content already uses, rather than being
// silently dropped by processMessage's switch.
func TestProcessMessage_ThinkingBlockForwardsAsReasoningDelta(t *testing.T) {
	var content strings.Builder
	var out RunOutput
	var received []chat.Delta

	msg := &claudecode.AssistantMessage{
		Content: []claudecode.ContentBlock{&claudecode.ThinkingBlock{Thinking: "considering the options..."}},
	}
	_, err := processMessage(msg, nil, &content, &out, func(d chat.Delta) error {
		received = append(received, d)
		return nil
	}, nil)
	require.NoError(t, err)
	assert.Equal(t, []chat.Delta{{ReasoningContent: "considering the options..."}}, received)
}

// TestProcessMessage_RequiresFullyQualifiedMcpName locks in a real bug
// caught during live e2e verification: the `claude` CLI reports an
// in-process MCP tool's ToolUseBlock.Name fully qualified
// (mcp__<server>__<tool>), never as the bare tool name a caller passes in
// RunInput.Tools — comparing against the bare name silently drops every
// Draft proposal.
func TestProcessMessage_RequiresFullyQualifiedMcpName(t *testing.T) {
	var content strings.Builder
	var out RunOutput

	msg := &claudecode.AssistantMessage{
		Content: []claudecode.ContentBlock{&claudecode.ToolUseBlock{Name: "propose_plan", Input: map[string]any{}}},
	}
	_, err := processMessage(msg, []string{"propose_plan"}, &content, &out, nil, nil)
	require.NoError(t, err)
	assert.Nil(t, out.ToolCall, "the bare tool name alone must not match — the CLI always reports it fully qualified")
}

// TestProcessMessage_MatchesAnyOfSeveralOfferedTools covers Review's shape
// (docs/milestones/done/milestone9.md): a session can offer more than one Draft
// tool at once (propose_review, propose_knowledge), and a call to either
// one is recognized as the turn's proposal.
func TestProcessMessage_MatchesAnyOfSeveralOfferedTools(t *testing.T) {
	var content strings.Builder
	var out RunOutput

	msg := &claudecode.AssistantMessage{
		Content: []claudecode.ContentBlock{&claudecode.ToolUseBlock{
			ToolUseID: "call-1",
			Name:      "mcp__draft__propose_knowledge",
			Input:     map[string]any{"concept_id": "x"},
		}},
	}
	done, err := processMessage(msg, []string{"propose_review", "propose_knowledge"}, &content, &out, nil, nil)
	require.NoError(t, err)
	assert.False(t, done)
	require.NotNil(t, out.ToolCall)
	assert.Equal(t, "propose_knowledge", out.ToolCall.Function.Name)
}

func TestProcessMessage_KeepsFirstToolCallOnly(t *testing.T) {
	var content strings.Builder
	out := RunOutput{ToolCall: &chat.ToolCall{ID: "first", Type: "function"}}

	msg := &claudecode.AssistantMessage{
		Content: []claudecode.ContentBlock{&claudecode.ToolUseBlock{
			ToolUseID: "second",
			Name:      "mcp__draft__propose_plan",
			Input:     map[string]any{},
		}},
	}
	_, err := processMessage(msg, []string{"propose_plan"}, &content, &out, nil, nil)
	require.NoError(t, err)
	assert.Equal(t, "first", out.ToolCall.ID)
}

func TestProcessMessage_ResultMessageEndsTurnSuccessfully(t *testing.T) {
	var content strings.Builder
	content.WriteString("final text")
	var out RunOutput

	done, err := processMessage(&claudecode.ResultMessage{IsError: false}, []string{"propose_plan"}, &content, &out, nil, nil)
	require.NoError(t, err)
	assert.True(t, done)
	assert.Equal(t, "final text", out.Content)
}

func TestProcessMessage_ResultMessageReportsError(t *testing.T) {
	var content strings.Builder
	var out RunOutput

	done, err := processMessage(&claudecode.ResultMessage{IsError: true, Errors: []string{"boom"}}, []string{"propose_plan"}, &content, &out, nil, nil)
	assert.True(t, done)
	require.Error(t, err)
	assert.Contains(t, err.Error(), "boom")
}

func TestProcessMessage_StreamDeltaInvokesOnDelta(t *testing.T) {
	var content strings.Builder
	var out RunOutput
	var received []chat.Delta

	msg := &claudecode.StreamEvent{Event: map[string]any{
		"type":  claudecode.StreamEventTypeContentBlockDelta,
		"delta": map[string]any{"text": "chunk"},
	}}
	done, err := processMessage(msg, []string{"propose_plan"}, &content, &out, func(d chat.Delta) error {
		received = append(received, d)
		return nil
	}, nil)
	require.NoError(t, err)
	assert.False(t, done)
	assert.Equal(t, []chat.Delta{{Content: "chunk"}}, received)
}

func TestProcessMessage_StreamDeltaPropagatesOnDeltaError(t *testing.T) {
	var content strings.Builder
	var out RunOutput
	wantErr := errors.New("delta failed")

	msg := &claudecode.StreamEvent{Event: map[string]any{
		"type":  claudecode.StreamEventTypeContentBlockDelta,
		"delta": map[string]any{"text": "chunk"},
	}}
	done, err := processMessage(msg, []string{"propose_plan"}, &content, &out, func(chat.Delta) error { return wantErr }, nil)
	assert.True(t, done)
	assert.ErrorIs(t, err, wantErr)
}

func TestStreamDeltaText_IgnoresNonDeltaEvents(t *testing.T) {
	ev := &claudecode.StreamEvent{Event: map[string]any{"type": claudecode.StreamEventTypeMessageStop}}
	_, ok := streamDeltaText(ev)
	assert.False(t, ok)
}

// TestStreamReasoningDeltaText_ExtractsThinkingField locks in the docs/adr/0018
// fix: a thinking_delta content_block_delta event carries its incremental
// text under "thinking", not "text" — streamDeltaText alone (checking only
// "text") silently drops it, which is exactly the bug this function closes.
func TestStreamReasoningDeltaText_ExtractsThinkingField(t *testing.T) {
	ev := &claudecode.StreamEvent{Event: map[string]any{
		"type":  claudecode.StreamEventTypeContentBlockDelta,
		"delta": map[string]any{"type": "thinking_delta", "thinking": "hmm, "},
	}}
	text, ok := streamReasoningDeltaText(ev)
	require.True(t, ok)
	assert.Equal(t, "hmm, ", text)

	// A plain text_delta event must not also look like a reasoning delta.
	_, ok = streamReasoningDeltaText(&claudecode.StreamEvent{Event: map[string]any{
		"type":  claudecode.StreamEventTypeContentBlockDelta,
		"delta": map[string]any{"text": "chunk"},
	}})
	assert.False(t, ok)
}

func TestProcessMessage_StreamReasoningDeltaInvokesOnDeltaAsReasoning(t *testing.T) {
	var content strings.Builder
	var out RunOutput
	var received []chat.Delta

	msg := &claudecode.StreamEvent{Event: map[string]any{
		"type":  claudecode.StreamEventTypeContentBlockDelta,
		"delta": map[string]any{"type": "thinking_delta", "thinking": "chunk"},
	}}
	done, err := processMessage(msg, nil, &content, &out, func(d chat.Delta) error {
		received = append(received, d)
		return nil
	}, nil)
	require.NoError(t, err)
	assert.False(t, done)
	assert.Equal(t, []chat.Delta{{ReasoningContent: "chunk"}}, received)
}

func TestProcessExecuteMessage_AccumulatesText(t *testing.T) {
	var content strings.Builder
	var out ExecuteOutput

	msg := &claudecode.AssistantMessage{
		Content: []claudecode.ContentBlock{&claudecode.TextBlock{Text: "implementing... "}},
	}
	done, err := processExecuteMessage(msg, &content, &out, nil)
	require.NoError(t, err)
	assert.False(t, done)
	assert.Equal(t, "implementing... ", content.String())
}

// TestProcessExecuteMessage_EmitsEveryToolCall locks in the behavior that
// makes Execute different from Run's processMessage: every ToolUseBlock
// becomes a tool_call event, not just one matching a registered Draft
// tool — Execute has no Draft tool at all, and dropping Write/Edit/Bash
// calls would defeat the point of streaming a live execution trace.
func TestProcessExecuteMessage_EmitsEveryToolCall(t *testing.T) {
	var content strings.Builder
	var out ExecuteOutput
	var events []ExecuteEvent

	msg := &claudecode.AssistantMessage{
		Content: []claudecode.ContentBlock{
			&claudecode.ToolUseBlock{Name: "Write", Input: map[string]any{"path": "a.go"}},
			&claudecode.ToolUseBlock{Name: "Bash", Input: map[string]any{"command": "go test ./..."}},
		},
	}
	done, err := processExecuteMessage(msg, &content, &out, func(e ExecuteEvent) error {
		events = append(events, e)
		return nil
	})
	require.NoError(t, err)
	assert.False(t, done)
	require.Len(t, events, 2)
	assert.Equal(t, "tool_call", events[0].Kind)
	assert.Equal(t, "Write", events[0].ToolName)
	assert.JSONEq(t, `{"path":"a.go"}`, events[0].ToolInput)
	assert.Equal(t, "Bash", events[1].ToolName)
}

func TestProcessExecuteMessage_EmitsToolResultFromUserMessage(t *testing.T) {
	var content strings.Builder
	var out ExecuteOutput
	var events []ExecuteEvent

	isError := true
	msg := &claudecode.UserMessage{
		Content: []claudecode.ContentBlock{
			&claudecode.ToolResultBlock{ToolUseID: "call-1", Content: "test failed: exit status 1", IsError: &isError},
		},
	}
	done, err := processExecuteMessage(msg, &content, &out, func(e ExecuteEvent) error {
		events = append(events, e)
		return nil
	})
	require.NoError(t, err)
	assert.False(t, done)
	require.Len(t, events, 1)
	assert.Equal(t, "tool_result", events[0].Kind)
	assert.Equal(t, "test failed: exit status 1", events[0].ToolResult)
	assert.True(t, events[0].IsError)
}

func TestProcessExecuteMessage_ToolResultDefaultsNotErrorWhenNil(t *testing.T) {
	var content strings.Builder
	var out ExecuteOutput
	var events []ExecuteEvent

	msg := &claudecode.UserMessage{
		Content: []claudecode.ContentBlock{
			&claudecode.ToolResultBlock{ToolUseID: "call-1", Content: "ok"},
		},
	}
	_, err := processExecuteMessage(msg, &content, &out, func(e ExecuteEvent) error {
		events = append(events, e)
		return nil
	})
	require.NoError(t, err)
	require.Len(t, events, 1)
	assert.False(t, events[0].IsError)
}

func TestProcessExecuteMessage_ResultMessagePopulatesRealMetrics(t *testing.T) {
	var content strings.Builder
	content.WriteString("done implementing")
	var out ExecuteOutput

	cost := 0.0421
	usage := map[string]any{"input_tokens": float64(120), "output_tokens": float64(45)}
	msg := &claudecode.ResultMessage{
		DurationMs:   2500,
		NumTurns:     7,
		TotalCostUSD: &cost,
		Usage:        &usage,
	}
	done, err := processExecuteMessage(msg, &content, &out, nil)
	require.NoError(t, err)
	assert.True(t, done)
	assert.Equal(t, "done implementing", out.Content)
	assert.Equal(t, 2.5, out.DurationSeconds)
	assert.Equal(t, 7, out.NumTurns)
	assert.Equal(t, 0.0421, out.CostEstimate)
	assert.Equal(t, 165, out.TokensUsed)
}

func TestProcessExecuteMessage_ResultMessageLeavesMetricsZeroWhenUnavailable(t *testing.T) {
	var content strings.Builder
	var out ExecuteOutput

	done, err := processExecuteMessage(&claudecode.ResultMessage{DurationMs: 1000}, &content, &out, nil)
	require.NoError(t, err)
	assert.True(t, done)
	assert.Equal(t, 0, out.TokensUsed)
	assert.Equal(t, 0.0, out.CostEstimate)
}

func TestProcessExecuteMessage_ResultMessageReportsError(t *testing.T) {
	var content strings.Builder
	var out ExecuteOutput

	done, err := processExecuteMessage(&claudecode.ResultMessage{IsError: true, Errors: []string{"boom"}}, &content, &out, nil)
	assert.True(t, done)
	require.Error(t, err)
	assert.Contains(t, err.Error(), "boom")
}

func TestProcessExecuteMessage_StreamDeltaEmitsTextEvent(t *testing.T) {
	var content strings.Builder
	var out ExecuteOutput
	var events []ExecuteEvent

	msg := &claudecode.StreamEvent{Event: map[string]any{
		"type":  claudecode.StreamEventTypeContentBlockDelta,
		"delta": map[string]any{"text": "chunk"},
	}}
	done, err := processExecuteMessage(msg, &content, &out, func(e ExecuteEvent) error {
		events = append(events, e)
		return nil
	})
	require.NoError(t, err)
	assert.False(t, done)
	assert.Equal(t, []ExecuteEvent{{Kind: "text", Text: "chunk"}}, events)
}

// TestProcessExecuteMessage_ThinkingBlockEmitsReasoningEvent mirrors
// TestProcessMessage_ThinkingBlockForwardsAsReasoningDelta for the Execute
// path (docs/adr/0018): a ThinkingBlock becomes a "reasoning" ExecuteEvent
// rather than being silently dropped. No frontend currently renders this
// Kind (ExecutePanel has no case for it) — the same "typed but not yet
// rendered" posture ChatStreamEvent.ToolActivity had before this ADR.
func TestProcessExecuteMessage_ThinkingBlockEmitsReasoningEvent(t *testing.T) {
	var content strings.Builder
	var out ExecuteOutput
	var events []ExecuteEvent

	msg := &claudecode.AssistantMessage{
		Content: []claudecode.ContentBlock{&claudecode.ThinkingBlock{Thinking: "weighing approaches..."}},
	}
	done, err := processExecuteMessage(msg, &content, &out, func(e ExecuteEvent) error {
		events = append(events, e)
		return nil
	})
	require.NoError(t, err)
	assert.False(t, done)
	assert.Equal(t, []ExecuteEvent{{Kind: "reasoning", Text: "weighing approaches..."}}, events)
}

func TestSumUsageTokens_NilMapReturnsZero(t *testing.T) {
	assert.Equal(t, 0, sumUsageTokens(nil))
}

func TestToolResultText_PassesThroughStrings(t *testing.T) {
	assert.Equal(t, "hello", toolResultText("hello"))
}

func TestToolResultText_EncodesStructuredContentAsJSON(t *testing.T) {
	assert.JSONEq(t, `{"ok":true}`, toolResultText(map[string]any{"ok": true}))
}

func TestClaudeRunner_CheckHealth_FailsWhenReposRootEmpty(t *testing.T) {
	r := NewClaudeRunner(time.Minute, "", nil)
	err := r.CheckHealth(context.Background())
	require.Error(t, err)
	assert.Contains(t, err.Error(), "AGENT_REPOS_ROOT")
}

func TestClaudeRunner_CheckHealth_FailsWhenCLINotOnPath(t *testing.T) {
	original := lookPath
	lookPath = func(string) (string, error) { return "", errors.New("not found") }
	defer func() { lookPath = original }()

	r := NewClaudeRunner(time.Minute, t.TempDir(), nil)
	err := r.CheckHealth(context.Background())
	require.Error(t, err)
	assert.Contains(t, err.Error(), "claude CLI not found on PATH")
}

func TestClaudeRunner_CheckHealth_OKWhenReposRootSetAndCLIFound(t *testing.T) {
	original := lookPath
	lookPath = func(string) (string, error) { return "/usr/bin/claude", nil }
	defer func() { lookPath = original }()

	r := NewClaudeRunner(time.Minute, t.TempDir(), nil)
	assert.NoError(t, r.CheckHealth(context.Background()))
}

func TestSystemPromptWithHistory_ReturnsUnchangedWhenEmpty(t *testing.T) {
	assert.Equal(t, "be nice", systemPromptWithHistory("be nice", nil))
}

func TestSystemPromptWithHistory_AppendsRenderedTranscript(t *testing.T) {
	got := systemPromptWithHistory("be nice", []chat.Message{
		{Role: "user", Content: "let's start"},
		{Role: "assistant", Content: "sure, tell me more"},
	})
	assert.Contains(t, got, "be nice")
	assert.Contains(t, got, "Prior conversation (restored after restart)")
	assert.Contains(t, got, "user: let's start")
	assert.Contains(t, got, "assistant: sure, tell me more")
}

func TestIsStaleClaudeConnectionError_MatchesKnownDeadPipeMessages(t *testing.T) {
	cases := []string{
		`querying claude code agent: failed to write message: write |1: The pipe is being closed.`,
		"write: broken pipe",
		"io: read/write on closed pipe",
		"transport not connected or stdin closed",
		"write : file already closed",
	}
	for _, msg := range cases {
		assert.True(t, isStaleClaudeConnectionError(errors.New(msg)), "expected %q to be detected as a stale connection", msg)
	}
}

func TestIsStaleClaudeConnectionError_IgnoresUnrelatedErrors(t *testing.T) {
	assert.False(t, isStaleClaudeConnectionError(errors.New("claude code agent run failed: something else entirely")))
}

// TestClaudeRunner_Run_ReconnectsOnceOnStaleConnectionThenSucceeds locks in
// the fix for a long idle gap between conversation turns outliving the
// cached `claude` CLI subprocess (reported live as "querying claude code
// agent: failed to write message: write |1: The pipe is being closed."):
// the stale cached client's first Query fails with a dead-pipe error, so
// Run must evict it (Disconnect + remove from the cache) and retry against
// a freshly constructed client rather than surfacing the raw pipe error.
func TestClaudeRunner_Run_ReconnectsOnceOnStaleConnectionThenSucceeds(t *testing.T) {
	r := NewClaudeRunner(time.Minute, "", nil)
	key := "task-a:requirements"

	stale := &fakeClaudeClient{queryErr: errors.New("write |1: The pipe is being closed.")}
	r.clients[key] = stale

	fresh := &fakeClaudeClient{}
	r.newClient = func(...claudecode.Option) claudecode.Client { return fresh }

	out, err := r.Run(context.Background(), RunInput{SessionKey: key, Workspace: t.TempDir()}, nil)

	require.NoError(t, err)
	assert.Equal(t, RunOutput{}, out)
	assert.Equal(t, 1, stale.queryCalls)
	assert.True(t, stale.disconnected, "the stale client should have been disconnected when evicted")
	assert.Equal(t, 1, fresh.queryCalls)
	assert.Same(t, fresh, r.clients[key], "the fresh client should now be the cached one")
}

// TestClaudeRunner_Run_SurfacesErrorWhenReconnectAlsoFails ensures the
// retry is only attempted once — if the freshly reconnected client's Query
// also fails, that error (not the original stale-pipe one) is what
// surfaces, and Run doesn't loop forever trying to reconnect.
func TestClaudeRunner_Run_SurfacesErrorWhenReconnectAlsoFails(t *testing.T) {
	r := NewClaudeRunner(time.Minute, "", nil)
	key := "task-a:requirements"

	stale := &fakeClaudeClient{queryErr: errors.New("broken pipe")}
	r.clients[key] = stale

	fresh := &fakeClaudeClient{queryErr: errors.New("still broken")}
	r.newClient = func(...claudecode.Option) claudecode.Client { return fresh }

	_, err := r.Run(context.Background(), RunInput{SessionKey: key, Workspace: t.TempDir()}, nil)

	require.Error(t, err)
	assert.Contains(t, err.Error(), "still broken")
	assert.Equal(t, 1, stale.queryCalls)
	assert.Equal(t, 1, fresh.queryCalls)
}

// TestClaudeRunner_Run_NonStaleQueryErrorIsNotRetried locks in that only
// the specific dead-pipe failure mode triggers a reconnect — a genuine
// query failure must surface immediately, not silently retry against a
// second, unnecessary subprocess.
func TestClaudeRunner_Run_NonStaleQueryErrorIsNotRetried(t *testing.T) {
	r := NewClaudeRunner(time.Minute, "", nil)
	key := "task-a:requirements"

	client := &fakeClaudeClient{queryErr: errors.New("some other failure")}
	r.clients[key] = client
	r.newClient = func(...claudecode.Option) claudecode.Client {
		t.Fatal("newClient must not be called for a non-stale query error")
		return nil
	}

	_, err := r.Run(context.Background(), RunInput{SessionKey: key, Workspace: t.TempDir()}, nil)

	require.Error(t, err)
	assert.Contains(t, err.Error(), "some other failure")
	assert.Equal(t, 1, client.queryCalls)
}

func TestClaudeRunner_ClientFor_ErrorsWhenWorkspaceEmpty(t *testing.T) {
	r := NewClaudeRunner(time.Minute, "", nil)
	_, err := r.clientFor(context.Background(), "task-a:requirements", RunInput{Workspace: ""})
	require.Error(t, err)
	assert.Contains(t, err.Error(), "claude-code requires a project repository")
}

// fakeKnowledgeStore is a minimal knowledgetool.Store stub, shared by the
// two handler tests below.
type fakeKnowledgeStore struct {
	summaries []knowledge.ConceptSummary
	concepts  map[string]knowledge.Concept
	listErr   error
	getErr    error
}

func (f *fakeKnowledgeStore) List() ([]knowledge.ConceptSummary, error) {
	if f.listErr != nil {
		return nil, f.listErr
	}
	return f.summaries, nil
}

func (f *fakeKnowledgeStore) Get(conceptID string) (knowledge.Concept, error) {
	if f.getErr != nil {
		return knowledge.Concept{}, f.getErr
	}
	c, ok := f.concepts[conceptID]
	if !ok {
		return knowledge.Concept{}, errors.New("not found")
	}
	return c, nil
}

func TestClaudeRunner_KnowledgeListHandler_ReturnsRealContent(t *testing.T) {
	r := NewClaudeRunner(time.Minute, "", &fakeKnowledgeStore{summaries: []knowledge.ConceptSummary{
		{ConceptID: "coding-standards/logging", Type: "Coding Standard"},
	}})
	result, err := r.knowledgeListHandler(context.Background(), nil)
	require.NoError(t, err)
	require.False(t, result.IsError)
	require.Len(t, result.Content, 1)
	assert.Contains(t, result.Content[0].Text, "coding-standards/logging")
}

func TestClaudeRunner_KnowledgeListHandler_StoreErrorSurfacesAsMcpError(t *testing.T) {
	r := NewClaudeRunner(time.Minute, "", &fakeKnowledgeStore{listErr: errors.New("disk on fire")})
	result, err := r.knowledgeListHandler(context.Background(), nil)
	require.NoError(t, err, "a store failure must surface as an MCP error result, not a Go error")
	assert.True(t, result.IsError)
	assert.Contains(t, result.Content[0].Text, "disk on fire")
}

func TestClaudeRunner_KnowledgeGetHandler_ReturnsRealContent(t *testing.T) {
	r := NewClaudeRunner(time.Minute, "", &fakeKnowledgeStore{concepts: map[string]knowledge.Concept{
		"a": {Type: "Reference", Body: "hello\n"},
	}})
	result, err := r.knowledgeGetHandler(context.Background(), map[string]any{"concept_id": "a"})
	require.NoError(t, err)
	require.False(t, result.IsError)
	assert.Contains(t, result.Content[0].Text, "hello")
}

func TestClaudeRunner_KnowledgeGetHandler_MissingConceptSurfacesAsMcpError(t *testing.T) {
	r := NewClaudeRunner(time.Minute, "", &fakeKnowledgeStore{})
	result, err := r.knowledgeGetHandler(context.Background(), map[string]any{"concept_id": "missing"})
	require.NoError(t, err)
	assert.True(t, result.IsError)
}

func TestClaudeRunner_ListModels_ReturnsEmptyNotError(t *testing.T) {
	r := NewClaudeRunner(time.Minute, "", nil)
	models, err := r.ListModels(context.Background())
	assert.NoError(t, err)
	assert.Nil(t, models)
}

func TestClaudeRunner_CloseSession_DisconnectsAndRemovesCachedClient(t *testing.T) {
	r := NewClaudeRunner(time.Minute, "", nil)
	client := &fakeClaudeClient{}
	r.clients["task-a:planning"] = client

	r.CloseSession("task-a:planning")

	assert.True(t, client.disconnected)
	_, ok := r.clients["task-a:planning"]
	assert.False(t, ok)
}

func TestClaudeRunner_CloseSession_OnUnknownKeyIsANoOp(t *testing.T) {
	r := NewClaudeRunner(time.Minute, "", nil)
	assert.NotPanics(t, func() { r.CloseSession("no-such-session") })
}

// fakeClaudeClient is a minimal claudecode.Client stub for exercising
// ClaudeRunner without a live subprocess. queryErr scripts Query's return
// value (e.g. a stale-pipe error); ReceiveMessages always returns a
// real, already-closed channel (not nil, which a for-range would block on
// forever) so Run's drain loop completes immediately once Query succeeds.
type fakeClaudeClient struct {
	disconnected bool
	queryErr     error
	queryCalls   int
}

func (f *fakeClaudeClient) Connect(context.Context, ...claudecode.StreamMessage) error { return nil }
func (f *fakeClaudeClient) Disconnect() error                                          { f.disconnected = true; return nil }
func (f *fakeClaudeClient) Query(context.Context, string) error {
	f.queryCalls++
	return f.queryErr
}
func (f *fakeClaudeClient) QueryWithSession(context.Context, string, string) error { return nil }
func (f *fakeClaudeClient) QueryStream(context.Context, <-chan claudecode.StreamMessage) error {
	return nil
}
func (f *fakeClaudeClient) ReceiveMessages(context.Context) <-chan claudecode.Message {
	ch := make(chan claudecode.Message)
	close(ch)
	return ch
}
func (f *fakeClaudeClient) ReceiveResponse(context.Context) claudecode.MessageIterator {
	return nil
}
func (f *fakeClaudeClient) Interrupt(context.Context) error { return nil }
func (f *fakeClaudeClient) SetModel(context.Context, *string) error { return nil }
func (f *fakeClaudeClient) SetPermissionMode(context.Context, claudecode.PermissionMode) error {
	return nil
}
func (f *fakeClaudeClient) RewindFiles(context.Context, string) error { return nil }
func (f *fakeClaudeClient) GetMcpStatus(context.Context) (*claudecode.McpStatusResponse, error) {
	return nil, nil
}
func (f *fakeClaudeClient) GetStreamIssues() []claudecode.StreamIssue { return nil }
func (f *fakeClaudeClient) GetStreamStats() claudecode.StreamStats    { return claudecode.StreamStats{} }
func (f *fakeClaudeClient) GetServerInfo(context.Context) (map[string]interface{}, error) {
	return nil, nil
}

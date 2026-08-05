package agentrunner

import (
	"context"
	"encoding/json"
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
	r := NewClaudeRunner(time.Minute, time.Minute, "", nil)

	assert.True(t, r.tryLock("task-a:planning"))
	assert.False(t, r.tryLock("task-a:planning"), "a second lock for the same key must be rejected")
	assert.True(t, r.tryLock("task-b:planning"), "a different key must not be blocked")

	r.unlock("task-a:planning")
	assert.True(t, r.tryLock("task-a:planning"), "unlocking must free the key for reuse")
}

// textDeltaEvent builds the content_block_delta StreamEvent a real `claude`
// CLI session emits for one chunk of streamed assistant text.
func textDeltaEvent(text string) *claudecode.StreamEvent {
	return &claudecode.StreamEvent{Event: map[string]any{
		"type":  claudecode.StreamEventTypeContentBlockDelta,
		"delta": map[string]any{"text": text},
	}}
}

// messageStartEvent builds the message_start StreamEvent that opens a new
// assistant-message round — always sent before that round's own text
// deltas (see assistantText.startNewRound).
func messageStartEvent() *claudecode.StreamEvent {
	return &claudecode.StreamEvent{Event: map[string]any{"type": claudecode.StreamEventTypeMessageStart}}
}

// toolResultMessage builds the UserMessage a real `claude` CLI session
// sends carrying one ToolResultBlock — the ack/reject for a Draft's
// ToolUseBlock (draftToolHandlerFor) or a genuine tool's result.
func toolResultMessage(id string, isError bool) *claudecode.UserMessage {
	return &claudecode.UserMessage{
		Content: []claudecode.ContentBlock{
			&claudecode.ToolResultBlock{ToolUseID: id, Content: "ok", IsError: &isError},
		},
	}
}

func TestProcessMessage_AccumulatesText(t *testing.T) {
	var content assistantText
	var out RunOutput

	done, err := processMessage(textDeltaEvent("hello "), []string{"propose_plan"}, &content, &out, nil, nil)
	require.NoError(t, err)
	assert.False(t, done)
	assert.Equal(t, "hello ", content.String())
}

func TestProcessMessage_SeparatesTextFromDistinctAssistantMessages(t *testing.T) {
	var content assistantText
	var out RunOutput

	_, err := processMessage(messageStartEvent(), nil, &content, &out, nil, nil)
	require.NoError(t, err)
	_, err = processMessage(textDeltaEvent("Good — this is a clean extraction."), nil, &content, &out, nil, nil)
	require.NoError(t, err)

	_, err = processMessage(messageStartEvent(), nil, &content, &out, nil, nil)
	require.NoError(t, err)
	_, err = processMessage(textDeltaEvent("This looks like a faithful mechanical extraction."), nil, &content, &out, nil, nil)
	require.NoError(t, err)

	assert.Equal(t, "Good — this is a clean extraction.\n\nThis looks like a faithful mechanical extraction.", content.String())
}

// TestProcessMessage_DoesNotSeparateConsecutiveDeltasWithinSameRound locks
// in the other half of the boundary rule: two text deltas with no
// intervening message_start belong to the same round and must concatenate
// directly, with no paragraph break inserted between them.
func TestProcessMessage_DoesNotSeparateConsecutiveDeltasWithinSameRound(t *testing.T) {
	var content assistantText
	var out RunOutput

	_, err := processMessage(textDeltaEvent("hello "), nil, &content, &out, nil, nil)
	require.NoError(t, err)
	_, err = processMessage(textDeltaEvent("world"), nil, &content, &out, nil, nil)
	require.NoError(t, err)

	assert.Equal(t, "hello world", content.String())
}

// TestProcessMessage_CapturesMatchingToolCall covers the full two-message
// round trip: a Draft ToolUseBlock alone is only a candidate
// (draftToolHandlerFor may still reject it) — out.ToolCall is only set once
// its matching non-error ToolResultBlock arrives.
func TestProcessMessage_CapturesMatchingToolCall(t *testing.T) {
	var content assistantText
	var out RunOutput
	hooks := &toolActivityHooks{pending: make(pendingToolCalls)}

	msg := &claudecode.AssistantMessage{
		Content: []claudecode.ContentBlock{&claudecode.ToolUseBlock{
			ToolUseID: "call-1",
			Name:      "mcp__draft__propose_plan",
			Input:     map[string]any{"approach": "do it"},
		}},
	}
	done, err := processMessage(msg, []string{"propose_plan"}, &content, &out, nil, hooks)
	require.NoError(t, err)
	assert.False(t, done)
	assert.Nil(t, out.ToolCall, "not trusted until its ToolResultBlock confirms acceptance")

	done, err = processMessage(toolResultMessage("call-1", false), []string{"propose_plan"}, &content, &out, nil, hooks)
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

// TestProcessMessage_RejectedDraftDoesNotBlockLaterRetry locks in the fix
// for the "latch onto the first call" bug: a Draft call that
// draftToolHandlerFor rejected (IsError true, e.g. a schema-invalid
// propose_plan) must not prevent a later, valid retry from being captured.
func TestProcessMessage_RejectedDraftDoesNotBlockLaterRetry(t *testing.T) {
	var content assistantText
	var out RunOutput
	hooks := &toolActivityHooks{pending: make(pendingToolCalls)}
	toolNames := []string{"propose_plan"}

	first := &claudecode.AssistantMessage{
		Content: []claudecode.ContentBlock{&claudecode.ToolUseBlock{
			ToolUseID: "call-1", Name: "mcp__draft__propose_plan", Input: map[string]any{"approach": "missing steps"},
		}},
	}
	_, err := processMessage(first, toolNames, &content, &out, nil, hooks)
	require.NoError(t, err)
	_, err = processMessage(toolResultMessage("call-1", true), toolNames, &content, &out, nil, hooks)
	require.NoError(t, err)
	assert.Nil(t, out.ToolCall, "a rejected call must never populate out.ToolCall")

	retry := &claudecode.AssistantMessage{
		Content: []claudecode.ContentBlock{&claudecode.ToolUseBlock{
			ToolUseID: "call-2", Name: "mcp__draft__propose_plan", Input: map[string]any{"approach": "fixed", "steps": []any{"a"}},
		}},
	}
	_, err = processMessage(retry, toolNames, &content, &out, nil, hooks)
	require.NoError(t, err)
	_, err = processMessage(toolResultMessage("call-2", false), toolNames, &content, &out, nil, hooks)
	require.NoError(t, err)
	require.NotNil(t, out.ToolCall, "a later valid retry must still be captured")
	assert.Equal(t, "call-2", out.ToolCall.ID)
}

func TestProcessMessage_IgnoresNonMatchingToolCall(t *testing.T) {
	var content assistantText
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
	var content assistantText
	var out RunOutput
	var calls []string
	hooks := &toolActivityHooks{
		onCall:  func(id, name, argsJSON string) { calls = append(calls, name+":"+argsJSON) },
		pending: make(pendingToolCalls),
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
	var content assistantText
	var out RunOutput
	var calls int
	hooks := &toolActivityHooks{onCall: func(string, string, string) { calls++ }, pending: make(pendingToolCalls)}

	msg := &claudecode.AssistantMessage{
		Content: []claudecode.ContentBlock{&claudecode.ToolUseBlock{
			ToolUseID: "call-1", Name: "mcp__draft__propose_plan", Input: map[string]any{},
		}},
	}
	_, err := processMessage(msg, []string{"propose_plan"}, &content, &out, nil, hooks)
	require.NoError(t, err)
	_, err = processMessage(toolResultMessage("call-1", false), []string{"propose_plan"}, &content, &out, nil, hooks)
	require.NoError(t, err)
	require.NotNil(t, out.ToolCall)
	assert.Zero(t, calls, "the Draft's own proposal call must never also surface as tool activity")
	assert.Empty(t, hooks.pending, "a Draft call must not be tracked in the intermediate-activity pending map")
}

// TestProcessMessage_ForwardsToolResultCorrelatedByID covers the other half:
// the claude CLI reports a ToolResultBlock in a later UserMessage carrying
// only a toolUseID, not the tool's name — hooks.pending (populated when the
// matching ToolUseBlock was seen) is what lets onResult still report which
// tool this result belongs to.
func TestProcessMessage_ForwardsToolResultCorrelatedByID(t *testing.T) {
	var content assistantText
	var out RunOutput
	type result struct {
		id      string
		name    string
		text    string
		isError bool
	}
	var results []result
	hooks := &toolActivityHooks{
		onResult: func(id, name, text string, isError bool) { results = append(results, result{id, name, text, isError}) },
		pending:  pendingToolCalls{"call-1": "Grep"},
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
	assert.Equal(t, result{"call-1", "Grep", "no matches", true}, results[0])
	assert.Empty(t, hooks.pending, "a correlated result must be popped from pending, not left to leak")
}

// TestProcessMessage_ThinkingBlockForwardsAsReasoningDelta locks in the
// docs/adr/0018 forward-compatible fix: a ThinkingBlock (only ever present
// once extended thinking is enabled — not done anywhere in this codebase
// today) must feed onDelta's ReasoningContent, the same field/UI path the
// OpenAI-SDK executors' reasoning_content already uses, rather than being
// silently dropped by processMessage's switch.
func TestProcessMessage_ThinkingBlockForwardsAsReasoningDelta(t *testing.T) {
	var content assistantText
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
	var content assistantText
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
	var content assistantText
	var out RunOutput
	hooks := &toolActivityHooks{pending: make(pendingToolCalls)}
	toolNames := []string{"propose_review", "propose_knowledge"}

	msg := &claudecode.AssistantMessage{
		Content: []claudecode.ContentBlock{&claudecode.ToolUseBlock{
			ToolUseID: "call-1",
			Name:      "mcp__draft__propose_knowledge",
			Input:     map[string]any{"concept_id": "x"},
		}},
	}
	done, err := processMessage(msg, toolNames, &content, &out, nil, hooks)
	require.NoError(t, err)
	assert.False(t, done)
	done, err = processMessage(toolResultMessage("call-1", false), toolNames, &content, &out, nil, hooks)
	require.NoError(t, err)
	assert.False(t, done)
	require.NotNil(t, out.ToolCall)
	assert.Equal(t, "propose_knowledge", out.ToolCall.Function.Name)
}

// TestProcessMessage_CapturesEveryDraftToolCallInATurn covers the bug this
// fixes: previously only the first draft resolved in a turn survived into
// RunOutput at all — a model calling both propose_review and
// propose_knowledge in the same turn (Review offers both at once,
// stageTool) silently lost the second one entirely, with no trace of it in
// the persisted conversation. Now every accepted draft call in the turn is
// captured, in order, into out.ToolCalls; out.ToolCall stays the first, for
// every single-draft-per-turn caller (Requirements/Planning/TaskDraft).
func TestProcessMessage_CapturesEveryDraftToolCallInATurn(t *testing.T) {
	var content assistantText
	var out RunOutput
	hooks := &toolActivityHooks{pending: make(pendingToolCalls)}
	toolNames := []string{"propose_review", "propose_knowledge"}

	msg := &claudecode.AssistantMessage{
		Content: []claudecode.ContentBlock{
			&claudecode.ToolUseBlock{ToolUseID: "call-1", Name: "mcp__draft__propose_review", Input: map[string]any{"decision": "needs_changes"}},
			&claudecode.ToolUseBlock{ToolUseID: "call-2", Name: "mcp__draft__propose_knowledge", Input: map[string]any{"concept_id": "x"}},
		},
	}
	_, err := processMessage(msg, toolNames, &content, &out, nil, hooks)
	require.NoError(t, err)
	_, err = processMessage(toolResultMessage("call-1", false), toolNames, &content, &out, nil, hooks)
	require.NoError(t, err)
	_, err = processMessage(toolResultMessage("call-2", false), toolNames, &content, &out, nil, hooks)
	require.NoError(t, err)

	require.Len(t, out.ToolCalls, 2)
	assert.Equal(t, "propose_review", out.ToolCalls[0].Function.Name)
	assert.Equal(t, "propose_knowledge", out.ToolCalls[1].Function.Name)
	require.NotNil(t, out.ToolCall)
	assert.Equal(t, "propose_review", out.ToolCall.Function.Name, "ToolCall stays the first, for single-draft callers")
}

// TestProcessMessage_KeepsFirstValidToolCallOnly covers the same "keep
// first" precedence as before, now expressed over accepted (not merely
// seen) calls: a second Draft call's ToolResultBlock must not overwrite an
// out.ToolCall a prior accepted call already set.
func TestProcessMessage_KeepsFirstValidToolCallOnly(t *testing.T) {
	var content assistantText
	out := RunOutput{ToolCall: &chat.ToolCall{ID: "first", Type: "function"}}
	hooks := &toolActivityHooks{pending: make(pendingToolCalls)}
	toolNames := []string{"propose_plan"}

	msg := &claudecode.AssistantMessage{
		Content: []claudecode.ContentBlock{&claudecode.ToolUseBlock{
			ToolUseID: "second",
			Name:      "mcp__draft__propose_plan",
			Input:     map[string]any{},
		}},
	}
	_, err := processMessage(msg, toolNames, &content, &out, nil, hooks)
	require.NoError(t, err)
	_, err = processMessage(toolResultMessage("second", false), toolNames, &content, &out, nil, hooks)
	require.NoError(t, err)
	assert.Equal(t, "first", out.ToolCall.ID)
}

func TestDraftToolHandlerFor_RejectsSchemaInvalidArgs(t *testing.T) {
	handler := draftToolHandlerFor("propose_plan")
	result, err := handler(context.Background(), map[string]any{"approach": "no steps here"})
	require.NoError(t, err)
	require.NotNil(t, result)
	assert.True(t, result.IsError)
}

func TestDraftToolHandlerFor_AcksValidArgs(t *testing.T) {
	handler := draftToolHandlerFor("propose_plan")
	result, err := handler(context.Background(), map[string]any{
		"approach": "do it", "steps": []any{"a"}, "estimated_complexity": "low",
	})
	require.NoError(t, err)
	require.NotNil(t, result)
	assert.False(t, result.IsError)
}

func TestDraftToolHandlerFor_UnknownNameFailsOpen(t *testing.T) {
	handler := draftToolHandlerFor("not_a_real_tool")
	result, err := handler(context.Background(), map[string]any{"anything": "goes"})
	require.NoError(t, err)
	require.NotNil(t, result)
	assert.False(t, result.IsError)
}

func TestProcessMessage_ResultMessageEndsTurnSuccessfully(t *testing.T) {
	var content assistantText
	content.WriteString("final text")
	var out RunOutput

	done, err := processMessage(&claudecode.ResultMessage{IsError: false}, []string{"propose_plan"}, &content, &out, nil, nil)
	require.NoError(t, err)
	assert.True(t, done)
	assert.Equal(t, "final text", out.Content)
}

func TestProcessMessage_ResultMessageReportsError(t *testing.T) {
	var content assistantText
	var out RunOutput

	done, err := processMessage(&claudecode.ResultMessage{IsError: true, Errors: []string{"boom"}}, []string{"propose_plan"}, &content, &out, nil, nil)
	assert.True(t, done)
	require.Error(t, err)
	assert.Contains(t, err.Error(), "boom")
}

func TestProcessMessage_ResultMessageErrorFallsBackToResultWhenErrorsEmpty(t *testing.T) {
	var content assistantText
	var out RunOutput
	result := "max turns exceeded"

	done, err := processMessage(&claudecode.ResultMessage{IsError: true, Result: &result}, []string{"propose_plan"}, &content, &out, nil, nil)
	assert.True(t, done)
	require.Error(t, err)
	assert.Contains(t, err.Error(), "max turns exceeded")
}

func TestProcessMessage_ResultMessageErrorFallsBackToSubtypeWhenErrorsAndResultEmpty(t *testing.T) {
	var content assistantText
	var out RunOutput

	done, err := processMessage(&claudecode.ResultMessage{IsError: true, Subtype: "error_max_turns"}, []string{"propose_plan"}, &content, &out, nil, nil)
	assert.True(t, done)
	require.Error(t, err)
	assert.Contains(t, err.Error(), "error_max_turns")
}

func TestProcessMessage_StreamDeltaInvokesOnDelta(t *testing.T) {
	var content assistantText
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
	var content assistantText
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
	var content assistantText
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
	var content assistantText
	var out ExecuteOutput

	done, err := processExecuteMessage(textDeltaEvent("implementing... "), &content, &out, nil, nil)
	require.NoError(t, err)
	assert.False(t, done)
	assert.Equal(t, "implementing... ", content.String())
}

func TestProcessExecuteMessage_SeparatesTextFromDistinctAssistantMessages(t *testing.T) {
	var content assistantText
	var out ExecuteOutput

	_, err := processExecuteMessage(messageStartEvent(), &content, &out, nil, nil)
	require.NoError(t, err)
	_, err = processExecuteMessage(textDeltaEvent("Running the test suite first."), &content, &out, nil, nil)
	require.NoError(t, err)

	_, err = processExecuteMessage(messageStartEvent(), &content, &out, nil, nil)
	require.NoError(t, err)
	_, err = processExecuteMessage(textDeltaEvent("Tests pass, moving on."), &content, &out, nil, nil)
	require.NoError(t, err)

	assert.Equal(t, "Running the test suite first.\n\nTests pass, moving on.", content.String())
}

// TestProcessExecuteMessage_EmitsEveryToolCall locks in the behavior that
// makes Execute different from Run's processMessage: every ToolUseBlock
// becomes a tool_call event, not just one matching a registered Draft
// tool — Execute has no Draft tool at all, and dropping Write/Edit/Bash
// calls would defeat the point of streaming a live execution trace.
func TestProcessExecuteMessage_EmitsEveryToolCall(t *testing.T) {
	var content assistantText
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
	}, nil)
	require.NoError(t, err)
	assert.False(t, done)
	require.Len(t, events, 2)
	assert.Equal(t, "tool_call", events[0].Kind)
	assert.Equal(t, "Write", events[0].ToolName)
	assert.JSONEq(t, `{"path":"a.go"}`, events[0].ToolInput)
	assert.Equal(t, "Bash", events[1].ToolName)
}

func TestProcessExecuteMessage_EmitsToolResultFromUserMessage(t *testing.T) {
	var content assistantText
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
	}, nil)
	require.NoError(t, err)
	assert.False(t, done)
	require.Len(t, events, 1)
	assert.Equal(t, "tool_result", events[0].Kind)
	assert.Equal(t, "test failed: exit status 1", events[0].ToolResult)
	assert.True(t, events[0].IsError)
}

func TestProcessExecuteMessage_ToolResultDefaultsNotErrorWhenNil(t *testing.T) {
	var content assistantText
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
	}, nil)
	require.NoError(t, err)
	require.Len(t, events, 1)
	assert.False(t, events[0].IsError)
}

func TestProcessExecuteMessage_ResultMessagePopulatesRealMetrics(t *testing.T) {
	var content assistantText
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
	done, err := processExecuteMessage(msg, &content, &out, nil, nil)
	require.NoError(t, err)
	assert.True(t, done)
	assert.Equal(t, "done implementing", out.Content)
	assert.Equal(t, 2.5, out.DurationSeconds)
	assert.Equal(t, 7, out.NumTurns)
	assert.Equal(t, 0.0421, out.CostEstimate)
	assert.Equal(t, 165, out.TokensUsed)
}

func TestProcessExecuteMessage_ResultMessageLeavesMetricsZeroWhenUnavailable(t *testing.T) {
	var content assistantText
	var out ExecuteOutput

	done, err := processExecuteMessage(&claudecode.ResultMessage{DurationMs: 1000}, &content, &out, nil, nil)
	require.NoError(t, err)
	assert.True(t, done)
	assert.Equal(t, 0, out.TokensUsed)
	assert.Equal(t, 0.0, out.CostEstimate)
}

func TestProcessExecuteMessage_ResultMessageReportsError(t *testing.T) {
	var content assistantText
	var out ExecuteOutput

	done, err := processExecuteMessage(&claudecode.ResultMessage{IsError: true, Errors: []string{"boom"}}, &content, &out, nil, nil)
	assert.True(t, done)
	require.Error(t, err)
	assert.Contains(t, err.Error(), "boom")
}

func TestProcessExecuteMessage_StreamDeltaEmitsTextEvent(t *testing.T) {
	var content assistantText
	var out ExecuteOutput
	var events []ExecuteEvent

	msg := &claudecode.StreamEvent{Event: map[string]any{
		"type":  claudecode.StreamEventTypeContentBlockDelta,
		"delta": map[string]any{"text": "chunk"},
	}}
	done, err := processExecuteMessage(msg, &content, &out, func(e ExecuteEvent) error {
		events = append(events, e)
		return nil
	}, nil)
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
	var content assistantText
	var out ExecuteOutput
	var events []ExecuteEvent

	msg := &claudecode.AssistantMessage{
		Content: []claudecode.ContentBlock{&claudecode.ThinkingBlock{Thinking: "weighing approaches..."}},
	}
	done, err := processExecuteMessage(msg, &content, &out, func(e ExecuteEvent) error {
		events = append(events, e)
		return nil
	}, nil)
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

func TestClaudeRunner_RunTimeout_UsesExecuteTimeoutWhenBashEnabled(t *testing.T) {
	r := NewClaudeRunner(5*time.Minute, 30*time.Minute, "", nil)
	assert.Equal(t, 30*time.Minute, r.runTimeout(RunInput{EnableBashTool: true}))
}

func TestClaudeRunner_RunTimeout_UsesRunTimeoutWhenBashDisabled(t *testing.T) {
	r := NewClaudeRunner(5*time.Minute, 30*time.Minute, "", nil)
	assert.Equal(t, 5*time.Minute, r.runTimeout(RunInput{EnableBashTool: false}))
}

func TestClaudeRunner_CheckHealth_FailsWhenReposRootEmpty(t *testing.T) {
	r := NewClaudeRunner(time.Minute, time.Minute, "", nil)
	err := r.CheckHealth(context.Background())
	require.Error(t, err)
	assert.Contains(t, err.Error(), "REPOS_ROOT")
}

func TestClaudeRunner_CheckHealth_FailsWhenCLINotOnPath(t *testing.T) {
	original := lookPath
	lookPath = func(string) (string, error) { return "", errors.New("not found") }
	defer func() { lookPath = original }()

	r := NewClaudeRunner(time.Minute, time.Minute, t.TempDir(), nil)
	err := r.CheckHealth(context.Background())
	require.Error(t, err)
	assert.Contains(t, err.Error(), "claude CLI not found on PATH")
}

func TestClaudeRunner_CheckHealth_OKWhenReposRootSetAndCLIFound(t *testing.T) {
	original := lookPath
	lookPath = func(string) (string, error) { return "/usr/bin/claude", nil }
	defer func() { lookPath = original }()

	r := NewClaudeRunner(time.Minute, time.Minute, t.TempDir(), nil)
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

func TestSystemPromptWithHistory_TruncatesOldestWhenOverBudget(t *testing.T) {
	huge := strings.Repeat("x", maxHistoryReplayBytes)
	got := systemPromptWithHistory("be nice", []chat.Message{
		{Role: "user", Content: "oldest turn, should be dropped"},
		{Role: "assistant", Content: huge},
	})
	assert.Contains(t, got, "earliest turns omitted")
	assert.NotContains(t, got, "oldest turn, should be dropped")
	assert.Contains(t, got, huge)
	assert.Less(t, len(got), len("be nice")+len(huge)+len("oldest turn, should be dropped")+200)
}

func TestSystemPromptWithHistory_NeverDropsTheOnlyMessageEvenIfOversized(t *testing.T) {
	huge := strings.Repeat("x", maxHistoryReplayBytes*2)
	got := systemPromptWithHistory("be nice", []chat.Message{
		{Role: "assistant", Content: huge},
	})
	assert.Contains(t, got, huge)
	assert.NotContains(t, got, "earliest turns omitted")
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
	r := NewClaudeRunner(time.Minute, time.Minute, "", nil)
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
	r := NewClaudeRunner(time.Minute, time.Minute, "", nil)
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
	r := NewClaudeRunner(time.Minute, time.Minute, "", nil)
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
	r := NewClaudeRunner(time.Minute, time.Minute, "", nil)
	_, err := r.clientFor(context.Background(), "task-a:requirements", RunInput{Workspace: ""})
	require.Error(t, err)
	assert.Contains(t, err.Error(), "claude-code requires a project repository")
}

// TestClaudeRunner_ClientFor_ResumesRealSessionWhenIDSet locks in the
// resume-first contract: when in.ResumeSessionID is set and no client is
// cached, clientFor attempts claudecode.WithResume(id) — not
// systemPromptWithHistory's rendered-transcript replay, even though History
// is also populated (a caller always supplies both; resume wins when it
// succeeds).
func TestClaudeRunner_ClientFor_ResumesRealSessionWhenIDSet(t *testing.T) {
	r := NewClaudeRunner(time.Minute, time.Minute, "", nil)

	var captured claudecode.Options
	fake := &fakeClaudeClient{}
	r.newClient = func(opts ...claudecode.Option) claudecode.Client {
		captured = captureOptions(opts...)
		return fake
	}

	client, err := r.clientFor(context.Background(), "task-a:requirements", RunInput{
		Workspace:       t.TempDir(),
		SystemPrompt:    "base prompt",
		ResumeSessionID: "sess-123",
		History:         []chat.Message{{Role: "user", Content: "earlier turn"}},
	})
	require.NoError(t, err)
	assert.Same(t, fake, client)
	require.NotNil(t, captured.Resume)
	assert.Equal(t, "sess-123", *captured.Resume)
	require.NotNil(t, captured.AppendSystemPrompt)
	assert.Equal(t, "base prompt", *captured.AppendSystemPrompt, "a resumed session must not also get history rendered into its system prompt")
}

// TestClaudeRunner_ClientFor_FallsBackAfterSessionNotFound locks in the
// same-turn fallback: a resume attempt failing with a "not found"-shaped
// error must not fail the turn — clientFor retries against a fresh,
// non-resumed client using systemPromptWithHistory's replay instead, and
// that fresh client becomes the one cached/returned.
func TestClaudeRunner_ClientFor_FallsBackAfterSessionNotFound(t *testing.T) {
	r := NewClaudeRunner(time.Minute, time.Minute, "", nil)

	failed := &fakeClaudeClient{connectErr: errors.New("No conversation found with session ID: sess-stale")}
	var fallbackOpts claudecode.Options
	fresh := &fakeClaudeClient{}
	calls := 0
	r.newClient = func(opts ...claudecode.Option) claudecode.Client {
		calls++
		if calls == 1 {
			return failed
		}
		fallbackOpts = captureOptions(opts...)
		return fresh
	}

	client, err := r.clientFor(context.Background(), "task-a:requirements", RunInput{
		Workspace:       t.TempDir(),
		SystemPrompt:    "base prompt",
		ResumeSessionID: "sess-stale",
		History:         []chat.Message{{Role: "user", Content: "earlier turn"}},
	})
	require.NoError(t, err)
	assert.Same(t, fresh, client)
	assert.Equal(t, 2, calls, "must attempt exactly one fallback connect, not loop")
	assert.Nil(t, fallbackOpts.Resume, "the fallback connect must not itself pass WithResume")
	require.NotNil(t, fallbackOpts.AppendSystemPrompt)
	assert.Contains(t, *fallbackOpts.AppendSystemPrompt, "earlier turn", "the fallback connect must replay history into the system prompt")

	r.mu.Lock()
	cached := r.clients["task-a:requirements"]
	r.mu.Unlock()
	assert.Same(t, fresh, cached, "the fresh fallback client, not the failed one, must be cached")
}

// TestClaudeRunner_ClientFor_PropagatesNonNotFoundConnectError locks in
// that only a "not found"-shaped resume failure triggers the fallback
// retry — a genuine failure (auth, rate limit, ...) must propagate
// directly, with no second connect attempt.
func TestClaudeRunner_ClientFor_PropagatesNonNotFoundConnectError(t *testing.T) {
	r := NewClaudeRunner(time.Minute, time.Minute, "", nil)

	failed := &fakeClaudeClient{connectErr: errors.New("authentication failed")}
	calls := 0
	r.newClient = func(...claudecode.Option) claudecode.Client {
		calls++
		return failed
	}

	_, err := r.clientFor(context.Background(), "task-a:requirements", RunInput{
		Workspace:       t.TempDir(),
		ResumeSessionID: "sess-123",
	})
	require.Error(t, err)
	assert.Contains(t, err.Error(), "authentication failed")
	assert.Equal(t, 1, calls, "a non-'not found' connect error must not trigger a fallback retry")
}

// TestClaudeRunner_ClientFor_NoResumeIDUsesHistoryReplayDirectly locks in
// that the pre-existing behavior (no ResumeSessionID at all) is unchanged:
// a single connect, straight to systemPromptWithHistory, no resume attempt.
func TestClaudeRunner_ClientFor_NoResumeIDUsesHistoryReplayDirectly(t *testing.T) {
	r := NewClaudeRunner(time.Minute, time.Minute, "", nil)

	var captured claudecode.Options
	calls := 0
	fake := &fakeClaudeClient{}
	r.newClient = func(opts ...claudecode.Option) claudecode.Client {
		calls++
		captured = captureOptions(opts...)
		return fake
	}

	_, err := r.clientFor(context.Background(), "task-a:requirements", RunInput{
		Workspace:    t.TempDir(),
		SystemPrompt: "base prompt",
		History:      []chat.Message{{Role: "user", Content: "earlier turn"}},
	})
	require.NoError(t, err)
	assert.Equal(t, 1, calls)
	assert.Nil(t, captured.Resume)
	require.NotNil(t, captured.AppendSystemPrompt)
	assert.Contains(t, *captured.AppendSystemPrompt, "earlier turn")
}

func TestIsSessionNotFoundError_MatchesKnownPatterns(t *testing.T) {
	for _, msg := range []string{
		"No conversation found with session ID: abc-123",
		"session not found",
		"Session ID not found",
		"unknown session",
		"no session found for id",
	} {
		assert.True(t, isSessionNotFoundError(errors.New(msg)), "expected %q to match", msg)
	}
}

func TestIsSessionNotFoundError_IgnoresUnrelatedErrors(t *testing.T) {
	assert.False(t, isSessionNotFoundError(errors.New("authentication failed")))
	assert.False(t, isSessionNotFoundError(errors.New("rate limit exceeded")))
}

// TestProcessMessage_CapturesSessionIDFromResultMessage locks in that
// out.SessionID is populated from every ResultMessage regardless of
// whether the turn resumed a real session or fell back to a fresh one —
// the caller persists this unconditionally as the SessionKey's next
// ResumeSessionID.
func TestProcessMessage_CapturesSessionIDFromResultMessage(t *testing.T) {
	var content assistantText
	var out RunOutput

	done, err := processMessage(&claudecode.ResultMessage{SessionID: "sess-abc"}, nil, &content, &out, nil, nil)
	require.NoError(t, err)
	assert.True(t, done)
	assert.Equal(t, "sess-abc", out.SessionID)
}

// captureOptions applies opts to a fresh claudecode.Options so a test can
// inspect exactly what a ClaudeRunner call configured — the same pattern
// docs/adr/0022's fix depends on: WithTools must be set, not just
// WithAllowedTools, or the CLI's full default tool surface stays reachable.
func captureOptions(opts ...claudecode.Option) claudecode.Options {
	var o claudecode.Options
	for _, opt := range opts {
		opt(&o)
	}
	return o
}

// TestClaudeRunner_ClientFor_SetsToolsAlongsideAllowedTools locks in
// docs/adr/0022's fix: WithAllowedTools alone (the CLI's --allowed-tools,
// a permission auto-approve list) doesn't restrict which tools the model
// can see — WithTools (--tools) is the actual gate, and clientFor must set
// both to the same readOnlyTools set for a plain Requirements/Planning
// session (no Bash, no Draft/knowledge tools registered).
func TestClaudeRunner_ClientFor_SetsToolsAlongsideAllowedTools(t *testing.T) {
	r := NewClaudeRunner(time.Minute, time.Minute, "", nil)

	var captured claudecode.Options
	r.newClient = func(opts ...claudecode.Option) claudecode.Client {
		captured = captureOptions(opts...)
		return &fakeClaudeClient{}
	}

	_, err := r.clientFor(context.Background(), "task-a:requirements", RunInput{Workspace: t.TempDir()})
	require.NoError(t, err)

	assert.Equal(t, []string{"Read", "Grep", "Glob", "WebFetch", "WebSearch"}, captured.Tools)
	assert.Equal(t, []string{"Read", "Grep", "Glob", "WebFetch", "WebSearch"}, captured.AllowedTools)
}

// TestClaudeRunner_ClientFor_ToolsExcludesMcpQualifiedNames locks in that
// WithTools only ever receives built-in tool names — the CLI's --tools
// --help example is "Bash,Edit,Read", unlike --allowed-tools/
// --disallowed-tools which claude_runner.go already treats as
// MCP-qualified-name-aware (mcp__<server>__<tool>). Passing an MCP name to
// --tools would either error or silently fail to enable the Draft/
// knowledge tools, so this must stay in AllowedTools only.
func TestClaudeRunner_ClientFor_ToolsExcludesMcpQualifiedNames(t *testing.T) {
	r := NewClaudeRunner(time.Minute, time.Minute, "", nil)

	var captured claudecode.Options
	r.newClient = func(opts ...claudecode.Option) claudecode.Client {
		captured = captureOptions(opts...)
		return &fakeClaudeClient{}
	}

	in := RunInput{
		Workspace:      t.TempDir(),
		EnableBashTool: true,
		Tools: []chat.Tool{{
			Type: "function",
			Function: chat.ToolSchema{
				Name:       "propose_context",
				Parameters: json.RawMessage(`{"type":"object"}`),
			},
		}},
	}
	_, err := r.clientFor(context.Background(), "task-a:review", in)
	require.NoError(t, err)

	assert.Equal(t, []string{"Read", "Grep", "Glob", "WebFetch", "WebSearch", "Bash"}, captured.Tools,
		"WithTools must stay built-in-only even when Draft MCP tools are registered")
	assert.Contains(t, captured.AllowedTools, mcpQualifiedName("propose_context"),
		"the MCP-qualified name still belongs in AllowedTools/--allowed-tools")
	assert.NotContains(t, captured.Tools, mcpQualifiedName("propose_context"))
}

// TestClaudeRunner_Execute_SetsToolsAlongsideAllowedTools mirrors the
// clientFor test for Execute's independent opts construction (docs/adr/0022
// applies the same fix to both call sites).
func TestClaudeRunner_Execute_SetsToolsAlongsideAllowedTools(t *testing.T) {
	r := NewClaudeRunner(time.Minute, time.Minute, "", nil)

	var captured claudecode.Options
	r.newClient = func(opts ...claudecode.Option) claudecode.Client {
		captured = captureOptions(opts...)
		return &fakeClaudeClient{}
	}

	_, err := r.Execute(context.Background(), ExecuteInput{
		SessionKey: "task-a:execute",
		Workspace:  t.TempDir(),
	}, nil)
	require.NoError(t, err)

	assert.Equal(t, executionTools, captured.Tools)
	assert.Equal(t, executionTools, captured.AllowedTools)
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
	r := NewClaudeRunner(time.Minute, time.Minute, "", &fakeKnowledgeStore{summaries: []knowledge.ConceptSummary{
		{ConceptID: "coding-standards/logging", Type: "Coding Standard"},
	}})
	result, err := r.knowledgeListHandler(context.Background(), nil)
	require.NoError(t, err)
	require.False(t, result.IsError)
	require.Len(t, result.Content, 1)
	assert.Contains(t, result.Content[0].Text, "coding-standards/logging")
}

func TestClaudeRunner_KnowledgeListHandler_StoreErrorSurfacesAsMcpError(t *testing.T) {
	r := NewClaudeRunner(time.Minute, time.Minute, "", &fakeKnowledgeStore{listErr: errors.New("disk on fire")})
	result, err := r.knowledgeListHandler(context.Background(), nil)
	require.NoError(t, err, "a store failure must surface as an MCP error result, not a Go error")
	assert.True(t, result.IsError)
	assert.Contains(t, result.Content[0].Text, "disk on fire")
}

func TestClaudeRunner_KnowledgeGetHandler_ReturnsRealContent(t *testing.T) {
	r := NewClaudeRunner(time.Minute, time.Minute, "", &fakeKnowledgeStore{concepts: map[string]knowledge.Concept{
		"a": {Type: "Reference", Body: "hello\n"},
	}})
	result, err := r.knowledgeGetHandler(context.Background(), map[string]any{"concept_id": "a"})
	require.NoError(t, err)
	require.False(t, result.IsError)
	assert.Contains(t, result.Content[0].Text, "hello")
}

func TestClaudeRunner_KnowledgeGetHandler_MissingConceptSurfacesAsMcpError(t *testing.T) {
	r := NewClaudeRunner(time.Minute, time.Minute, "", &fakeKnowledgeStore{})
	result, err := r.knowledgeGetHandler(context.Background(), map[string]any{"concept_id": "missing"})
	require.NoError(t, err)
	assert.True(t, result.IsError)
}

func TestClaudeRunner_ListModels_ReturnsAdvertisedModels(t *testing.T) {
	r := NewClaudeRunner(time.Minute, time.Minute, "", nil)
	models, err := r.ListModels(context.Background())
	assert.NoError(t, err)
	assert.Equal(t, []string{"sonnet", "opus", "haiku"}, models)
}

func TestClaudeRunner_CloseSession_DisconnectsAndRemovesCachedClient(t *testing.T) {
	r := NewClaudeRunner(time.Minute, time.Minute, "", nil)
	client := &fakeClaudeClient{}
	r.clients["task-a:planning"] = client

	r.CloseSession("task-a:planning")

	assert.True(t, client.disconnected)
	_, ok := r.clients["task-a:planning"]
	assert.False(t, ok)
}

func TestClaudeRunner_CloseSession_OnUnknownKeyIsANoOp(t *testing.T) {
	r := NewClaudeRunner(time.Minute, time.Minute, "", nil)
	assert.NotPanics(t, func() { r.CloseSession("no-such-session") })
}

func TestClaudeRunner_CloseAll_DisconnectsEveryCachedClientAndClearsCache(t *testing.T) {
	r := NewClaudeRunner(time.Minute, time.Minute, "", nil)
	clientA := &fakeClaudeClient{}
	clientB := &fakeClaudeClient{}
	r.clients["task-a:planning"] = clientA
	r.clients["task-b:review"] = clientB

	r.CloseAll()

	assert.True(t, clientA.disconnected)
	assert.True(t, clientB.disconnected)
	assert.Empty(t, r.clients)
}

func TestClaudeRunner_CloseAll_OnEmptyCacheIsANoOp(t *testing.T) {
	r := NewClaudeRunner(time.Minute, time.Minute, "", nil)
	assert.NotPanics(t, func() { r.CloseAll() })
}

// fakeClaudeClient is a minimal claudecode.Client stub for exercising
// ClaudeRunner without a live subprocess. queryErr scripts Query's return
// value (e.g. a stale-pipe error); connectErr scripts Connect's return
// value (e.g. a "no conversation found" resume failure); ReceiveMessages
// always returns a real, already-closed channel (not nil, which a
// for-range would block on forever) so Run's drain loop completes
// immediately once Query succeeds.
type fakeClaudeClient struct {
	disconnected bool
	connectErr   error
	queryErr     error
	queryCalls   int
}

func (f *fakeClaudeClient) Connect(context.Context, ...claudecode.StreamMessage) error {
	return f.connectErr
}
func (f *fakeClaudeClient) Disconnect() error { f.disconnected = true; return nil }
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
func (f *fakeClaudeClient) Interrupt(context.Context) error         { return nil }
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

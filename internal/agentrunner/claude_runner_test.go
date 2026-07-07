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
)

func TestClaudeRunner_TryLockRejectsConcurrentSameKey(t *testing.T) {
	r := NewClaudeRunner(time.Minute, "")

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
	done, err := processMessage(msg, "propose_plan", &content, &out, nil)
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
	done, err := processMessage(msg, "propose_plan", &content, &out, nil)
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
	_, err := processMessage(msg, "propose_plan", &content, &out, nil)
	require.NoError(t, err)
	assert.Nil(t, out.ToolCall)
}

// TestProcessMessage_RequiresFullyQualifiedMcpName locks in a real bug
// caught during live e2e verification: the `claude` CLI reports an
// in-process MCP tool's ToolUseBlock.Name fully qualified
// (mcp__<server>__<tool>), never as the bare tool name a caller passes in
// RunInput.Tool — comparing against the bare name silently drops every
// Draft proposal.
func TestProcessMessage_RequiresFullyQualifiedMcpName(t *testing.T) {
	var content strings.Builder
	var out RunOutput

	msg := &claudecode.AssistantMessage{
		Content: []claudecode.ContentBlock{&claudecode.ToolUseBlock{Name: "propose_plan", Input: map[string]any{}}},
	}
	_, err := processMessage(msg, "propose_plan", &content, &out, nil)
	require.NoError(t, err)
	assert.Nil(t, out.ToolCall, "the bare tool name alone must not match — the CLI always reports it fully qualified")
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
	_, err := processMessage(msg, "propose_plan", &content, &out, nil)
	require.NoError(t, err)
	assert.Equal(t, "first", out.ToolCall.ID)
}

func TestProcessMessage_ResultMessageEndsTurnSuccessfully(t *testing.T) {
	var content strings.Builder
	content.WriteString("final text")
	var out RunOutput

	done, err := processMessage(&claudecode.ResultMessage{IsError: false}, "propose_plan", &content, &out, nil)
	require.NoError(t, err)
	assert.True(t, done)
	assert.Equal(t, "final text", out.Content)
}

func TestProcessMessage_ResultMessageReportsError(t *testing.T) {
	var content strings.Builder
	var out RunOutput

	done, err := processMessage(&claudecode.ResultMessage{IsError: true, Errors: []string{"boom"}}, "propose_plan", &content, &out, nil)
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
	done, err := processMessage(msg, "propose_plan", &content, &out, func(d chat.Delta) error {
		received = append(received, d)
		return nil
	})
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
	done, err := processMessage(msg, "propose_plan", &content, &out, func(chat.Delta) error { return wantErr })
	assert.True(t, done)
	assert.ErrorIs(t, err, wantErr)
}

func TestStreamDeltaText_IgnoresNonDeltaEvents(t *testing.T) {
	ev := &claudecode.StreamEvent{Event: map[string]any{"type": claudecode.StreamEventTypeMessageStop}}
	_, ok := streamDeltaText(ev)
	assert.False(t, ok)
}

func TestClaudeRunner_CheckHealth_FailsWhenReposRootEmpty(t *testing.T) {
	r := NewClaudeRunner(time.Minute, "")
	err := r.CheckHealth(context.Background())
	require.Error(t, err)
	assert.Contains(t, err.Error(), "AGENT_REPOS_ROOT")
}

func TestClaudeRunner_CheckHealth_FailsWhenCLINotOnPath(t *testing.T) {
	original := lookPath
	lookPath = func(string) (string, error) { return "", errors.New("not found") }
	defer func() { lookPath = original }()

	r := NewClaudeRunner(time.Minute, t.TempDir())
	err := r.CheckHealth(context.Background())
	require.Error(t, err)
	assert.Contains(t, err.Error(), "claude CLI not found on PATH")
}

func TestClaudeRunner_CheckHealth_OKWhenReposRootSetAndCLIFound(t *testing.T) {
	original := lookPath
	lookPath = func(string) (string, error) { return "/usr/bin/claude", nil }
	defer func() { lookPath = original }()

	r := NewClaudeRunner(time.Minute, t.TempDir())
	assert.NoError(t, r.CheckHealth(context.Background()))
}

func TestClaudeRunner_ListModels_ReturnsEmptyNotError(t *testing.T) {
	r := NewClaudeRunner(time.Minute, "")
	models, err := r.ListModels(context.Background())
	assert.NoError(t, err)
	assert.Nil(t, models)
}

func TestClaudeRunner_CloseSession_DisconnectsAndRemovesCachedClient(t *testing.T) {
	r := NewClaudeRunner(time.Minute, "")
	client := &fakeClaudeClient{}
	r.clients["task-a:planning"] = client

	r.CloseSession("task-a:planning")

	assert.True(t, client.disconnected)
	_, ok := r.clients["task-a:planning"]
	assert.False(t, ok)
}

func TestClaudeRunner_CloseSession_OnUnknownKeyIsANoOp(t *testing.T) {
	r := NewClaudeRunner(time.Minute, "")
	assert.NotPanics(t, func() { r.CloseSession("no-such-session") })
}

// fakeClaudeClient is a minimal claudecode.Client stub for exercising
// ClaudeRunner.CloseSession without a live subprocess. Only Disconnect is
// used by the test above; every other method is an unused stub.
type fakeClaudeClient struct {
	disconnected bool
}

func (f *fakeClaudeClient) Connect(context.Context, ...claudecode.StreamMessage) error { return nil }
func (f *fakeClaudeClient) Disconnect() error                                          { f.disconnected = true; return nil }
func (f *fakeClaudeClient) Query(context.Context, string) error                        { return nil }
func (f *fakeClaudeClient) QueryWithSession(context.Context, string, string) error     { return nil }
func (f *fakeClaudeClient) QueryStream(context.Context, <-chan claudecode.StreamMessage) error {
	return nil
}
func (f *fakeClaudeClient) ReceiveMessages(context.Context) <-chan claudecode.Message { return nil }
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

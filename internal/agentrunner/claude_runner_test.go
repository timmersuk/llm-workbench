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
	r := NewClaudeRunner(time.Minute, "")
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
	r := NewClaudeRunner(time.Minute, "")
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
	r := NewClaudeRunner(time.Minute, "")
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
	r := NewClaudeRunner(time.Minute, "")
	_, err := r.clientFor(context.Background(), "task-a:requirements", RunInput{Workspace: ""})
	require.Error(t, err)
	assert.Contains(t, err.Error(), "claude-code requires a project repository")
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

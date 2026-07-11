package agentrunner

import (
	"context"
	"errors"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/timmersuk/llm-workbench/internal/chat"
)

// fakeChatClient is a minimal chat.ChatClient stub for exercising
// ChatClientRunner without a real HTTP upstream. Since the runner now drives
// the tool loop via StreamChatCompletion, that is the method under test here;
// StreamSessionTurn/SeedSessionHistory remain on the interface but are no
// longer called by Run, so they are inert stubs.
type fakeChatClient struct {
	// Scripted StreamChatCompletion response.
	streamContent  string
	streamToolCall *chat.ToolCall
	streamErr      error
	gotRequests    []chat.CompletionRequest

	closedSessions   []string
	checkHealthErr   error
	listModelsResult []string
	listModelsErr    error
}

func (f *fakeChatClient) CreateChatCompletion(context.Context, chat.CompletionRequest) (chat.CompletionResponse, error) {
	return chat.CompletionResponse{}, nil
}

func (f *fakeChatClient) StreamChatCompletion(_ context.Context, req chat.CompletionRequest, onDelta func(chat.Delta) error) error {
	f.gotRequests = append(f.gotRequests, req)
	if f.streamErr != nil {
		return f.streamErr
	}
	if f.streamContent != "" {
		if err := onDelta(chat.Delta{Content: f.streamContent}); err != nil {
			return err
		}
	}
	if f.streamToolCall != nil {
		tc := *f.streamToolCall
		if err := onDelta(chat.Delta{ToolCall: &tc}); err != nil {
			return err
		}
	}
	return nil
}

// Inert on the interface but unused by Run now.
func (f *fakeChatClient) StreamSessionTurn(context.Context, string, string, string, string, []chat.Tool, func(chat.Delta) error) (string, *chat.ToolCall, error) {
	return "", nil, nil
}
func (f *fakeChatClient) SeedSessionHistory(string, []chat.Message) {}

func (f *fakeChatClient) CloseSession(sessionKey string) {
	f.closedSessions = append(f.closedSessions, sessionKey)
}
func (f *fakeChatClient) CheckHealth(context.Context) error { return f.checkHealthErr }
func (f *fakeChatClient) ListModels(context.Context) ([]string, error) {
	return f.listModelsResult, f.listModelsErr
}

func TestChatClientRunner_Run_RequiresSessionKey(t *testing.T) {
	runner := NewChatClientRunner(&fakeChatClient{})
	_, err := runner.Run(context.Background(), RunInput{UserMessage: "hi"}, func(chat.Delta) error { return nil })
	require.Error(t, err)
	assert.Contains(t, err.Error(), "SessionKey")
}

func TestChatClientRunner_Run_BuildsMessagesAndStreams(t *testing.T) {
	client := &fakeChatClient{streamContent: "hi back"}
	runner := NewChatClientRunner(client)

	var gotDeltas []chat.Delta
	out, err := runner.Run(context.Background(), RunInput{
		SessionKey:   "sess-1",
		SystemPrompt: "be nice",
		Model:        "m",
		UserMessage:  "hello",
	}, func(d chat.Delta) error {
		gotDeltas = append(gotDeltas, d)
		return nil
	})

	require.NoError(t, err)
	assert.Equal(t, "hi back", out.Content)
	assert.Nil(t, out.ToolCall)

	require.Len(t, client.gotRequests, 1)
	req := client.gotRequests[0]
	assert.Equal(t, "m", req.Model)
	assert.NotZero(t, req.MaxTokens, "the loop must cap generation")
	// No workspace on RunInput -> no tools offered.
	assert.Empty(t, req.Tools)
	// System prompt becomes the leading system message; then the user turn.
	require.Len(t, req.Messages, 2)
	assert.Equal(t, chat.Message{Role: "system", Content: "be nice"}, req.Messages[0])
	assert.Equal(t, chat.Message{Role: "user", Content: "hello"}, req.Messages[1])
	assert.Equal(t, []chat.Delta{{Content: "hi back"}}, gotDeltas)
}

func TestChatClientRunner_Run_ForwardsToolAndSurfacesToolCall(t *testing.T) {
	toolCall := &chat.ToolCall{ID: "call-1", Type: "function", Function: chat.ToolCallFunction{
		Name: "propose_context", Arguments: `{"objective":"ship login"}`,
	}}
	client := &fakeChatClient{streamContent: "here's my proposal", streamToolCall: toolCall}
	runner := NewChatClientRunner(client)

	tool := chat.Tool{Type: "function", Function: chat.ToolSchema{Name: "propose_context"}}
	out, err := runner.Run(context.Background(), RunInput{
		SessionKey:  "sess-1",
		UserMessage: "let's start",
		Tool:        tool,
	}, func(chat.Delta) error { return nil })

	require.NoError(t, err)
	// The Draft tool is offered to the model as the loop's stop condition.
	require.Len(t, client.gotRequests, 1)
	require.Len(t, client.gotRequests[0].Tools, 1)
	assert.Equal(t, tool, client.gotRequests[0].Tools[0])
	// A call to it surfaces as RunOutput.ToolCall.
	require.NotNil(t, out.ToolCall)
	assert.Equal(t, "propose_context", out.ToolCall.Function.Name)
	assert.Equal(t, `{"objective":"ship login"}`, out.ToolCall.Function.Arguments)
}

func TestChatClientRunner_Run_SeedsHistoryIntoRequest(t *testing.T) {
	client := &fakeChatClient{streamContent: "hi back"}
	runner := NewChatClientRunner(client)

	history := []chat.Message{
		{Role: "user", Content: "earlier turn"},
		{Role: "assistant", Content: "earlier answer"},
	}
	_, err := runner.Run(context.Background(), RunInput{
		SessionKey:  "sess-1",
		UserMessage: "hello",
		History:     history,
	}, func(chat.Delta) error { return nil })

	require.NoError(t, err)
	require.Len(t, client.gotRequests, 1)
	msgs := client.gotRequests[0].Messages
	// Seeded history precedes the new user turn (no system prompt set here).
	require.Len(t, msgs, 3)
	assert.Equal(t, history[0], msgs[0])
	assert.Equal(t, history[1], msgs[1])
	assert.Equal(t, chat.Message{Role: "user", Content: "hello"}, msgs[2])
}

func TestChatClientRunner_Run_DoesNotSeedWhenHistoryEmpty(t *testing.T) {
	client := &fakeChatClient{streamContent: "hi back"}
	runner := NewChatClientRunner(client)

	_, err := runner.Run(context.Background(), RunInput{
		SessionKey:  "sess-1",
		UserMessage: "hello",
	}, func(chat.Delta) error { return nil })

	require.NoError(t, err)
	require.Len(t, client.gotRequests, 1)
	assert.Equal(t, []chat.Message{{Role: "user", Content: "hello"}}, client.gotRequests[0].Messages)
}

// The runner owns per-session history: a second turn on the same session must
// carry the first turn's user message and assistant answer.
func TestChatClientRunner_Run_PersistsTurnsAcrossRuns(t *testing.T) {
	client := &fakeChatClient{streamContent: "answer"}
	runner := NewChatClientRunner(client)

	for _, msg := range []string{"first", "second"} {
		_, err := runner.Run(context.Background(), RunInput{SessionKey: "sess-1", UserMessage: msg}, func(chat.Delta) error { return nil })
		require.NoError(t, err)
	}

	require.Len(t, client.gotRequests, 2)
	second := client.gotRequests[1].Messages
	require.Len(t, second, 3)
	assert.Equal(t, chat.Message{Role: "user", Content: "first"}, second[0])
	assert.Equal(t, chat.Message{Role: "assistant", Content: "answer"}, second[1])
	assert.Equal(t, chat.Message{Role: "user", Content: "second"}, second[2])
}

// A closed session forgets its history: the next run starts fresh.
func TestChatClientRunner_CloseSession_ClearsHeldHistory(t *testing.T) {
	client := &fakeChatClient{streamContent: "answer"}
	runner := NewChatClientRunner(client)

	_, err := runner.Run(context.Background(), RunInput{SessionKey: "sess-1", UserMessage: "first"}, func(chat.Delta) error { return nil })
	require.NoError(t, err)
	runner.CloseSession("sess-1")
	assert.Equal(t, []string{"sess-1"}, client.closedSessions)

	_, err = runner.Run(context.Background(), RunInput{SessionKey: "sess-1", UserMessage: "second"}, func(chat.Delta) error { return nil })
	require.NoError(t, err)

	require.Len(t, client.gotRequests, 2)
	assert.Equal(t, []chat.Message{{Role: "user", Content: "second"}}, client.gotRequests[1].Messages,
		"history from before CloseSession must not leak into the new session")
}

func TestChatClientRunner_Run_PropagatesUnderlyingError(t *testing.T) {
	wantErr := errors.New("upstream down")
	runner := NewChatClientRunner(&fakeChatClient{streamErr: wantErr})
	_, err := runner.Run(context.Background(), RunInput{SessionKey: "sess-1", UserMessage: "hi"}, func(chat.Delta) error { return nil })
	assert.ErrorIs(t, err, wantErr)
}

func TestChatClientRunner_Execute_ReturnsNotSupported(t *testing.T) {
	runner := NewChatClientRunner(&fakeChatClient{})
	_, err := runner.Execute(context.Background(), ExecuteInput{SessionKey: "task-a:execute"}, func(ExecuteEvent) error { return nil })
	assert.ErrorIs(t, err, ErrExecuteNotSupported)
}

func TestChatClientRunner_CheckHealth_DelegatesToWrappedClient(t *testing.T) {
	wantErr := errors.New("down")
	runner := NewChatClientRunner(&fakeChatClient{checkHealthErr: wantErr})
	assert.ErrorIs(t, runner.CheckHealth(context.Background()), wantErr)
}

func TestChatClientRunner_ListModels_DelegatesToWrappedClient(t *testing.T) {
	runner := NewChatClientRunner(&fakeChatClient{listModelsResult: []string{"a", "b"}})
	models, err := runner.ListModels(context.Background())
	require.NoError(t, err)
	assert.Equal(t, []string{"a", "b"}, models)
}

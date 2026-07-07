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
// ChatClientRunner without a real HTTP upstream (internal/chat/client_test.go
// covers StreamSessionTurn/CloseSession's real behavior against a real
// server; this only needs to prove ChatClientRunner translates correctly).
type fakeChatClient struct {
	gotSessionKey, gotSystemPrompt, gotModel, gotUserMessage string
	gotTools                                                 []chat.Tool
	streamTurnContent                                        string
	streamTurnToolCall                                       *chat.ToolCall
	streamTurnErr                                            error
	closedSessions                                           []string
	checkHealthErr                                           error
	listModelsResult                                         []string
	listModelsErr                                            error
}

func (f *fakeChatClient) CreateChatCompletion(context.Context, chat.CompletionRequest) (chat.CompletionResponse, error) {
	return chat.CompletionResponse{}, nil
}
func (f *fakeChatClient) StreamChatCompletion(context.Context, chat.CompletionRequest, func(chat.Delta) error) error {
	return nil
}

func (f *fakeChatClient) StreamSessionTurn(_ context.Context, sessionKey, systemPrompt, model, userMessage string, tools []chat.Tool, onDelta func(chat.Delta) error) (string, *chat.ToolCall, error) {
	f.gotSessionKey, f.gotSystemPrompt, f.gotModel, f.gotUserMessage, f.gotTools = sessionKey, systemPrompt, model, userMessage, tools
	if f.streamTurnContent != "" {
		if err := onDelta(chat.Delta{Content: f.streamTurnContent}); err != nil {
			return "", nil, err
		}
	}
	return f.streamTurnContent, f.streamTurnToolCall, f.streamTurnErr
}

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

func TestChatClientRunner_Run_TranslatesRunInputToStreamSessionTurn(t *testing.T) {
	client := &fakeChatClient{streamTurnContent: "hi back"}
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
	assert.Nil(t, out.ToolCall, "no Tool was set on RunInput, so no tools are offered")
	assert.Empty(t, client.gotTools)
	assert.Equal(t, "sess-1", client.gotSessionKey)
	assert.Equal(t, "be nice", client.gotSystemPrompt)
	assert.Equal(t, "m", client.gotModel)
	assert.Equal(t, "hello", client.gotUserMessage)
	assert.Equal(t, []chat.Delta{{Content: "hi back"}}, gotDeltas)
}

func TestChatClientRunner_Run_ForwardsToolAndSurfacesToolCall(t *testing.T) {
	toolCall := &chat.ToolCall{ID: "call-1", Type: "function", Function: chat.ToolCallFunction{
		Name: "propose_context", Arguments: `{"objective":"ship login"}`,
	}}
	client := &fakeChatClient{streamTurnContent: "here's my proposal", streamTurnToolCall: toolCall}
	runner := NewChatClientRunner(client)

	tool := chat.Tool{Type: "function", Function: chat.ToolSchema{Name: "propose_context"}}
	out, err := runner.Run(context.Background(), RunInput{
		SessionKey:  "sess-1",
		UserMessage: "let's start",
		Tool:        tool,
	}, func(chat.Delta) error { return nil })

	require.NoError(t, err)
	require.Len(t, client.gotTools, 1)
	assert.Equal(t, tool, client.gotTools[0])
	assert.Same(t, toolCall, out.ToolCall)
}

func TestChatClientRunner_Run_PropagatesUnderlyingError(t *testing.T) {
	wantErr := errors.New("upstream down")
	runner := NewChatClientRunner(&fakeChatClient{streamTurnErr: wantErr})
	_, err := runner.Run(context.Background(), RunInput{SessionKey: "sess-1", UserMessage: "hi"}, func(chat.Delta) error { return nil })
	assert.ErrorIs(t, err, wantErr)
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

func TestChatClientRunner_CloseSession_DelegatesToWrappedClient(t *testing.T) {
	client := &fakeChatClient{}
	runner := NewChatClientRunner(client)
	runner.CloseSession("sess-1")
	assert.Equal(t, []string{"sess-1"}, client.closedSessions)
}

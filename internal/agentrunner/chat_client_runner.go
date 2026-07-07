package agentrunner

import (
	"context"
	"errors"

	"github.com/timmersuk/llm-workbench/internal/chat"
)

// ChatClientRunner adapts a chat.ChatClient into the AgentRunner interface,
// so both stage-conversation agents and the free-floating Chat tab are
// selectable and health-checked through the same abstraction. Session
// state (per-SessionKey conversation history) is held by the wrapped
// ChatClient itself (chat.ChatClient.StreamSessionTurn/CloseSession) —
// this type is a thin translator, not a second place holding state.
type ChatClientRunner struct {
	client chat.ChatClient
}

// NewChatClientRunner returns an AgentRunner backed by client.
func NewChatClientRunner(client chat.ChatClient) *ChatClientRunner {
	return &ChatClientRunner{client: client}
}

// Run implements AgentRunner. in.Tool, if set, is offered to the wrapped
// ChatClient the same way stage conversations offer it directly — so a
// Draft proposal from the local-LLM path surfaces as RunOutput.ToolCall
// exactly like ClaudeRunner's does.
func (r *ChatClientRunner) Run(ctx context.Context, in RunInput, onDelta func(chat.Delta) error) (RunOutput, error) {
	if in.SessionKey == "" {
		return RunOutput{}, errors.New("chat client runner requires a non-empty SessionKey")
	}
	var tools []chat.Tool
	if in.Tool.Function.Name != "" {
		tools = []chat.Tool{in.Tool}
	}
	content, toolCall, err := r.client.StreamSessionTurn(ctx, in.SessionKey, in.SystemPrompt, in.Model, in.UserMessage, tools, onDelta)
	return RunOutput{Content: content, ToolCall: toolCall}, err
}

// CheckHealth implements AgentRunner.
func (r *ChatClientRunner) CheckHealth(ctx context.Context) error {
	return r.client.CheckHealth(ctx)
}

// ListModels implements AgentRunner.
func (r *ChatClientRunner) ListModels(ctx context.Context) ([]string, error) {
	return r.client.ListModels(ctx)
}

// CloseSession implements AgentRunner.
func (r *ChatClientRunner) CloseSession(sessionKey string) {
	r.client.CloseSession(sessionKey)
}

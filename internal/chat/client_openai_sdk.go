package chat

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"github.com/openai/openai-go"
	"github.com/openai/openai-go/option"
)

// openaiSDKClient talks to an OpenAI-compatible chat completions endpoint via
// the github.com/openai/openai-go SDK. It is unexported so nothing outside
// this package can depend on the concrete type instead of ChatClient.
type openaiSDKClient struct {
	client  openai.Client
	timeout time.Duration

	sessionsMu sync.Mutex
	sessions   map[string][]Message
}

// NewOpenAISDKClient returns a ChatClient speaking the OpenAI-compatible chat
// completions dialect against baseURL (e.g. "http://localhost:11434/v1"),
// via the official openai-go SDK. apiKey may be empty for servers that don't
// require authentication — openai.NewClient never errors, so unlike
// NewLangChainClient this has nothing to fail on construction.
func NewOpenAISDKClient(baseURL, apiKey string, timeout time.Duration) ChatClient {
	opts := []option.RequestOption{option.WithBaseURL(baseURL)}
	if apiKey != "" {
		opts = append(opts, option.WithAPIKey(apiKey))
	}

	return &openaiSDKClient{
		client:   openai.NewClient(opts...),
		timeout:  timeout,
		sessions: make(map[string][]Message),
	}
}

// CreateChatCompletion implements ChatClient via a single non-streaming
// chat completion call.
func (c *openaiSDKClient) CreateChatCompletion(ctx context.Context, req CompletionRequest) (CompletionResponse, error) {
	req.Stream = false
	req.Messages = withTimeBudgetHint(req.Messages, c.timeout)

	ctx, cancel := context.WithTimeout(ctx, c.timeout)
	defer cancel()

	params := openai.ChatCompletionNewParams{
		Model:    openai.ChatModel(req.Model),
		Messages: toOpenAISDKMessages(req.Messages),
	}
	if len(req.Tools) > 0 {
		params.Tools = toOpenAISDKTools(req.Tools)
	}

	resp, err := c.client.Chat.Completions.New(ctx, params)
	if err != nil {
		return CompletionResponse{}, fmt.Errorf("calling chat completions endpoint: %w", err)
	}
	if len(resp.Choices) == 0 {
		return CompletionResponse{}, fmt.Errorf("chat completions endpoint returned no choices")
	}

	choice := resp.Choices[0]
	return CompletionResponse{
		ID:    resp.ID,
		Model: resp.Model,
		Choices: []Choice{{
			Index:        int(choice.Index),
			Message:      Message{Role: "assistant", Content: choice.Message.Content, ToolCalls: fromOpenAISDKToolCalls(choice.Message.ToolCalls)},
			FinishReason: choice.FinishReason,
		}},
	}, nil
}

// StreamChatCompletion implements ChatClient using openai-go's ssestream
// iterator — a real incremental stream, unlike langchaingo's callback-only
// approach, so tool-call fragments are genuinely visible per-chunk via
// toolCallAccumulator (same as the native client), not just assembled at the
// end.
//
// reasoning_content (the LM Studio/llama.cpp streaming extension the native
// client and langchaingo both support) has no typed field here since it
// isn't part of the official OpenAI schema — it's recovered via each delta's
// preserved raw JSON instead (see reasoningContentFrom).
func (c *openaiSDKClient) StreamChatCompletion(ctx context.Context, req CompletionRequest, onDelta func(Delta) error) error {
	req.Stream = true

	ctx, cancel := context.WithCancel(ctx)
	defer cancel()

	var idleFired atomic.Bool
	timer := time.AfterFunc(c.timeout, func() {
		idleFired.Store(true)
		cancel()
	})
	defer timer.Stop()

	params := openai.ChatCompletionNewParams{
		Model:    openai.ChatModel(req.Model),
		Messages: toOpenAISDKMessages(req.Messages),
	}
	if len(req.Tools) > 0 {
		params.Tools = toOpenAISDKTools(req.Tools)
	}

	stream := c.client.Chat.Completions.NewStreaming(ctx, params)
	defer stream.Close()

	toolCalls := newToolCallAccumulator()
	toolCallsFlushed := false

	for stream.Next() {
		timer.Reset(c.timeout)
		chunk := stream.Current()
		if len(chunk.Choices) == 0 {
			continue
		}

		choice := chunk.Choices[0]
		for _, tc := range choice.Delta.ToolCalls {
			toolCalls.add(int(tc.Index), tc.ID, tc.Function.Name, tc.Function.Arguments)
		}

		delta := Delta{
			Content:          choice.Delta.Content,
			ReasoningContent: reasoningContentFrom(choice.Delta),
		}
		if delta.Content != "" || delta.ReasoningContent != "" {
			if err := onDelta(delta); err != nil {
				return err
			}
		}

		if choice.FinishReason != "" && !toolCallsFlushed {
			toolCallsFlushed = true
			for _, tc := range toolCalls.flush() {
				tc := tc
				if err := onDelta(Delta{ToolCall: &tc}); err != nil {
					return err
				}
			}
		}
	}
	if err := stream.Err(); err != nil {
		if idleFired.Load() {
			return fmt.Errorf("%w: no data for %s", ErrStreamIdleTimeout, c.timeout)
		}
		var apiErr *openai.Error
		if errors.As(err, &apiErr) {
			return fmt.Errorf("chat completions endpoint returned status %d: %s", apiErr.StatusCode, apiErr.Message)
		}
		return fmt.Errorf("reading chat completion stream: %w", err)
	}
	return nil
}

// reasoningContentFrom recovers the LM Studio/llama.cpp "reasoning_content"
// streaming extension from a delta's preserved raw JSON — it isn't part of
// the official OpenAI schema so openai-go has no typed field for it, but
// unrecognized fields survive on RawJSON().
func reasoningContentFrom(delta openai.ChatCompletionChunkChoiceDelta) string {
	var extra struct {
		ReasoningContent string `json:"reasoning_content"`
	}
	_ = json.Unmarshal([]byte(delta.RawJSON()), &extra)
	return extra.ReasoningContent
}

// CheckHealth requests the models list from the provider and returns nil when
// the endpoint responds successfully.
func (c *openaiSDKClient) CheckHealth(ctx context.Context) error {
	_, err := c.ListModels(ctx)
	return err
}

// ListModels requests the available models from the provider and returns
// their IDs.
func (c *openaiSDKClient) ListModels(ctx context.Context) ([]string, error) {
	resp, err := c.client.Models.List(ctx)
	if err != nil {
		return nil, fmt.Errorf("calling models endpoint: %w", err)
	}

	ids := make([]string, 0, len(resp.Data))
	for _, m := range resp.Data {
		ids = append(ids, m.ID)
	}
	return ids, nil
}

// StreamSessionTurn implements ChatClient. Unbounded in-memory growth for
// the life of the process is expected — CloseSession is the mitigation for
// a session that's genuinely done, not a size cap.
func (c *openaiSDKClient) StreamSessionTurn(ctx context.Context, sessionKey, systemPrompt, model, userMessage string, tools []Tool, onDelta func(Delta) error) (string, *ToolCall, error) {
	c.sessionsMu.Lock()
	history := append([]Message{}, c.sessions[sessionKey]...)
	c.sessionsMu.Unlock()

	messages := history
	if systemPrompt != "" && (len(messages) == 0 || messages[0].Role != "system") {
		messages = append([]Message{{Role: "system", Content: systemPrompt}}, messages...)
	}
	messages = append(messages, Message{Role: "user", Content: userMessage})

	var content strings.Builder
	var toolCall *ToolCall
	err := c.StreamChatCompletion(ctx, CompletionRequest{Model: model, Messages: messages, Tools: tools}, func(d Delta) error {
		if d.Content != "" {
			content.WriteString(d.Content)
		}
		if d.ToolCall != nil {
			toolCall = d.ToolCall
		}
		return onDelta(d)
	})

	result := content.String()

	assistantMsg := Message{Role: "assistant", Content: result}
	newHistory := append(messages, assistantMsg)
	if toolCall != nil {
		newHistory[len(newHistory)-1].ToolCalls = []ToolCall{*toolCall}
		newHistory = append(newHistory, Message{Role: "tool", ToolCallID: toolCall.ID, Content: "Draft proposed to user for review."})
	}

	c.sessionsMu.Lock()
	c.sessions[sessionKey] = newHistory
	c.sessionsMu.Unlock()

	return result, toolCall, err
}

// CloseSession implements ChatClient.
func (c *openaiSDKClient) CloseSession(sessionKey string) {
	c.sessionsMu.Lock()
	delete(c.sessions, sessionKey)
	c.sessionsMu.Unlock()
}

// SeedSessionHistory implements ChatClient.
func (c *openaiSDKClient) SeedSessionHistory(sessionKey string, history []Message) {
	c.sessionsMu.Lock()
	defer c.sessionsMu.Unlock()
	if _, exists := c.sessions[sessionKey]; exists {
		return
	}
	c.sessions[sessionKey] = append([]Message{}, history...)
}

// toOpenAISDKMessages converts internal Message structs to openai-go's
// message-param union, preserving tool-call round-tripping: an assistant
// message with ToolCalls is built via the verbose
// ChatCompletionAssistantMessageParam form (the plain openai.AssistantMessage
// helper has no way to carry tool calls), and a "tool" role message (the
// synthetic acknowledgement StreamSessionTurn appends after a tool call)
// becomes an openai.ToolMessage.
func toOpenAISDKMessages(msgs []Message) []openai.ChatCompletionMessageParamUnion {
	out := make([]openai.ChatCompletionMessageParamUnion, 0, len(msgs))
	for _, m := range msgs {
		switch m.Role {
		case "system":
			out = append(out, openai.SystemMessage(m.Content))
		case "tool":
			out = append(out, openai.ToolMessage(m.Content, m.ToolCallID))
		case "assistant":
			if len(m.ToolCalls) == 0 {
				out = append(out, openai.AssistantMessage(m.Content))
				continue
			}
			toolCalls := make([]openai.ChatCompletionMessageToolCallParam, 0, len(m.ToolCalls))
			for _, tc := range m.ToolCalls {
				toolCalls = append(toolCalls, openai.ChatCompletionMessageToolCallParam{
					ID: tc.ID,
					Function: openai.ChatCompletionMessageToolCallFunctionParam{
						Name:      tc.Function.Name,
						Arguments: tc.Function.Arguments,
					},
				})
			}
			out = append(out, openai.ChatCompletionMessageParamUnion{
				OfAssistant: &openai.ChatCompletionAssistantMessageParam{
					Content:   openai.ChatCompletionAssistantMessageParamContentUnion{OfString: openai.String(m.Content)},
					ToolCalls: toolCalls,
				},
			})
		default:
			out = append(out, openai.UserMessage(m.Content))
		}
	}
	return out
}

func toOpenAISDKTools(tools []Tool) []openai.ChatCompletionToolParam {
	out := make([]openai.ChatCompletionToolParam, 0, len(tools))
	for _, t := range tools {
		var params map[string]any
		_ = json.Unmarshal(t.Function.Parameters, &params)
		out = append(out, openai.ChatCompletionToolParam{
			Function: openai.FunctionDefinitionParam{
				Name:        t.Function.Name,
				Description: openai.String(t.Function.Description),
				Parameters:  openai.FunctionParameters(params),
			},
		})
	}
	return out
}

// fromOpenAISDKToolCalls converts openai-go's tool calls back into this
// package's ToolCall shape.
func fromOpenAISDKToolCalls(in []openai.ChatCompletionMessageToolCall) []ToolCall {
	out := make([]ToolCall, 0, len(in))
	for _, tc := range in {
		out = append(out, ToolCall{
			ID:   tc.ID,
			Type: "function",
			Function: ToolCallFunction{
				Name:      tc.Function.Name,
				Arguments: tc.Function.Arguments,
			},
		})
	}
	return out
}

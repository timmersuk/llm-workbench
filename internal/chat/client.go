package chat

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"strings"
	"sync"
	"sync/atomic"
	"time"
)

// ErrStreamIdleTimeout indicates a streaming chat completion was aborted
// because no data arrived from upstream for the configured timeout
// duration — as opposed to the caller's context being cancelled, or the
// stream simply running long while still emitting chunks (which never
// times out; see StreamChatCompletion).
var ErrStreamIdleTimeout = errors.New("chat completion stream received no data before idle timeout")

// ChatClient creates chat completions (blocking and streaming), lists
// available models, and reports provider health. Satisfied by the
// unexported openAIClient returned by NewOpenAIClient — callers only ever
// see this interface, never the concrete implementation, so the provider
// stays swappable in practice and not just in principle (see
// docs/adr/0001-opaque-chat-provider-implementation.md).
type ChatClient interface {
	CreateChatCompletion(ctx context.Context, req CompletionRequest) (CompletionResponse, error)
	StreamChatCompletion(ctx context.Context, req CompletionRequest, onDelta func(Delta) error) error
	ListModels(ctx context.Context) ([]string, error)
	CheckHealth(ctx context.Context) error

	// StreamSessionTurn appends userMessage to sessionKey's held history
	// (seeding a leading system message from systemPrompt on the
	// session's first turn), streams via StreamChatCompletion (offering
	// tools, if any), and appends the accumulated assistant reply back
	// into history before returning — so callers send only the newest
	// turn instead of resending full history each call. If the model
	// calls one of tools, the returned ToolCall is non-nil and the held
	// history records it as an assistant tool-call message followed by a
	// synthetic tool-role acknowledgement, so the next turn stays
	// protocol-valid without the caller reconstructing that shape itself.
	StreamSessionTurn(ctx context.Context, sessionKey, systemPrompt, model, userMessage string, tools []Tool, onDelta func(Delta) error) (content string, toolCall *ToolCall, err error)

	// CloseSession discards sessionKey's held history. Safe to call for a
	// key that never had one (no-op).
	CloseSession(sessionKey string)

	// SeedSessionHistory sets sessionKey's held history to history, but
	// only if the session doesn't already exist — used to rehydrate a
	// persisted conversation into a fresh in-memory session after a
	// process restart, without clobbering a session that's already live.
	SeedSessionHistory(sessionKey string, history []Message)
}

// openAIClient talks to an OpenAI-compatible chat completions endpoint. It
// is unexported so nothing outside this package can depend on the concrete
// type instead of ChatClient.
type openAIClient struct {
	baseURL    string
	apiKey     string
	httpClient *http.Client
	timeout    time.Duration

	sessionsMu sync.Mutex
	sessions   map[string][]Message
}

// NewOpenAIClient returns a ChatClient speaking the OpenAI-compatible chat
// completions dialect against baseURL (e.g. "http://localhost:11434/v1").
// apiKey may be empty for servers that don't require authentication.
//
// timeout bounds the total duration of a non-streaming CreateChatCompletion
// call, and the maximum idle gap between chunks of a StreamChatCompletion
// call (a streaming call that keeps receiving data never times out no
// matter how long it runs overall).
func NewOpenAIClient(baseURL, apiKey string, timeout time.Duration) ChatClient {
	return &openAIClient{
		baseURL:    baseURL,
		apiKey:     apiKey,
		httpClient: &http.Client{},
		timeout:    timeout,
		sessions:   make(map[string][]Message),
	}
}

// CreateChatCompletion posts req to {baseURL}/chat/completions and decodes the
// full response. req.Stream is always sent as false, regardless of its
// zero value, since this method never streams.
func (c *openAIClient) CreateChatCompletion(ctx context.Context, req CompletionRequest) (CompletionResponse, error) {
	req.Stream = false
	req.Messages = withTimeBudgetHint(req.Messages, c.timeout)

	ctx, cancel := context.WithTimeout(ctx, c.timeout)
	defer cancel()

	body, err := json.Marshal(req)
	if err != nil {
		return CompletionResponse{}, fmt.Errorf("encoding chat completion request: %w", err)
	}

	httpReq, err := http.NewRequestWithContext(ctx, http.MethodPost, c.baseURL+"/chat/completions", bytes.NewReader(body))
	if err != nil {
		return CompletionResponse{}, fmt.Errorf("building chat completion request: %w", err)
	}
	c.setHeaders(httpReq)

	resp, err := c.httpClient.Do(httpReq)
	if err != nil {
		return CompletionResponse{}, fmt.Errorf("calling chat completions endpoint: %w", err)
	}
	defer resp.Body.Close()

	respBody, err := io.ReadAll(resp.Body)
	if err != nil {
		return CompletionResponse{}, fmt.Errorf("reading chat completions response: %w", err)
	}

	if resp.StatusCode != http.StatusOK {
		return CompletionResponse{}, fmt.Errorf("chat completions endpoint returned status %d: %s", resp.StatusCode, string(respBody))
	}

	var out CompletionResponse
	if err := json.Unmarshal(respBody, &out); err != nil {
		return CompletionResponse{}, fmt.Errorf("decoding chat completions response: %w", err)
	}
	return out, nil
}

// withTimeBudgetHint returns messages with a system message describing
// budget appended to (or, if none exists, prepended as) the leading system
// message, so a reasoning-capable model has a chance to pace itself instead
// of reliably blowing a deadline mid-reasoning. It never introduces a
// second, competing system message.
func withTimeBudgetHint(messages []Message, budget time.Duration) []Message {
	hint := fmt.Sprintf("You have approximately %s to respond. Keep any internal reasoning brief and prioritize returning a complete answer within that time.", budget)
	if len(messages) > 0 && messages[0].Role == "system" {
		out := make([]Message, len(messages))
		copy(out, messages)
		out[0].Content = out[0].Content + "\n\n" + hint
		return out
	}
	return append([]Message{{Role: "system", Content: hint}}, messages...)
}

// StreamChatCompletion posts req to {baseURL}/chat/completions with
// streaming forced on, and invokes onDelta once per incremental piece of
// content or reasoning content as it arrives over the upstream
// Server-Sent-Events stream. It returns once the stream ends (upstream
// sends "data: [DONE]" or closes the connection) or onDelta returns an
// error, in which case that error is returned immediately without reading
// further.
func (c *openAIClient) StreamChatCompletion(ctx context.Context, req CompletionRequest, onDelta func(Delta) error) error {
	req.Stream = true

	body, err := json.Marshal(req)
	if err != nil {
		return fmt.Errorf("encoding chat completion request: %w", err)
	}

	ctx, cancel := context.WithCancel(ctx)
	defer cancel()

	var idleFired atomic.Bool
	timer := time.AfterFunc(c.timeout, func() {
		idleFired.Store(true)
		cancel()
	})
	defer timer.Stop()

	httpReq, err := http.NewRequestWithContext(ctx, http.MethodPost, c.baseURL+"/chat/completions", bytes.NewReader(body))
	if err != nil {
		return fmt.Errorf("building chat completion request: %w", err)
	}
	c.setHeaders(httpReq)
	httpReq.Header.Set("Accept", "text/event-stream")

	resp, err := c.httpClient.Do(httpReq)
	if err != nil {
		return fmt.Errorf("calling chat completions endpoint: %w", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		respBody, _ := io.ReadAll(resp.Body)
		return fmt.Errorf("chat completions endpoint returned status %d: %s", resp.StatusCode, string(respBody))
	}

	scanner := bufio.NewScanner(resp.Body)
	// Upstream lines (a full completion's accumulated reasoning +
	// content) can comfortably exceed bufio.Scanner's default 64KiB
	// token limit; grow it well past anything a single SSE line should
	// realistically need.
	scanner.Buffer(make([]byte, 0, 64*1024), 1024*1024)

	toolCalls := newToolCallAccumulator()
	toolCallsFlushed := false

	for scanner.Scan() {
		timer.Reset(c.timeout)
		line := strings.TrimSpace(scanner.Text())
		data, ok := strings.CutPrefix(line, "data: ")
		if !ok {
			continue
		}
		if data == "[DONE]" {
			return nil
		}

		var chunk streamChunk
		if err := json.Unmarshal([]byte(data), &chunk); err != nil {
			return fmt.Errorf("decoding chat completion stream chunk: %w", err)
		}
		if len(chunk.Choices) == 0 {
			continue
		}

		choice := chunk.Choices[0]
		for _, tc := range choice.Delta.ToolCalls {
			toolCalls.add(tc.Index, tc.ID, tc.Function.Name, tc.Function.Arguments)
		}

		delta := Delta{
			Content:          choice.Delta.Content,
			ReasoningContent: choice.Delta.ReasoningContent,
		}
		if delta.Content != "" || delta.ReasoningContent != "" {
			if err := onDelta(delta); err != nil {
				return err
			}
		}

		if choice.FinishReason != nil && !toolCallsFlushed {
			toolCallsFlushed = true
			for _, tc := range toolCalls.flush() {
				tc := tc
				if err := onDelta(Delta{ToolCall: &tc}); err != nil {
					return err
				}
			}
		}
	}
	if err := scanner.Err(); err != nil {
		if idleFired.Load() {
			return fmt.Errorf("%w: no data for %s", ErrStreamIdleTimeout, c.timeout)
		}
		return fmt.Errorf("reading chat completion stream: %w", err)
	}
	return nil
}

// CheckHealth requests the models list from the provider and returns nil when
// the endpoint responds successfully.
func (c *openAIClient) CheckHealth(ctx context.Context) error {
	_, err := c.ListModels(ctx)
	return err
}

// ListModels requests the available models from {baseURL}/models and returns
// their IDs.
func (c *openAIClient) ListModels(ctx context.Context) ([]string, error) {
	httpReq, err := http.NewRequestWithContext(ctx, http.MethodGet, c.baseURL+"/models", nil)
	if err != nil {
		return nil, fmt.Errorf("building models request: %w", err)
	}
	c.setHeaders(httpReq)

	resp, err := c.httpClient.Do(httpReq)
	if err != nil {
		return nil, fmt.Errorf("calling models endpoint: %w", err)
	}
	defer resp.Body.Close()

	body, err := io.ReadAll(resp.Body)
	if err != nil {
		return nil, fmt.Errorf("reading models response: %w", err)
	}

	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("models endpoint returned status %d: %s", resp.StatusCode, string(body))
	}

	if len(body) == 0 {
		return []string{}, nil
	}

	var out ModelsResponse
	if err := json.Unmarshal(body, &out); err != nil {
		return nil, fmt.Errorf("decoding models response: %w", err)
	}

	ids := make([]string, 0, len(out.Data))
	for _, m := range out.Data {
		ids = append(ids, m.ID)
	}
	return ids, nil
}

// StreamSessionTurn implements ChatClient. Unbounded in-memory growth for
// the life of the process is expected — CloseSession is the mitigation for
// a session that's genuinely done, not a size cap.
func (c *openAIClient) StreamSessionTurn(ctx context.Context, sessionKey, systemPrompt, model, userMessage string, tools []Tool, onDelta func(Delta) error) (string, *ToolCall, error) {
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
func (c *openAIClient) CloseSession(sessionKey string) {
	c.sessionsMu.Lock()
	delete(c.sessions, sessionKey)
	c.sessionsMu.Unlock()
}

// SeedSessionHistory implements ChatClient.
func (c *openAIClient) SeedSessionHistory(sessionKey string, history []Message) {
	c.sessionsMu.Lock()
	defer c.sessionsMu.Unlock()
	if _, exists := c.sessions[sessionKey]; exists {
		return
	}
	c.sessions[sessionKey] = append([]Message{}, history...)
}

func (c *openAIClient) setHeaders(req *http.Request) {
	req.Header.Set("Content-Type", "application/json")
	if c.apiKey != "" {
		req.Header.Set("Authorization", "Bearer "+c.apiKey)
	}
}

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

	// CloseSession is a lifecycle hook to discard any per-session state a
	// stateful provider might hold. The OpenAI-compatible clients hold none
	// — each engine turn is a stateless completion and the caller
	// (ChatClientRunner) owns conversation history — so their implementation
	// is a no-op. Retained on the interface so a future stateful provider can
	// hook session teardown, and safe to call for any key.
	CloseSession(sessionKey string)
}

// openAIClient talks to an OpenAI-compatible chat completions endpoint. It
// is unexported so nothing outside this package can depend on the concrete
// type instead of ChatClient.
type openAIClient struct {
	baseURL    string
	apiKey     string
	httpClient *http.Client
	timeout    time.Duration
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
	req.StreamOptions = &StreamOptions{IncludeUsage: true}

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
		if chunk.Usage != nil {
			if err := onDelta(Delta{Usage: chunk.Usage}); err != nil {
				return err
			}
		}
		if len(chunk.Choices) == 0 {
			continue
		}

		choice := chunk.Choices[0]
		for _, tc := range choice.Delta.ToolCalls {
			toolCalls.add(tc.Index, tc.ID, tc.Function.Name, tc.Function.Arguments)
		}

		// A given model populates exactly one reasoning key; prefer
		// reasoning_content, fall back to reasoning (gpt-oss family), so the
		// caller sees a single reasoning stream regardless of family.
		reasoning := choice.Delta.ReasoningContent
		if reasoning == "" {
			reasoning = choice.Delta.Reasoning
		}
		delta := Delta{
			Content:          choice.Delta.Content,
			ReasoningContent: reasoning,
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
// their IDs. Bounded by c.timeout so an unreachable or hung provider fails
// fast instead of blocking on the caller's context (which, for a healthcheck
// or agent-executors HTTP handler, has no deadline of its own).
func (c *openAIClient) ListModels(ctx context.Context) ([]string, error) {
	ctx, cancel := context.WithTimeout(ctx, c.timeout)
	defer cancel()

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

// CloseSession implements ChatClient. This client holds no per-session state
// (each turn is a stateless completion; the caller owns history), so there is
// nothing to discard.
func (c *openAIClient) CloseSession(string) {}

func (c *openAIClient) setHeaders(req *http.Request) {
	req.Header.Set("Content-Type", "application/json")
	if c.apiKey != "" {
		req.Header.Set("Authorization", "Bearer "+c.apiKey)
	}
}

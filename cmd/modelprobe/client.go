package main

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"strings"
	"time"
)

// This file is a deliberately standalone OpenAI-compatible client, separate
// from internal/chat. modelprobe needs three things internal/chat does not
// currently expose: a max_tokens ceiling (so a duplicate-call or spiral
// pathology can't run unbounded and wedge the endpoint — exactly the
// misbehavior this tool exists to catch), the raw response bytes (to detect
// whether a model reports reasoning under `reasoning` vs `reasoning_content`,
// a distinction internal/chat's parser erases), and per-request tool_choice
// control. Milestone 8 PR 2 refactors modelprobe onto internal/toolloop +
// internal/chat once that engine (and a max_tokens field on the client)
// exists; until then this keeps the tool honest and dependency-free.

type message struct {
	Role       string     `json:"role"`
	Content    string     `json:"content,omitempty"`
	ToolCalls  []toolCall `json:"tool_calls,omitempty"`
	ToolCallID string     `json:"tool_call_id,omitempty"`
}

type toolCall struct {
	ID       string       `json:"id,omitempty"`
	Type     string       `json:"type,omitempty"`
	Function functionCall `json:"function"`
}

type functionCall struct {
	Name      string `json:"name"`
	Arguments string `json:"arguments"`
}

type tool struct {
	Type     string       `json:"type"`
	Function toolFunction `json:"function"`
}

type toolFunction struct {
	Name        string          `json:"name"`
	Description string          `json:"description,omitempty"`
	Parameters  json.RawMessage `json:"parameters"`
}

type completionRequest struct {
	Model      string    `json:"model"`
	Messages   []message `json:"messages"`
	Tools      []tool    `json:"tools,omitempty"`
	ToolChoice any       `json:"tool_choice,omitempty"`
	MaxTokens  int       `json:"max_tokens,omitempty"`
	Stream     bool      `json:"stream,omitempty"`
}

// responseMessage captures both reasoning keys so a caller can detect which
// one (if any) a given model/server populates.
type responseMessage struct {
	Role             string     `json:"role"`
	Content          string     `json:"content"`
	Reasoning        string     `json:"reasoning"`
	ReasoningContent string     `json:"reasoning_content"`
	ToolCalls        []toolCall `json:"tool_calls"`
}

type completionResponse struct {
	Model   string `json:"model"` // server-echoed id — reveals JIT/misrouting when it differs from the requested model
	Choices []struct {
		Message      responseMessage `json:"message"`
		FinishReason string          `json:"finish_reason"`
	} `json:"choices"`
	Usage struct {
		PromptTokens     int `json:"prompt_tokens"`
		CompletionTokens int `json:"completion_tokens"`
		TotalTokens      int `json:"total_tokens"`
	} `json:"usage"`
}

// streamChunk is one OpenAI-compatible chat.completion.chunk SSE payload.
type streamChunk struct {
	Choices []struct {
		Delta struct {
			Content          string `json:"content"`
			Reasoning        string `json:"reasoning"`
			ReasoningContent string `json:"reasoning_content"`
			ToolCalls        []struct {
				Index    int    `json:"index"`
				ID       string `json:"id"`
				Type     string `json:"type"`
				Function struct {
					Name      string `json:"name"`
					Arguments string `json:"arguments"`
				} `json:"function"`
			} `json:"tool_calls"`
		} `json:"delta"`
		FinishReason string `json:"finish_reason"`
	} `json:"choices"`
}

type client struct {
	baseURL string
	apiKey  string
	http    *http.Client
	timeout time.Duration
}

func newClient(baseURL, apiKey string, timeout time.Duration) *client {
	return &client{
		baseURL: strings.TrimRight(baseURL, "/"),
		apiKey:  apiKey,
		http:    &http.Client{},
		timeout: timeout,
	}
}

func (c *client) post(ctx context.Context, body any) (*http.Response, error) {
	b, err := json.Marshal(body)
	if err != nil {
		return nil, err
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, c.baseURL+"/chat/completions", bytes.NewReader(b))
	if err != nil {
		return nil, err
	}
	req.Header.Set("Content-Type", "application/json")
	if c.apiKey != "" {
		req.Header.Set("Authorization", "Bearer "+c.apiKey)
	}
	return c.http.Do(req)
}

// complete runs a non-streaming completion and returns the parsed response
// plus the raw response bytes (for wire-shape inspection).
func (c *client) complete(ctx context.Context, req completionRequest) (completionResponse, []byte, error) {
	req.Stream = false
	ctx, cancel := context.WithTimeout(ctx, c.timeout)
	defer cancel()

	resp, err := c.post(ctx, req)
	if err != nil {
		return completionResponse{}, nil, err
	}
	defer resp.Body.Close()

	raw, err := io.ReadAll(resp.Body)
	if err != nil {
		return completionResponse{}, nil, err
	}
	if resp.StatusCode != http.StatusOK {
		return completionResponse{}, raw, fmt.Errorf("http %d: %s", resp.StatusCode, snippet(raw, 300))
	}

	var parsed completionResponse
	if err := json.Unmarshal(raw, &parsed); err != nil {
		return completionResponse{}, raw, fmt.Errorf("decode: %w", err)
	}
	return parsed, raw, nil
}

// streamObservation is what stream accumulates while reading SSE chunks.
type streamObservation struct {
	content          strings.Builder
	reasoning        strings.Builder
	reasoningContent strings.Builder
	toolCalls        map[int]*toolCall // keyed by delta index
	toolCallOrder    []int
	chunks           int
	sawToolDelta     bool
}

// stream runs a streaming completion, accumulating deltas into a
// streamObservation so callers can assert on incremental tool-call and
// reasoning delivery.
func (c *client) stream(ctx context.Context, req completionRequest) (*streamObservation, error) {
	req.Stream = true
	ctx, cancel := context.WithTimeout(ctx, c.timeout)
	defer cancel()

	resp, err := c.post(ctx, req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		raw, _ := io.ReadAll(resp.Body)
		return nil, fmt.Errorf("http %d: %s", resp.StatusCode, snippet(raw, 300))
	}

	obs := &streamObservation{toolCalls: map[int]*toolCall{}}
	sc := bufio.NewScanner(resp.Body)
	sc.Buffer(make([]byte, 0, 64*1024), 1024*1024)
	for sc.Scan() {
		line := strings.TrimSpace(sc.Text())
		if !strings.HasPrefix(line, "data:") {
			continue
		}
		payload := strings.TrimSpace(strings.TrimPrefix(line, "data:"))
		if payload == "[DONE]" {
			break
		}
		var chunk streamChunk
		if err := json.Unmarshal([]byte(payload), &chunk); err != nil {
			continue // tolerate keep-alive / non-JSON lines
		}
		obs.chunks++
		for _, ch := range chunk.Choices {
			obs.content.WriteString(ch.Delta.Content)
			obs.reasoning.WriteString(ch.Delta.Reasoning)
			obs.reasoningContent.WriteString(ch.Delta.ReasoningContent)
			for _, tc := range ch.Delta.ToolCalls {
				obs.sawToolDelta = true
				existing, ok := obs.toolCalls[tc.Index]
				if !ok {
					existing = &toolCall{Type: "function"}
					obs.toolCalls[tc.Index] = existing
					obs.toolCallOrder = append(obs.toolCallOrder, tc.Index)
				}
				if tc.ID != "" {
					existing.ID = tc.ID
				}
				if tc.Function.Name != "" {
					existing.Function.Name = tc.Function.Name
				}
				existing.Function.Arguments += tc.Function.Arguments
			}
		}
	}
	if err := sc.Err(); err != nil {
		return obs, fmt.Errorf("read stream: %w", err)
	}
	return obs, nil
}

func snippet(b []byte, n int) string {
	s := strings.TrimSpace(string(b))
	if len(s) > n {
		return s[:n] + "…"
	}
	return s
}

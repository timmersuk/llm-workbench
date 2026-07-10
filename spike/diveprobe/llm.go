package main

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"strings"

	"github.com/deepnoodle-ai/dive/llm"

	"github.com/timmersuk/llm-workbench/internal/chat"
)

// wbLLM adapts the workbench's chat.ChatClient to dive's llm.LLM /
// llm.StreamingLLM. Unlike eino (whose schema is OpenAI-flavored), dive's
// core types follow the Anthropic wire format: messages hold content
// blocks, and Stream must emit a message_start → content_block_* →
// message_delta → message_stop event sequence that dive's
// ResponseAccumulator reassembles. The adapter is therefore a full
// OpenAI→Anthropic shape translation, not a field-for-field mapping.
type wbLLM struct {
	client    chat.ChatClient
	modelName string
}

func newWBLLM(client chat.ChatClient, modelName string) *wbLLM {
	return &wbLLM{client: client, modelName: modelName}
}

func (m *wbLLM) Name() string { return "workbench-chat" }

// toChatRequest resolves dive's functional options into the OpenAI-compat
// request shape internal/chat speaks.
func (m *wbLLM) toChatRequest(opts ...llm.Option) (chat.CompletionRequest, error) {
	var cfg llm.Config
	cfg.Apply(opts...)

	// Diagnostic (spike): dive unconditionally appends a one-sentence
	// "<system-reminder>" priming rule to the system prompt (agent.go:489).
	// Strip it to isolate whether it destabilizes the local model.
	systemPrompt := cfg.SystemPrompt
	if os.Getenv("SPIKE_STRIP_PRIMING") == "1" {
		if i := strings.Index(systemPrompt, "\n\nRuntime context may appear in <system-reminder>"); i >= 0 {
			systemPrompt = systemPrompt[:i]
		}
	}

	msgs := make([]chat.Message, 0, len(cfg.Messages)+1)
	if systemPrompt != "" {
		msgs = append(msgs, chat.Message{Role: "system", Content: systemPrompt})
	}
	for _, dm := range cfg.Messages {
		var text string
		var toolCalls []chat.ToolCall
		for _, c := range dm.Content {
			switch blk := c.(type) {
			case *llm.TextContent:
				text += blk.Text
			case *llm.ToolUseContent:
				toolCalls = append(toolCalls, chat.ToolCall{
					ID:   blk.ID,
					Type: "function",
					Function: chat.ToolCallFunction{
						Name:      blk.Name,
						Arguments: string(blk.Input),
					},
				})
			case *llm.ToolResultContent:
				content, ok := blk.Content.(string)
				if !ok {
					b, err := json.Marshal(blk.Content)
					if err != nil {
						return chat.CompletionRequest{}, fmt.Errorf("tool result content: %w", err)
					}
					content = string(b)
				}
				msgs = append(msgs, chat.Message{
					Role:       "tool",
					ToolCallID: blk.ToolUseID,
					Content:    content,
				})
			case *llm.ThinkingContent:
				// internal/chat.Message has no reasoning field; prior-turn
				// thinking is not replayed (matching OpenAI-compat semantics).
			}
		}
		if text != "" || len(toolCalls) > 0 {
			msgs = append(msgs, chat.Message{
				Role:      string(dm.Role),
				Content:   text,
				ToolCalls: toolCalls,
			})
		}
	}

	tools := make([]chat.Tool, 0, len(cfg.Tools))
	for _, t := range cfg.Tools {
		params, err := json.Marshal(t.Schema())
		if err != nil {
			return chat.CompletionRequest{}, fmt.Errorf("tool %s schema: %w", t.Name(), err)
		}
		tools = append(tools, chat.Tool{
			Type:     "function",
			Function: chat.ToolSchema{Name: t.Name(), Description: t.Description(), Parameters: params},
		})
	}

	return chat.CompletionRequest{Model: m.modelName, Messages: msgs, Tools: tools}, nil
}

func mapStopReason(finishReason string, hasToolCalls bool) string {
	if hasToolCalls || finishReason == "tool_calls" {
		return "tool_use"
	}
	return "end_turn"
}

func (m *wbLLM) Generate(ctx context.Context, opts ...llm.Option) (*llm.Response, error) {
	req, err := m.toChatRequest(opts...)
	if err != nil {
		return nil, err
	}
	resp, err := m.client.CreateChatCompletion(ctx, req)
	if err != nil {
		return nil, err
	}
	if len(resp.Choices) == 0 {
		return nil, errors.New("no choices in completion response")
	}
	choice := resp.Choices[0]

	var content []llm.Content
	if choice.Message.Content != "" {
		content = append(content, &llm.TextContent{Text: choice.Message.Content})
	}
	for _, tc := range choice.Message.ToolCalls {
		content = append(content, &llm.ToolUseContent{
			ID:    tc.ID,
			Name:  tc.Function.Name,
			Input: json.RawMessage(tc.Function.Arguments),
		})
	}

	return &llm.Response{
		ID:         resp.ID,
		Model:      resp.Model,
		Role:       llm.Assistant,
		Type:       "message",
		Content:    content,
		StopReason: mapStopReason(choice.FinishReason, len(choice.Message.ToolCalls) > 0),
	}, nil
}

// wbStream is the StreamIterator handed to dive; events are produced by
// the goroutine in Stream below.
type wbStream struct {
	events <-chan *llm.Event
	cancel context.CancelFunc
	cur    *llm.Event
	err    error
	errCh  <-chan error
}

func (s *wbStream) Next() bool {
	ev, ok := <-s.events
	if !ok {
		// Producer finished; collect its terminal error, if any.
		s.err = <-s.errCh
		return false
	}
	s.cur = ev
	return true
}

func (s *wbStream) Event() *llm.Event { return s.cur }
func (s *wbStream) Err() error        { return s.err }
func (s *wbStream) Close() error {
	s.cancel()
	return nil
}

func (m *wbLLM) Stream(ctx context.Context, opts ...llm.Option) (llm.StreamIterator, error) {
	req, err := m.toChatRequest(opts...)
	if err != nil {
		return nil, err
	}

	ctx, cancel := context.WithCancel(ctx)
	events := make(chan *llm.Event, 8)
	errCh := make(chan error, 1)

	go func() {
		defer close(events)

		send := func(ev *llm.Event) bool {
			select {
			case events <- ev:
				return true
			case <-ctx.Done():
				return false
			}
		}

		send(&llm.Event{
			Type: llm.EventTypeMessageStart,
			Message: &llm.Response{
				Model: m.modelName,
				Role:  llm.Assistant,
				Type:  "message",
			},
		})

		// Streaming state: which block type is open, at what index.
		blockIdx := -1
		var openType llm.ContentType
		sawToolCall := false

		closeBlock := func() bool {
			if openType == "" {
				return true
			}
			idx := blockIdx
			openType = ""
			return send(&llm.Event{Type: llm.EventTypeContentBlockStop, Index: &idx})
		}
		openBlock := func(t llm.ContentType, blk *llm.EventContentBlock) (int, bool) {
			if !closeBlock() {
				return 0, false
			}
			blockIdx++
			openType = t
			idx := blockIdx
			blk.Type = t
			return idx, send(&llm.Event{Type: llm.EventTypeContentBlockStart, Index: &idx, ContentBlock: blk})
		}

		streamErr := m.client.StreamChatCompletion(ctx, req, func(d chat.Delta) error {
			switch {
			case d.ToolCall != nil:
				sawToolCall = true
				idx, ok := openBlock(llm.ContentTypeToolUse, &llm.EventContentBlock{
					ID:   d.ToolCall.ID,
					Name: d.ToolCall.Function.Name,
				})
				if !ok {
					return context.Canceled
				}
				// chat.ChatClient emits tool calls fully accumulated, so
				// the entire argument object is one input_json_delta.
				if !send(&llm.Event{
					Type:  llm.EventTypeContentBlockDelta,
					Index: &idx,
					Delta: &llm.EventDelta{Type: llm.EventDeltaTypeInputJSON, PartialJSON: d.ToolCall.Function.Arguments},
				}) {
					return context.Canceled
				}
				if !closeBlock() {
					return context.Canceled
				}
			case d.ReasoningContent != "":
				if openType != llm.ContentTypeThinking {
					if _, ok := openBlock(llm.ContentTypeThinking, &llm.EventContentBlock{}); !ok {
						return context.Canceled
					}
				}
				idx := blockIdx
				if !send(&llm.Event{
					Type:  llm.EventTypeContentBlockDelta,
					Index: &idx,
					Delta: &llm.EventDelta{Type: llm.EventDeltaTypeThinking, Thinking: d.ReasoningContent},
				}) {
					return context.Canceled
				}
			case d.Content != "":
				if openType != llm.ContentTypeText {
					if _, ok := openBlock(llm.ContentTypeText, &llm.EventContentBlock{}); !ok {
						return context.Canceled
					}
				}
				idx := blockIdx
				if !send(&llm.Event{
					Type:  llm.EventTypeContentBlockDelta,
					Index: &idx,
					Delta: &llm.EventDelta{Type: llm.EventDeltaTypeText, Text: d.Content},
				}) {
					return context.Canceled
				}
			}
			return nil
		})

		if streamErr != nil && ctx.Err() == nil {
			errCh <- streamErr
			return
		}

		closeBlock()
		send(&llm.Event{
			Type:  llm.EventTypeMessageDelta,
			Delta: &llm.EventDelta{StopReason: mapStopReason("", sawToolCall)},
		})
		send(&llm.Event{Type: llm.EventTypeMessageStop})
		errCh <- nil
	}()

	return &wbStream{events: events, cancel: cancel, errCh: errCh}, nil
}

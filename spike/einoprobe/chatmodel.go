package main

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"

	"github.com/cloudwego/eino/components/model"
	"github.com/cloudwego/eino/schema"

	"github.com/timmersuk/llm-workbench/internal/chat"
)

// wbChatModel adapts the workbench's chat.ChatClient to eino's
// model.ToolCallingChatModel, the spike's criterion-1 test: does wrapping
// the existing client lose incremental tool-call-delta streaming or
// reasoning_content? Stream() maps each chat.Delta 1:1 onto an eino
// message chunk, so whatever the client surfaces, eino consumers see.
type wbChatModel struct {
	client    chat.ChatClient
	modelName string
	tools     []chat.Tool
}

func newWBChatModel(client chat.ChatClient, modelName string) *wbChatModel {
	return &wbChatModel{client: client, modelName: modelName}
}

// WithTools returns a copy bound to the given tools, per eino's contract
// that WithTools must not mutate the receiver.
func (m *wbChatModel) WithTools(tools []*schema.ToolInfo) (model.ToolCallingChatModel, error) {
	converted := make([]chat.Tool, 0, len(tools))
	for _, ti := range tools {
		params := json.RawMessage(`{"type":"object"}`)
		if ti.ParamsOneOf != nil {
			js, err := ti.ToJSONSchema()
			if err != nil {
				return nil, fmt.Errorf("tool %s: %w", ti.Name, err)
			}
			b, err := json.Marshal(js)
			if err != nil {
				return nil, fmt.Errorf("tool %s: %w", ti.Name, err)
			}
			// Re-marshal into canonical key order (type first) — eino's
			// jsonschema marshals "type" last, and templates that inject
			// tool JSON verbatim may destabilize models trained on the
			// canonical ordering. Diagnostic for the eino-vs-dive gap.
			var full map[string]json.RawMessage
			if err := json.Unmarshal(b, &full); err != nil {
				return nil, fmt.Errorf("tool %s: %w", ti.Name, err)
			}
			ordered := fmt.Sprintf(`{"type":"object","required":%s,"properties":%s}`,
				string(full["required"]), string(full["properties"]))
			params = json.RawMessage(ordered)
		}
		converted = append(converted, chat.Tool{
			Type:     "function",
			Function: chat.ToolSchema{Name: ti.Name, Description: ti.Desc, Parameters: params},
		})
	}
	return &wbChatModel{client: m.client, modelName: m.modelName, tools: converted}, nil
}

func toChatMessages(in []*schema.Message) []chat.Message {
	out := make([]chat.Message, 0, len(in))
	for _, m := range in {
		cm := chat.Message{Role: string(m.Role), Content: m.Content, ToolCallID: m.ToolCallID}
		for _, tc := range m.ToolCalls {
			cm.ToolCalls = append(cm.ToolCalls, chat.ToolCall{
				ID:   tc.ID,
				Type: tc.Type,
				Function: chat.ToolCallFunction{
					Name:      tc.Function.Name,
					Arguments: tc.Function.Arguments,
				},
			})
		}
		out = append(out, cm)
	}
	return out
}

func (m *wbChatModel) Generate(ctx context.Context, in []*schema.Message, _ ...model.Option) (*schema.Message, error) {
	resp, err := m.client.CreateChatCompletion(ctx, chat.CompletionRequest{
		Model:    m.modelName,
		Messages: toChatMessages(in),
		Tools:    m.tools,
	})
	if err != nil {
		return nil, err
	}
	if len(resp.Choices) == 0 {
		return nil, errors.New("no choices in completion response")
	}
	choice := resp.Choices[0]
	out := &schema.Message{Role: schema.Assistant, Content: choice.Message.Content}
	for i, tc := range choice.Message.ToolCalls {
		idx := i
		out.ToolCalls = append(out.ToolCalls, schema.ToolCall{
			Index:    &idx,
			ID:       tc.ID,
			Type:     tc.Type,
			Function: schema.FunctionCall{Name: tc.Function.Name, Arguments: tc.Function.Arguments},
		})
	}
	return out, nil
}

// errReaderClosed signals the delta callback to stop because the eino-side
// reader went away; it never escapes Stream's goroutine.
var errReaderClosed = errors.New("eino stream reader closed")

func (m *wbChatModel) Stream(ctx context.Context, in []*schema.Message, _ ...model.Option) (*schema.StreamReader[*schema.Message], error) {
	req := chat.CompletionRequest{
		Model:    m.modelName,
		Messages: toChatMessages(in),
		Tools:    m.tools,
	}
	sr, sw := schema.Pipe[*schema.Message](8)
	go func() {
		defer sw.Close()
		toolIdx := 0
		err := m.client.StreamChatCompletion(ctx, req, func(d chat.Delta) error {
			chunk := &schema.Message{Role: schema.Assistant}
			switch {
			case d.ToolCall != nil:
				// chat.ChatClient emits tool calls only once fully
				// accumulated, so each Delta is one complete call.
				idx := toolIdx
				toolIdx++
				chunk.ToolCalls = []schema.ToolCall{{
					Index: &idx,
					ID:    d.ToolCall.ID,
					Type:  d.ToolCall.Type,
					Function: schema.FunctionCall{
						Name:      d.ToolCall.Function.Name,
						Arguments: d.ToolCall.Function.Arguments,
					},
				}}
			case d.ReasoningContent != "":
				chunk.ReasoningContent = d.ReasoningContent
			default:
				chunk.Content = d.Content
			}
			if closed := sw.Send(chunk, nil); closed {
				return errReaderClosed
			}
			return nil
		})
		if err != nil && !errors.Is(err, errReaderClosed) {
			sw.Send(nil, err)
		}
	}()
	return sr, nil
}

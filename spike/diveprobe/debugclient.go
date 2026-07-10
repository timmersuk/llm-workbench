package main

import (
	"context"
	"fmt"
	"os"

	"github.com/timmersuk/llm-workbench/internal/chat"
)

// debugClient wraps a chat.ChatClient and dumps each outgoing request's
// shape to stderr, so we can diff what dive sends vs what eino sent.
type debugClient struct {
	chat.ChatClient
}

func dumpRequest(req chat.CompletionRequest) {
	fmt.Fprintf(os.Stderr, "\n--- request: %d messages, %d tools ---\n", len(req.Messages), len(req.Tools))
	for i, m := range req.Messages {
		preview := m.Content
		if len(preview) > 200 {
			preview = preview[:200] + "..."
		}
		fmt.Fprintf(os.Stderr, "  [%d] role=%s toolCalls=%d toolCallID=%q content=%q\n",
			i, m.Role, len(m.ToolCalls), m.ToolCallID, preview)
	}
	for _, t := range req.Tools {
		fmt.Fprintf(os.Stderr, "  tool: %s\n", t.Function.Name)
	}
	fmt.Fprintln(os.Stderr, "---")
}

func (d *debugClient) CreateChatCompletion(ctx context.Context, req chat.CompletionRequest) (chat.CompletionResponse, error) {
	dumpRequest(req)
	return d.ChatClient.CreateChatCompletion(ctx, req)
}

func (d *debugClient) StreamChatCompletion(ctx context.Context, req chat.CompletionRequest, onDelta func(chat.Delta) error) error {
	dumpRequest(req)
	return d.ChatClient.StreamChatCompletion(ctx, req, onDelta)
}

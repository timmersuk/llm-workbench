package main

import (
	"context"
	"encoding/json"
	"testing"

	"github.com/cloudwego/eino/schema"

	"github.com/timmersuk/llm-workbench/internal/chat"
)

// fakeClient captures the request the adapter builds, without any network.
type fakeClient struct {
	chat.ChatClient
	captured chat.CompletionRequest
}

func (f *fakeClient) CreateChatCompletion(_ context.Context, req chat.CompletionRequest) (chat.CompletionResponse, error) {
	f.captured = req
	return chat.CompletionResponse{Choices: []chat.Choice{{}}}, nil
}

// TestDumpToolSchema prints the exact chat.Tool JSON the eino adapter puts
// on the wire, for diffing against the dive adapter's output.
func TestDumpToolSchema(t *testing.T) {
	ctx := context.Background()
	tools, err := makeTools(".")
	if err != nil {
		t.Fatal(err)
	}
	infos := make([]*schema.ToolInfo, 0, len(tools))
	for _, tl := range tools {
		info, err := tl.Info(ctx)
		if err != nil {
			t.Fatal(err)
		}
		infos = append(infos, info)
	}

	fake := &fakeClient{}
	bound, err := newWBChatModel(fake, "test-model").WithTools(infos)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := bound.Generate(ctx, []*schema.Message{schema.UserMessage("hi")}); err != nil {
		t.Fatal(err)
	}

	b, err := json.MarshalIndent(fake.captured.Tools, "", "  ")
	if err != nil {
		t.Fatal(err)
	}
	t.Logf("wire tools:\n%s", b)
}

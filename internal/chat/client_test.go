package chat

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestClient_CreateChatCompletion_RequestShape(t *testing.T) {
	var gotAuth string
	var gotReq CompletionRequest

	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotAuth = r.Header.Get("Authorization")
		require.NoError(t, json.NewDecoder(r.Body).Decode(&gotReq))

		resp := CompletionResponse{
			ID:    "chatcmpl-1",
			Model: "test-model",
			Choices: []Choice{{
				Index:        0,
				Message:      Message{Role: "assistant", Content: "hello back"},
				FinishReason: "stop",
			}},
		}
		w.Header().Set("Content-Type", "application/json")
		require.NoError(t, json.NewEncoder(w).Encode(resp))
	}))
	defer server.Close()

	client := NewOpenAIClient(server.URL, "test-key", 5*time.Second)
	req := CompletionRequest{
		Model:    "test-model",
		Messages: []Message{{Role: "user", Content: "hello"}},
	}

	resp, err := client.CreateChatCompletion(context.Background(), req)
	require.NoError(t, err)

	assert.Equal(t, "Bearer test-key", gotAuth)
	assert.Equal(t, req, gotReq)
	assert.False(t, gotReq.Stream, "CreateChatCompletion must always send stream: false")
	assert.Equal(t, "hello back", resp.Choices[0].Message.Content)
}

func TestClient_CreateChatCompletion_NoAuthHeaderWhenKeyEmpty(t *testing.T) {
	var gotAuth string
	var sawAuth bool

	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotAuth, sawAuth = r.Header.Get("Authorization"), r.Header.Get("Authorization") != ""
		w.Header().Set("Content-Type", "application/json")
		require.NoError(t, json.NewEncoder(w).Encode(CompletionResponse{}))
	}))
	defer server.Close()

	client := NewOpenAIClient(server.URL, "", 5*time.Second)
	_, err := client.CreateChatCompletion(context.Background(), CompletionRequest{})
	require.NoError(t, err)
	assert.False(t, sawAuth, "expected no Authorization header, got %q", gotAuth)
}

func TestClient_CreateChatCompletion_NonOKStatus(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusInternalServerError)
		w.Write([]byte("upstream exploded"))
	}))
	defer server.Close()

	client := NewOpenAIClient(server.URL, "", 5*time.Second)
	_, err := client.CreateChatCompletion(context.Background(), CompletionRequest{})
	require.Error(t, err)
	assert.ErrorContains(t, err, "500")
}

func TestClient_CreateChatCompletion_MalformedJSON(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		w.Write([]byte("{not json"))
	}))
	defer server.Close()

	client := NewOpenAIClient(server.URL, "", 5*time.Second)
	_, err := client.CreateChatCompletion(context.Background(), CompletionRequest{})
	require.Error(t, err)
	assert.ErrorContains(t, err, "decoding")
}

func TestClient_ListModels_OK(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		assert.Equal(t, "/models", r.URL.Path)
		w.Header().Set("Content-Type", "application/json")
		require.NoError(t, json.NewEncoder(w).Encode(ModelsResponse{
			Data: []ModelInfo{{ID: "llama3"}, {ID: "mistral"}},
		}))
	}))
	defer server.Close()

	client := NewOpenAIClient(server.URL, "", 5*time.Second)
	models, err := client.ListModels(context.Background())
	require.NoError(t, err)
	assert.Equal(t, []string{"llama3", "mistral"}, models)
}

func TestClient_ListModels_NonOKStatus(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusInternalServerError)
		w.Write([]byte("upstream exploded"))
	}))
	defer server.Close()

	client := NewOpenAIClient(server.URL, "", 5*time.Second)
	_, err := client.ListModels(context.Background())
	require.Error(t, err)
	assert.ErrorContains(t, err, "500")
}

func TestClient_CheckHealth_UsesListModels(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		assert.Equal(t, "/models", r.URL.Path)
		w.WriteHeader(http.StatusOK)
	}))
	defer server.Close()

	client := NewOpenAIClient(server.URL, "", 5*time.Second)
	require.NoError(t, client.CheckHealth(context.Background()))
}

func TestClient_CreateChatCompletion_ContextCancelled(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		time.Sleep(50 * time.Millisecond)
		w.Header().Set("Content-Type", "application/json")
		require.NoError(t, json.NewEncoder(w).Encode(CompletionResponse{}))
	}))
	defer server.Close()

	ctx, cancel := context.WithCancel(context.Background())
	cancel()

	client := NewOpenAIClient(server.URL, "", 5*time.Second)
	_, err := client.CreateChatCompletion(ctx, CompletionRequest{})
	require.Error(t, err)
}

// writeSSE writes a single OpenAI-compatible "chat.completion.chunk" SSE
// line carrying the given content/reasoningContent delta.
func writeSSE(w http.ResponseWriter, content, reasoningContent string) {
	chunk := map[string]any{
		"id":     "chatcmpl-test",
		"object": "chat.completion.chunk",
		"choices": []map[string]any{{
			"index": 0,
			"delta": map[string]string{"content": content, "reasoning_content": reasoningContent},
		}},
	}
	data, _ := json.Marshal(chunk)
	fmt.Fprintf(w, "data: %s\n\n", data)
	if f, ok := w.(http.Flusher); ok {
		f.Flush()
	}
}

func TestClient_StreamChatCompletion_ForcesStreamTrue(t *testing.T) {
	var gotReq CompletionRequest
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		require.NoError(t, json.NewDecoder(r.Body).Decode(&gotReq))
		w.Header().Set("Content-Type", "text/event-stream")
		fmt.Fprint(w, "data: [DONE]\n\n")
	}))
	defer server.Close()

	client := NewOpenAIClient(server.URL, "", 5*time.Second)
	err := client.StreamChatCompletion(context.Background(), CompletionRequest{Model: "m", Stream: false}, func(Delta) error { return nil })
	require.NoError(t, err)
	assert.True(t, gotReq.Stream, "StreamChatCompletion must always send stream: true")
}

func TestClient_StreamChatCompletion_DeliversReasoningThenContentDeltas(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/event-stream")
		writeSSE(w, "", "Think")
		writeSSE(w, "", "ing...")
		writeSSE(w, "Hello", "")
		writeSSE(w, " world", "")
		fmt.Fprint(w, "data: [DONE]\n\n")
	}))
	defer server.Close()

	client := NewOpenAIClient(server.URL, "", 5*time.Second)
	var got []Delta
	err := client.StreamChatCompletion(context.Background(), CompletionRequest{Model: "m"}, func(d Delta) error {
		got = append(got, d)
		return nil
	})
	require.NoError(t, err)
	assert.Equal(t, []Delta{
		{ReasoningContent: "Think"},
		{ReasoningContent: "ing..."},
		{Content: "Hello"},
		{Content: " world"},
	}, got)
}

func TestClient_StreamChatCompletion_SkipsEmptyDeltas(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/event-stream")
		writeSSE(w, "hi", "")
		// A final chunk with an empty delta (just a finish_reason) is
		// realistic upstream behavior and must not reach onDelta.
		fmt.Fprint(w, `data: {"choices":[{"index":0,"delta":{},"finish_reason":"stop"}]}`+"\n\n")
		fmt.Fprint(w, "data: [DONE]\n\n")
	}))
	defer server.Close()

	client := NewOpenAIClient(server.URL, "", 5*time.Second)
	var got []Delta
	err := client.StreamChatCompletion(context.Background(), CompletionRequest{Model: "m"}, func(d Delta) error {
		got = append(got, d)
		return nil
	})
	require.NoError(t, err)
	assert.Equal(t, []Delta{{Content: "hi"}}, got)
}

func TestClient_StreamChatCompletion_NonOKStatus(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusInternalServerError)
		w.Write([]byte("upstream exploded"))
	}))
	defer server.Close()

	client := NewOpenAIClient(server.URL, "", 5*time.Second)
	err := client.StreamChatCompletion(context.Background(), CompletionRequest{Model: "m"}, func(Delta) error { return nil })
	require.Error(t, err)
	assert.ErrorContains(t, err, "500")
}

func TestClient_StreamChatCompletion_StopsOnOnDeltaError(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/event-stream")
		writeSSE(w, "one", "")
		writeSSE(w, "two", "")
		writeSSE(w, "three", "")
		fmt.Fprint(w, "data: [DONE]\n\n")
	}))
	defer server.Close()

	client := NewOpenAIClient(server.URL, "", 5*time.Second)
	boom := errors.New("boom")
	var got []Delta
	err := client.StreamChatCompletion(context.Background(), CompletionRequest{Model: "m"}, func(d Delta) error {
		got = append(got, d)
		if len(got) == 2 {
			return boom
		}
		return nil
	})
	require.ErrorIs(t, err, boom)
	assert.Equal(t, []Delta{{Content: "one"}, {Content: "two"}}, got, "deltas delivered before the callback error must still have been received")
}

func TestClient_StreamChatCompletion_MalformedChunk(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/event-stream")
		writeSSE(w, "ok", "")
		fmt.Fprint(w, "data: {not valid json\n\n")
	}))
	defer server.Close()

	client := NewOpenAIClient(server.URL, "", 5*time.Second)
	var got []Delta
	err := client.StreamChatCompletion(context.Background(), CompletionRequest{Model: "m"}, func(d Delta) error {
		got = append(got, d)
		return nil
	})
	require.Error(t, err)
	assert.ErrorContains(t, err, "decoding")
	assert.Equal(t, []Delta{{Content: "ok"}}, got)
}

// writeToolCallSSE writes a single "chat.completion.chunk" SSE line
// carrying one streamed tool-call fragment at the given index, matching the
// real upstream shape confirmed against a live LM Studio server: id/name
// only present on a call's first fragment, arguments arriving as
// incremental string pieces.
func writeToolCallSSE(w http.ResponseWriter, index int, id, name, argsFragment string) {
	chunk := map[string]any{
		"id":     "chatcmpl-test",
		"object": "chat.completion.chunk",
		"choices": []map[string]any{{
			"index": 0,
			"delta": map[string]any{
				"tool_calls": []map[string]any{{
					"index":    index,
					"id":       id,
					"function": map[string]string{"name": name, "arguments": argsFragment},
				}},
			},
		}},
	}
	data, _ := json.Marshal(chunk)
	fmt.Fprintf(w, "data: %s\n\n", data)
	if f, ok := w.(http.Flusher); ok {
		f.Flush()
	}
}

func writeFinishReasonSSE(w http.ResponseWriter, reason string) {
	fmt.Fprintf(w, `data: {"choices":[{"index":0,"delta":{},"finish_reason":%q}]}`+"\n\n", reason)
	if f, ok := w.(http.Flusher); ok {
		f.Flush()
	}
}

func TestClient_StreamChatCompletion_AccumulatesFragmentedToolCallArguments(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/event-stream")
		writeToolCallSSE(w, 0, "call-1", "propose_context", "")
		writeToolCallSSE(w, 0, "", "", `{"objective":`)
		writeToolCallSSE(w, 0, "", "", `"ship login"}`)
		writeFinishReasonSSE(w, "tool_calls")
		fmt.Fprint(w, "data: [DONE]\n\n")
	}))
	defer server.Close()

	client := NewOpenAIClient(server.URL, "", 5*time.Second)
	var got []Delta
	err := client.StreamChatCompletion(context.Background(), CompletionRequest{Model: "m"}, func(d Delta) error {
		got = append(got, d)
		return nil
	})
	require.NoError(t, err)
	require.Len(t, got, 1)
	require.NotNil(t, got[0].ToolCall)
	assert.Equal(t, "call-1", got[0].ToolCall.ID)
	assert.Equal(t, "propose_context", got[0].ToolCall.Function.Name)
	assert.Equal(t, `{"objective":"ship login"}`, got[0].ToolCall.Function.Arguments)
}

func TestClient_StreamChatCompletion_FlushesMultipleToolCallsInOrder(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/event-stream")
		writeToolCallSSE(w, 0, "call-1", "first", `{}`)
		writeToolCallSSE(w, 1, "call-2", "second", `{}`)
		writeFinishReasonSSE(w, "tool_calls")
		fmt.Fprint(w, "data: [DONE]\n\n")
	}))
	defer server.Close()

	client := NewOpenAIClient(server.URL, "", 5*time.Second)
	var got []Delta
	err := client.StreamChatCompletion(context.Background(), CompletionRequest{Model: "m"}, func(d Delta) error {
		got = append(got, d)
		return nil
	})
	require.NoError(t, err)
	require.Len(t, got, 2)
	assert.Equal(t, "first", got[0].ToolCall.Function.Name)
	assert.Equal(t, "second", got[1].ToolCall.Function.Name)
}

func TestClient_StreamChatCompletion_NoToolCallDeltaWhenUpstreamOmitsToolCalls(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/event-stream")
		writeSSE(w, "hello", "")
		writeFinishReasonSSE(w, "stop")
		fmt.Fprint(w, "data: [DONE]\n\n")
	}))
	defer server.Close()

	client := NewOpenAIClient(server.URL, "", 5*time.Second)
	var got []Delta
	err := client.StreamChatCompletion(context.Background(), CompletionRequest{Model: "m"}, func(d Delta) error {
		got = append(got, d)
		return nil
	})
	require.NoError(t, err)
	assert.Equal(t, []Delta{{Content: "hello"}}, got)
}

// hangingBody never closes on its own — the handler blocks until the
// request context is cancelled, simulating a slow/unresponsive upstream so
// StreamChatCompletion's context-cancellation path can be exercised without
// a real network drop.
func TestClient_StreamChatCompletion_ContextCancelled(t *testing.T) {
	started := make(chan struct{})
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/event-stream")
		w.WriteHeader(http.StatusOK)
		if f, ok := w.(http.Flusher); ok {
			f.Flush()
		}
		close(started)
		<-r.Context().Done()
	}))
	defer server.Close()

	ctx, cancel := context.WithCancel(context.Background())
	client := NewOpenAIClient(server.URL, "", 5*time.Second)

	errCh := make(chan error, 1)
	go func() {
		errCh <- client.StreamChatCompletion(ctx, CompletionRequest{Model: "m"}, func(Delta) error { return nil })
	}()

	<-started
	cancel()
	require.Error(t, <-errCh)
}

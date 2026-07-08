package chat

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func writeOpenAISDKSSE(w http.ResponseWriter, content, reasoningContent string) {
	chunk := map[string]any{
		"id":     "chatcmpl-test",
		"object": "chat.completion.chunk",
		"model":  "m",
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

func writeOpenAISDKToolCallSSE(w http.ResponseWriter, index int, id, name, argsFragment string) {
	chunk := map[string]any{
		"id":     "chatcmpl-test",
		"object": "chat.completion.chunk",
		"model":  "m",
		"choices": []map[string]any{{
			"index": 0,
			"delta": map[string]any{
				"tool_calls": []map[string]any{{
					"index":    index,
					"id":       id,
					"type":     "function",
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

func writeOpenAISDKFinishSSE(w http.ResponseWriter, reason string) {
	fmt.Fprintf(w, `data: {"id":"chatcmpl-test","object":"chat.completion.chunk","model":"m","choices":[{"index":0,"delta":{},"finish_reason":%q}]}`+"\n\n", reason)
	if f, ok := w.(http.Flusher); ok {
		f.Flush()
	}
}

func TestOpenAISDKClient_CreateChatCompletion_RequestShape(t *testing.T) {
	var gotAuth string
	var gotBody map[string]any

	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotAuth = r.Header.Get("Authorization")
		require.NoError(t, json.NewDecoder(r.Body).Decode(&gotBody))

		resp := map[string]any{
			"id":    "chatcmpl-1",
			"model": "test-model",
			"choices": []map[string]any{{
				"index":         0,
				"message":       map[string]string{"role": "assistant", "content": "hello back"},
				"finish_reason": "stop",
			}},
		}
		w.Header().Set("Content-Type", "application/json")
		require.NoError(t, json.NewEncoder(w).Encode(resp))
	}))
	defer server.Close()

	client := NewOpenAISDKClient(server.URL, "test-key", 5*time.Second)
	req := CompletionRequest{
		Model:    "test-model",
		Messages: []Message{{Role: "user", Content: "hello"}},
	}
	resp, err := client.CreateChatCompletion(context.Background(), req)
	require.NoError(t, err)

	assert.Equal(t, "Bearer test-key", gotAuth)
	assert.False(t, gotBody["stream"] == true, "CreateChatCompletion must not stream")
	assert.Equal(t, "hello back", resp.Choices[0].Message.Content)
}

func TestOpenAISDKClient_NoAuthHeaderWhenKeyEmpty(t *testing.T) {
	var sawAuth bool
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		sawAuth = r.Header.Get("Authorization") != ""
		w.Header().Set("Content-Type", "application/json")
		json.NewEncoder(w).Encode(map[string]any{"id": "x", "model": "m", "choices": []map[string]any{{"message": map[string]string{"role": "assistant", "content": ""}}}})
	}))
	defer server.Close()

	client := NewOpenAISDKClient(server.URL, "", 5*time.Second)
	_, err := client.CreateChatCompletion(context.Background(), CompletionRequest{Model: "m"})
	require.NoError(t, err)
	assert.False(t, sawAuth, "expected no Authorization header when apiKey is empty")
}

func TestOpenAISDKClient_StreamChatCompletion_DeliversReasoningThenContentDeltas(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/event-stream")
		writeOpenAISDKSSE(w, "", "Think")
		writeOpenAISDKSSE(w, "", "ing...")
		writeOpenAISDKSSE(w, "Hello", "")
		writeOpenAISDKSSE(w, " world", "")
		writeOpenAISDKFinishSSE(w, "stop")
		fmt.Fprint(w, "data: [DONE]\n\n")
	}))
	defer server.Close()

	client := NewOpenAISDKClient(server.URL, "", 5*time.Second)
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

func TestOpenAISDKClient_StreamChatCompletion_AccumulatesFragmentedToolCallArguments(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/event-stream")
		writeOpenAISDKToolCallSSE(w, 0, "call-1", "propose_context", "")
		writeOpenAISDKToolCallSSE(w, 0, "", "", `{"objective":`)
		writeOpenAISDKToolCallSSE(w, 0, "", "", `"ship login"}`)
		writeOpenAISDKFinishSSE(w, "tool_calls")
		fmt.Fprint(w, "data: [DONE]\n\n")
	}))
	defer server.Close()

	client := NewOpenAISDKClient(server.URL, "", 5*time.Second)
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

func TestOpenAISDKClient_StreamChatCompletion_NonOKStatus(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusInternalServerError)
		w.Write([]byte(`{"error":{"message":"upstream exploded"}}`))
	}))
	defer server.Close()

	client := NewOpenAISDKClient(server.URL, "", 5*time.Second)
	err := client.StreamChatCompletion(context.Background(), CompletionRequest{Model: "m"}, func(Delta) error { return nil })
	require.Error(t, err)
}

func TestOpenAISDKClient_StreamSessionTurn_ReplaysPriorToolCallAsAssistantAndSyntheticToolMessage(t *testing.T) {
	var requests []map[string]any
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var body map[string]any
		require.NoError(t, json.NewDecoder(r.Body).Decode(&body))
		requests = append(requests, body)

		w.Header().Set("Content-Type", "text/event-stream")
		if len(requests) == 1 {
			writeOpenAISDKToolCallSSE(w, 0, "call-1", "propose_context", `{"objective":"x"}`)
			writeOpenAISDKFinishSSE(w, "tool_calls")
		} else {
			writeOpenAISDKSSE(w, "ok", "")
			writeOpenAISDKFinishSSE(w, "stop")
		}
		fmt.Fprint(w, "data: [DONE]\n\n")
	}))
	defer server.Close()

	tools := []Tool{{Type: "function", Function: ToolSchema{Name: "propose_context"}}}
	client := NewOpenAISDKClient(server.URL, "", 5*time.Second)

	_, toolCall, err := client.StreamSessionTurn(context.Background(), "sess-1", "sys", "m", "let's start", tools, func(Delta) error { return nil })
	require.NoError(t, err)
	require.NotNil(t, toolCall)
	assert.Equal(t, "call-1", toolCall.ID)

	_, _, err = client.StreamSessionTurn(context.Background(), "sess-1", "sys", "m", "one more thing", tools, func(Delta) error { return nil })
	require.NoError(t, err)

	require.Len(t, requests, 2)
	messages, ok := requests[1]["messages"].([]any)
	require.True(t, ok)
	require.Len(t, messages, 5)
	assistantMsg := messages[2].(map[string]any)
	assert.Equal(t, "assistant", assistantMsg["role"])
	toolCalls, ok := assistantMsg["tool_calls"].([]any)
	require.True(t, ok)
	require.Len(t, toolCalls, 1)
	toolMsg := messages[3].(map[string]any)
	assert.Equal(t, "tool", toolMsg["role"])
	assert.Equal(t, "call-1", toolMsg["tool_call_id"])
}

func TestOpenAISDKClient_CloseSession_DiscardsHistory(t *testing.T) {
	var requests []map[string]any
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var body map[string]any
		require.NoError(t, json.NewDecoder(r.Body).Decode(&body))
		requests = append(requests, body)
		w.Header().Set("Content-Type", "text/event-stream")
		writeOpenAISDKFinishSSE(w, "stop")
		fmt.Fprint(w, "data: [DONE]\n\n")
	}))
	defer server.Close()

	client := NewOpenAISDKClient(server.URL, "", 5*time.Second)
	_, _, err := client.StreamSessionTurn(context.Background(), "sess-1", "", "m", "first", nil, func(Delta) error { return nil })
	require.NoError(t, err)

	client.CloseSession("sess-1")

	_, _, err = client.StreamSessionTurn(context.Background(), "sess-1", "", "m", "after close", nil, func(Delta) error { return nil })
	require.NoError(t, err)

	require.Len(t, requests, 2)
	messages, ok := requests[1]["messages"].([]any)
	require.True(t, ok)
	assert.Len(t, messages, 1, "CloseSession must discard the prior turn's history")
}

func TestOpenAISDKClient_ListModels_OK(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		assert.Contains(t, r.URL.Path, "/models")
		w.Header().Set("Content-Type", "application/json")
		json.NewEncoder(w).Encode(map[string]any{
			"object": "list",
			"data": []map[string]any{
				{"id": "llama3", "object": "model"},
				{"id": "mistral", "object": "model"},
			},
		})
	}))
	defer server.Close()

	client := NewOpenAISDKClient(server.URL, "", 5*time.Second)
	models, err := client.ListModels(context.Background())
	require.NoError(t, err)
	assert.Equal(t, []string{"llama3", "mistral"}, models)
}

func TestOpenAISDKClient_CheckHealth_UsesListModels(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		assert.Contains(t, r.URL.Path, "/models")
		w.Header().Set("Content-Type", "application/json")
		json.NewEncoder(w).Encode(map[string]any{"object": "list", "data": []map[string]any{}})
	}))
	defer server.Close()

	client := NewOpenAISDKClient(server.URL, "", 5*time.Second)
	require.NoError(t, client.CheckHealth(context.Background()))
}

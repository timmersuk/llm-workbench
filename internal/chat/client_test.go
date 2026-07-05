package chat

import (
	"context"
	"encoding/json"
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

	client := NewClient(server.URL, "test-key", 5*time.Second)
	req := CompletionRequest{
		Model:    "test-model",
		Messages: []Message{{Role: "user", Content: "hello"}},
	}

	resp, err := client.CreateChatCompletion(context.Background(), req)
	require.NoError(t, err)

	assert.Equal(t, "Bearer test-key", gotAuth)
	assert.Equal(t, req, gotReq)
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

	client := NewClient(server.URL, "", 5*time.Second)
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

	client := NewClient(server.URL, "", 5*time.Second)
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

	client := NewClient(server.URL, "", 5*time.Second)
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

	client := NewClient(server.URL, "", 5*time.Second)
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

	client := NewClient(server.URL, "", 5*time.Second)
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

	client := NewClient(server.URL, "", 5*time.Second)
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

	client := NewClient(server.URL, "", 5*time.Second)
	_, err := client.CreateChatCompletion(ctx, CompletionRequest{})
	require.Error(t, err)
}

package api

import (
	"bytes"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/mock"
	"github.com/stretchr/testify/require"

	"github.com/timmersuk/llm-workbench/internal/chat"
)

func TestHandleChatCompletions_OK(t *testing.T) {
	completer := new(mockChatCompleter)
	reqBody := chat.CompletionRequest{Model: "test", Messages: []chat.Message{{Role: "user", Content: "hi"}}}
	respBody := chat.CompletionResponse{ID: "1", Choices: []chat.Choice{{Message: chat.Message{Role: "assistant", Content: "hello"}}}}
	completer.On("CreateChatCompletion", mock.Anything, reqBody).Return(respBody, nil)

	body, _ := json.Marshal(reqBody)
	req := httptest.NewRequest(http.MethodPost, "/api/v1/chat/completions", bytes.NewReader(body))
	w := httptest.NewRecorder()
	handleChatCompletions(completer)(w, req)

	require.Equal(t, http.StatusOK, w.Code)
	var got chat.CompletionResponse
	require.NoError(t, json.Unmarshal(w.Body.Bytes(), &got))
	assert.Equal(t, "hello", got.Choices[0].Message.Content)
}

func TestHandleChatCompletions_BadRequestBody(t *testing.T) {
	completer := new(mockChatCompleter)

	req := httptest.NewRequest(http.MethodPost, "/api/v1/chat/completions", bytes.NewReader([]byte("{not json")))
	w := httptest.NewRecorder()
	handleChatCompletions(completer)(w, req)

	assert.Equal(t, http.StatusBadRequest, w.Code)
	completer.AssertNotCalled(t, "CreateChatCompletion", mock.Anything, mock.Anything)
}

func TestHandleChatCompletions_UpstreamError(t *testing.T) {
	completer := new(mockChatCompleter)
	completer.On("CreateChatCompletion", mock.Anything, mock.Anything).Return(nil, errors.New("upstream down"))

	body, _ := json.Marshal(chat.CompletionRequest{})
	req := httptest.NewRequest(http.MethodPost, "/api/v1/chat/completions", bytes.NewReader(body))
	w := httptest.NewRecorder()
	handleChatCompletions(completer)(w, req)

	assert.Equal(t, http.StatusBadGateway, w.Code)
}

func TestHandleListModels_OK(t *testing.T) {
	completer := new(mockChatCompleter)
	completer.On("ListModels", mock.Anything).Return([]string{"llama3", "mistral"}, nil)

	req := httptest.NewRequest(http.MethodGet, "/api/v1/chat/models", nil)
	w := httptest.NewRecorder()
	handleListModels(completer)(w, req)

	require.Equal(t, http.StatusOK, w.Code)
	var got struct {
		Models []string `json:"models"`
	}
	require.NoError(t, json.Unmarshal(w.Body.Bytes(), &got))
	assert.Equal(t, []string{"llama3", "mistral"}, got.Models)
}

func TestHandleListModels_UpstreamError(t *testing.T) {
	completer := new(mockChatCompleter)
	completer.On("ListModels", mock.Anything).Return(nil, errors.New("upstream down"))

	req := httptest.NewRequest(http.MethodGet, "/api/v1/chat/models", nil)
	w := httptest.NewRecorder()
	handleListModels(completer)(w, req)

	assert.Equal(t, http.StatusBadGateway, w.Code)
}

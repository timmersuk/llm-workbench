package api

import (
	"bytes"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/mock"
	"github.com/stretchr/testify/require"

	"github.com/timmersuk/llm-workbench/internal/chat"
)

// parseSSEEvents decodes a "data: {...}\n\n"-per-line SSE body into the
// chatStreamEvents it carries, in order.
func parseSSEEvents(t *testing.T, body string) []chatStreamEvent {
	t.Helper()
	var events []chatStreamEvent
	for _, line := range strings.Split(body, "\n") {
		data, ok := strings.CutPrefix(line, "data: ")
		if !ok {
			continue
		}
		var ev chatStreamEvent
		require.NoError(t, json.Unmarshal([]byte(data), &ev))
		events = append(events, ev)
	}
	return events
}

func TestHandleChatCompletions_OK(t *testing.T) {
	completer := new(mockChatCompleter)
	reqBody := chat.CompletionRequest{Model: "test", Messages: []chat.Message{{Role: "user", Content: "hi"}}}
	completer.On("StreamChatCompletion", mock.Anything, reqBody, mock.Anything).
		Return([]chat.Delta{{ReasoningContent: "thinking"}, {Content: "hello"}}, nil)

	body, _ := json.Marshal(reqBody)
	req := httptest.NewRequest(http.MethodPost, "/api/v1/chat/completions", bytes.NewReader(body))
	w := httptest.NewRecorder()
	handleChatCompletions(completer)(w, req)

	require.Equal(t, http.StatusOK, w.Code)
	assert.Equal(t, "text/event-stream", w.Header().Get("Content-Type"))
	assert.Equal(t, []chatStreamEvent{
		{ReasoningContent: "thinking"},
		{Content: "hello"},
	}, parseSSEEvents(t, w.Body.String()))
}

func TestHandleChatCompletions_BadRequestBody(t *testing.T) {
	completer := new(mockChatCompleter)

	req := httptest.NewRequest(http.MethodPost, "/api/v1/chat/completions", bytes.NewReader([]byte("{not json")))
	w := httptest.NewRecorder()
	handleChatCompletions(completer)(w, req)

	assert.Equal(t, http.StatusBadRequest, w.Code)
	completer.AssertNotCalled(t, "StreamChatCompletion", mock.Anything, mock.Anything, mock.Anything)
}

func TestHandleChatCompletions_MidStreamErrorKeepsPartialContent(t *testing.T) {
	completer := new(mockChatCompleter)
	reqBody := chat.CompletionRequest{}
	completer.On("StreamChatCompletion", mock.Anything, reqBody, mock.Anything).
		Return([]chat.Delta{{Content: "partial "}, {Content: "answer"}}, errors.New("upstream connection dropped"))

	body, _ := json.Marshal(reqBody)
	req := httptest.NewRequest(http.MethodPost, "/api/v1/chat/completions", bytes.NewReader(body))
	w := httptest.NewRecorder()
	handleChatCompletions(completer)(w, req)

	// Headers are already committed to 200 by the time a mid-stream error
	// happens, so the failure surfaces as a final SSE event, not an HTTP
	// error status — and everything streamed before it stays in the body.
	require.Equal(t, http.StatusOK, w.Code)
	assert.Equal(t, []chatStreamEvent{
		{Content: "partial "},
		{Content: "answer"},
		{Error: "upstream connection dropped"},
	}, parseSSEEvents(t, w.Body.String()))
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

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

	"github.com/timmersuk/llm-workbench/internal/agentrunner"
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
	runner := new(mockAgentRunner)
	runner.On("Run", mock.Anything, mock.MatchedBy(func(in agentrunner.RunInput) bool {
		return in.SessionKey == "sess-1" && in.UserMessage == "hi" && in.Model == "test" && in.Workspace == "/repos"
	}), mock.Anything).Return([]chat.Delta{{ReasoningContent: "thinking"}, {Content: "hello"}}, agentrunner.RunOutput{Content: "hello"}, nil)

	reqBody := chatCompletionRequest{Content: "hi", Model: "test", SessionKey: "sess-1"}
	body, _ := json.Marshal(reqBody)
	req := httptest.NewRequest(http.MethodPost, "/api/v1/chat/completions", bytes.NewReader(body))
	w := httptest.NewRecorder()
	(&Server{AgentRunners: map[string]agentrunner.AgentRunner{"local": runner}, ReposRoot: "/repos"}).handleChatCompletions()(w, req)

	require.Equal(t, http.StatusOK, w.Code)
	assert.Equal(t, "text/event-stream", w.Header().Get("Content-Type"))
	assert.Equal(t, []chatStreamEvent{
		{ReasoningContent: "thinking"},
		{Content: "hello"},
	}, parseSSEEvents(t, w.Body.String()))
}

func TestHandleChatCompletions_DefaultsToLocalExecutorWhenExecutorOmitted(t *testing.T) {
	runner := new(mockAgentRunner)
	runner.On("Run", mock.Anything, mock.Anything, mock.Anything).Return(nil, agentrunner.RunOutput{}, nil)

	reqBody := chatCompletionRequest{Content: "hi", SessionKey: "sess-1"}
	body, _ := json.Marshal(reqBody)
	req := httptest.NewRequest(http.MethodPost, "/api/v1/chat/completions", bytes.NewReader(body))
	w := httptest.NewRecorder()
	(&Server{AgentRunners: map[string]agentrunner.AgentRunner{"local": runner}}).handleChatCompletions()(w, req)

	require.Equal(t, http.StatusOK, w.Code)
	runner.AssertCalled(t, "Run", mock.Anything, mock.Anything, mock.Anything)
}

func TestHandleChatCompletions_UnknownExecutor_BadRequest(t *testing.T) {
	reqBody := chatCompletionRequest{Content: "hi", SessionKey: "sess-1", Executor: "does-not-exist"}
	body, _ := json.Marshal(reqBody)
	req := httptest.NewRequest(http.MethodPost, "/api/v1/chat/completions", bytes.NewReader(body))
	w := httptest.NewRecorder()
	(&Server{AgentRunners: map[string]agentrunner.AgentRunner{}}).handleChatCompletions()(w, req)

	assert.Equal(t, http.StatusBadRequest, w.Code)
}

func TestHandleChatCompletions_MissingSessionKey_BadRequest(t *testing.T) {
	reqBody := chatCompletionRequest{Content: "hi"}
	body, _ := json.Marshal(reqBody)
	req := httptest.NewRequest(http.MethodPost, "/api/v1/chat/completions", bytes.NewReader(body))
	w := httptest.NewRecorder()
	(&Server{AgentRunners: map[string]agentrunner.AgentRunner{}}).handleChatCompletions()(w, req)

	assert.Equal(t, http.StatusBadRequest, w.Code)
}

func TestHandleChatCompletions_BadRequestBody(t *testing.T) {
	runner := new(mockAgentRunner)

	req := httptest.NewRequest(http.MethodPost, "/api/v1/chat/completions", bytes.NewReader([]byte("{not json")))
	w := httptest.NewRecorder()
	(&Server{AgentRunners: map[string]agentrunner.AgentRunner{"local": runner}}).handleChatCompletions()(w, req)

	assert.Equal(t, http.StatusBadRequest, w.Code)
	runner.AssertNotCalled(t, "Run", mock.Anything, mock.Anything, mock.Anything)
}

func TestHandleChatCompletions_MidStreamErrorKeepsPartialContent(t *testing.T) {
	runner := new(mockAgentRunner)
	runner.On("Run", mock.Anything, mock.Anything, mock.Anything).
		Return([]chat.Delta{{Content: "partial "}, {Content: "answer"}}, agentrunner.RunOutput{Content: "partial answer"}, errors.New("upstream connection dropped"))

	reqBody := chatCompletionRequest{Content: "hi", SessionKey: "sess-1"}
	body, _ := json.Marshal(reqBody)
	req := httptest.NewRequest(http.MethodPost, "/api/v1/chat/completions", bytes.NewReader(body))
	w := httptest.NewRecorder()
	(&Server{AgentRunners: map[string]agentrunner.AgentRunner{"local": runner}}).handleChatCompletions()(w, req)

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

func TestHandleCloseChatSession_ClosesAcrossAllRegisteredRunners(t *testing.T) {
	claudeRunner := new(mockAgentRunner)
	claudeRunner.On("CloseSession", "sess-1")
	localRunner := new(mockAgentRunner)
	localRunner.On("CloseSession", "sess-1")

	reqBody := chatSessionCloseRequest{SessionKey: "sess-1"}
	body, _ := json.Marshal(reqBody)
	req := httptest.NewRequest(http.MethodPost, "/api/v1/chat/sessions/close", bytes.NewReader(body))
	w := httptest.NewRecorder()
	(&Server{AgentRunners: map[string]agentrunner.AgentRunner{"claude-code": claudeRunner, "local": localRunner}}).handleCloseChatSession()(w, req)

	assert.Equal(t, http.StatusNoContent, w.Code)
	claudeRunner.AssertCalled(t, "CloseSession", "sess-1")
	localRunner.AssertCalled(t, "CloseSession", "sess-1")
}

func TestHandleCloseChatSession_MissingSessionKey_BadRequest(t *testing.T) {
	req := httptest.NewRequest(http.MethodPost, "/api/v1/chat/sessions/close", bytes.NewReader([]byte("{}")))
	w := httptest.NewRecorder()
	(&Server{AgentRunners: map[string]agentrunner.AgentRunner{}}).handleCloseChatSession()(w, req)

	assert.Equal(t, http.StatusBadRequest, w.Code)
}

func TestHandleListModels_OK(t *testing.T) {
	runner := new(mockAgentRunner)
	runner.On("ListModels", mock.Anything).Return([]string{"llama3", "mistral"}, nil)

	req := httptest.NewRequest(http.MethodGet, "/api/v1/chat/models", nil)
	w := httptest.NewRecorder()
	(&Server{AgentRunners: map[string]agentrunner.AgentRunner{"local": runner}}).handleListModels()(w, req)

	require.Equal(t, http.StatusOK, w.Code)
	var got struct {
		Models []string `json:"models"`
	}
	require.NoError(t, json.Unmarshal(w.Body.Bytes(), &got))
	assert.Equal(t, []string{"llama3", "mistral"}, got.Models)
}

func TestHandleListModels_UpstreamError(t *testing.T) {
	runner := new(mockAgentRunner)
	runner.On("ListModels", mock.Anything).Return(nil, errors.New("upstream down"))

	req := httptest.NewRequest(http.MethodGet, "/api/v1/chat/models", nil)
	w := httptest.NewRecorder()
	(&Server{AgentRunners: map[string]agentrunner.AgentRunner{"local": runner}}).handleListModels()(w, req)

	assert.Equal(t, http.StatusBadGateway, w.Code)
}

func TestHandleListModels_NoLocalExecutorRegistered(t *testing.T) {
	req := httptest.NewRequest(http.MethodGet, "/api/v1/chat/models", nil)
	w := httptest.NewRecorder()
	(&Server{AgentRunners: map[string]agentrunner.AgentRunner{}}).handleListModels()(w, req)

	assert.Equal(t, http.StatusBadRequest, w.Code)
}

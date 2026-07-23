package api

import (
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/mock"
	"github.com/stretchr/testify/require"

	"github.com/timmersuk/llm-workbench/internal/agentrunner"
)

func TestHandleListAgentExecutors_ReportsHealthyExecutors(t *testing.T) {
	runner := new(mockAgentRunner)
	runner.On("CheckHealth", mock.Anything).Return(nil)

	req := httptest.NewRequest(http.MethodGet, "/api/v1/agent-executors", nil)
	w := httptest.NewRecorder()
	(&Server{AgentRunners: map[string]agentrunner.AgentRunner{"claude-code": runner}}).handleListAgentExecutors()(w, req)

	require.Equal(t, http.StatusOK, w.Code)
	var got struct {
		Executors []string `json:"executors"`
	}
	require.NoError(t, json.Unmarshal(w.Body.Bytes(), &got))
	assert.Equal(t, []string{"claude-code"}, got.Executors)
}

func TestHandleListAgentExecutors_ExcludesExecutorsThatFailCheckHealth(t *testing.T) {
	healthy := new(mockAgentRunner)
	healthy.On("CheckHealth", mock.Anything).Return(nil)
	unhealthy := new(mockAgentRunner)
	unhealthy.On("CheckHealth", mock.Anything).Return(errors.New("claude CLI not found on PATH"))

	req := httptest.NewRequest(http.MethodGet, "/api/v1/agent-executors", nil)
	w := httptest.NewRecorder()
	(&Server{AgentRunners: map[string]agentrunner.AgentRunner{"claude-code": unhealthy, "local": healthy}}).handleListAgentExecutors()(w, req)

	require.Equal(t, http.StatusOK, w.Code)
	var got struct {
		Executors []string `json:"executors"`
	}
	require.NoError(t, json.Unmarshal(w.Body.Bytes(), &got))
	assert.Equal(t, []string{"local"}, got.Executors)
}

func TestHandleListAgentExecutors_EmptyWhenNoneEnabled(t *testing.T) {
	req := httptest.NewRequest(http.MethodGet, "/api/v1/agent-executors", nil)
	w := httptest.NewRecorder()
	(&Server{}).handleListAgentExecutors()(w, req)

	require.Equal(t, http.StatusOK, w.Code)
	var got struct {
		Executors []string `json:"executors"`
	}
	require.NoError(t, json.Unmarshal(w.Body.Bytes(), &got))
	assert.Empty(t, got.Executors)
}

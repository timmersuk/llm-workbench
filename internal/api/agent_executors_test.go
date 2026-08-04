package api

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/timmersuk/llm-workbench/internal/agentrunner"
)

func TestHandleListAgentExecutors_ReportsCapabilities(t *testing.T) {
	runner := new(mockAgentRunner)

	req := httptest.NewRequest(http.MethodGet, "/api/v1/agent-executors", nil)
	w := httptest.NewRecorder()
	(&Server{AgentRunners: map[string]agentrunner.AgentRunner{"claude-code": runner}}).handleListAgentExecutors()(w, req)

	require.Equal(t, http.StatusOK, w.Code)
	var got struct {
		Executors []agentrunner.ExecutorCapabilities `json:"executors"`
	}
	require.NoError(t, json.Unmarshal(w.Body.Bytes(), &got))
	require.Len(t, got.Executors, 1)
	assert.Equal(t, "claude-code", got.Executors[0].Name)
	assert.Equal(t, []agentrunner.ReasoningEffort{"low", "medium", "high"}, got.Executors[0].Efforts)
}

func TestHandleListAgentExecutors_DoesNotHealthFilter(t *testing.T) {
	healthy := new(mockAgentRunner)
	unhealthy := new(mockAgentRunner)

	req := httptest.NewRequest(http.MethodGet, "/api/v1/agent-executors", nil)
	w := httptest.NewRecorder()
	(&Server{AgentRunners: map[string]agentrunner.AgentRunner{"claude-code": unhealthy, "local": healthy}}).handleListAgentExecutors()(w, req)

	require.Equal(t, http.StatusOK, w.Code)
	var got struct {
		Executors []agentrunner.ExecutorCapabilities `json:"executors"`
	}
	require.NoError(t, json.Unmarshal(w.Body.Bytes(), &got))
	assert.Equal(t, []string{"claude-code", "local"}, []string{got.Executors[0].Name, got.Executors[1].Name})
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

package api

// Integration tests wire real collaborators (FileStore, chat.Client) into
// the real router and drive it over a real net.Listener with a real
// http.Client — unlike the rest of this package's tests, which exercise
// handlers/the mux with mocks and httptest.NewRecorder. This is the layer
// that catches real wiring bugs (interface mismatches, real YAML
// round-tripping, real path-traversal validation) that mock-based unit
// tests can't.

import (
	"bytes"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/timmersuk/llm-workbench/internal/chat"
	"github.com/timmersuk/llm-workbench/internal/project"
	"github.com/timmersuk/llm-workbench/internal/task"
)

const integrationTaskYAML = `
id: TASK-0001
title: Do the thing
project: demo-project
status: draft
stage: requirements
created_at: 2026-07-05T00:00:00Z
updated_at: 2026-07-05T00:00:00Z
objective: Ship it
constraints: []
assumptions: []
success_criteria: []
references:
  knowledge: []
  repo: []
`

const integrationProjectYAML = `
id: demo-project
name: Demo Project
description: A demo project
repositories:
  - github.com/org/demo
knowledge: []
constraints: []
created_at: 2026-07-05T00:00:00Z
updated_at: 2026-07-05T00:00:00Z
`

// newIntegrationServer boots a real router backed by real FileStores (seeded
// with one fixture task/project each) and a real *chat.Client pointed at a
// fake upstream, then serves it over a real net.Listener. Callers get back a
// ready-to-use base URL and a cleanup-registered *chat.Client so upstream
// behavior (e.g. closing the fake server) can be adjusted per test.
func newIntegrationServer(t *testing.T, upstream *httptest.Server) (baseURL string, chatClient *chat.Client) {
	t.Helper()

	root := t.TempDir()
	taskRoot := filepath.Join(root, "tasks", "TASK-0001")
	require.NoError(t, os.MkdirAll(taskRoot, 0o755))
	require.NoError(t, os.WriteFile(filepath.Join(taskRoot, "task.yaml"), []byte(integrationTaskYAML), 0o644))

	projectRoot := filepath.Join(root, "projects", "demo-project")
	require.NoError(t, os.MkdirAll(projectRoot, 0o755))
	require.NoError(t, os.WriteFile(filepath.Join(projectRoot, "project.yaml"), []byte(integrationProjectYAML), 0o644))

	taskStore := task.NewFileStore(filepath.Join(root, "tasks"))
	projectStore := project.NewFileStore(filepath.Join(root, "projects"))
	chatClient = chat.NewClient(upstream.URL, "test-key", 5*time.Second)

	router := NewRouter(taskStore, projectStore, chatClient, testFrontendFS(), "test-build")
	server := httptest.NewServer(router)
	t.Cleanup(server.Close)

	return server.URL, chatClient
}

// fakeUpstream serves both the OpenAI-compatible /chat/completions endpoint
// and the /models endpoint chat.Client.CheckHealth probes.
func fakeUpstream(t *testing.T) *httptest.Server {
	t.Helper()
	return httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/models":
			w.WriteHeader(http.StatusOK)
		case "/chat/completions":
			w.Header().Set("Content-Type", "application/json")
			_ = json.NewEncoder(w).Encode(chat.CompletionResponse{
				ID:    "chatcmpl-1",
				Model: "test-model",
				Choices: []chat.Choice{{
					Index:        0,
					Message:      chat.Message{Role: "assistant", Content: "hello back"},
					FinishReason: "stop",
				}},
			})
		default:
			http.NotFound(w, r)
		}
	}))
}

func TestIntegration_TasksAndProjectsServedFromRealStores(t *testing.T) {
	upstream := fakeUpstream(t)
	defer upstream.Close()
	baseURL, _ := newIntegrationServer(t, upstream)
	client := &http.Client{Timeout: 5 * time.Second}

	resp, err := client.Get(baseURL + "/api/v1/tasks")
	require.NoError(t, err)
	defer resp.Body.Close()
	require.Equal(t, http.StatusOK, resp.StatusCode)
	var taskList task.ListResult
	require.NoError(t, json.NewDecoder(resp.Body).Decode(&taskList))
	require.Len(t, taskList.Tasks, 1)
	assert.Equal(t, "TASK-0001", taskList.Tasks[0].ID)
	assert.Empty(t, taskList.Errors)

	resp, err = client.Get(baseURL + "/api/v1/tasks/TASK-0001")
	require.NoError(t, err)
	defer resp.Body.Close()
	assert.Equal(t, http.StatusOK, resp.StatusCode)

	resp, err = client.Get(baseURL + "/api/v1/tasks/TASK-9999")
	require.NoError(t, err)
	defer resp.Body.Close()
	assert.Equal(t, http.StatusNotFound, resp.StatusCode)

	resp, err = client.Get(baseURL + "/api/v1/tasks/not-a-task-id")
	require.NoError(t, err)
	defer resp.Body.Close()
	assert.Equal(t, http.StatusBadRequest, resp.StatusCode)

	resp, err = client.Get(baseURL + "/api/v1/projects")
	require.NoError(t, err)
	defer resp.Body.Close()
	assert.Equal(t, http.StatusOK, resp.StatusCode)

	resp, err = client.Get(baseURL + "/api/v1/projects/demo-project")
	require.NoError(t, err)
	defer resp.Body.Close()
	assert.Equal(t, http.StatusOK, resp.StatusCode)

	resp, err = client.Get(baseURL + "/api/v1/projects/nonexistent")
	require.NoError(t, err)
	defer resp.Body.Close()
	assert.Equal(t, http.StatusNotFound, resp.StatusCode)
}

func TestIntegration_TasksListSkipsMalformedEntryWithErrorSignal(t *testing.T) {
	upstream := fakeUpstream(t)
	defer upstream.Close()

	root := t.TempDir()
	validDir := filepath.Join(root, "tasks", "TASK-0001")
	require.NoError(t, os.MkdirAll(validDir, 0o755))
	require.NoError(t, os.WriteFile(filepath.Join(validDir, "task.yaml"), []byte(integrationTaskYAML), 0o644))

	brokenDir := filepath.Join(root, "tasks", "TASK-0002")
	require.NoError(t, os.MkdirAll(brokenDir, 0o755))
	require.NoError(t, os.WriteFile(filepath.Join(brokenDir, "task.yaml"), []byte("id: [not valid yaml"), 0o644))

	projectRoot := filepath.Join(root, "projects", "demo-project")
	require.NoError(t, os.MkdirAll(projectRoot, 0o755))
	require.NoError(t, os.WriteFile(filepath.Join(projectRoot, "project.yaml"), []byte(integrationProjectYAML), 0o644))

	taskStore := task.NewFileStore(filepath.Join(root, "tasks"))
	projectStore := project.NewFileStore(filepath.Join(root, "projects"))
	chatClient := chat.NewClient(upstream.URL, "test-key", 5*time.Second)

	router := NewRouter(taskStore, projectStore, chatClient, testFrontendFS(), "test-build")
	server := httptest.NewServer(router)
	defer server.Close()

	resp, err := http.Get(server.URL + "/api/v1/tasks")
	require.NoError(t, err)
	defer resp.Body.Close()
	require.Equal(t, http.StatusOK, resp.StatusCode, "one malformed task must not fail the whole listing")

	var got task.ListResult
	require.NoError(t, json.NewDecoder(resp.Body).Decode(&got))

	require.Len(t, got.Tasks, 1)
	assert.Equal(t, "TASK-0001", got.Tasks[0].ID)

	require.Len(t, got.Errors, 1)
	assert.Equal(t, "TASK-0002", got.Errors[0].ID)
	assert.Contains(t, got.Errors[0].Error, "parsing")
}

func TestIntegration_ChatCompletionsRoundTripsThroughRealClient(t *testing.T) {
	upstream := fakeUpstream(t)
	defer upstream.Close()
	baseURL, _ := newIntegrationServer(t, upstream)

	body, err := json.Marshal(chat.CompletionRequest{
		Model:    "test-model",
		Messages: []chat.Message{{Role: "user", Content: "hello"}},
	})
	require.NoError(t, err)

	resp, err := http.Post(baseURL+"/api/v1/chat/completions", "application/json", bytes.NewReader(body))
	require.NoError(t, err)
	defer resp.Body.Close()
	require.Equal(t, http.StatusOK, resp.StatusCode)

	var got chat.CompletionResponse
	require.NoError(t, json.NewDecoder(resp.Body).Decode(&got))
	require.Len(t, got.Choices, 1)
	assert.Equal(t, "hello back", got.Choices[0].Message.Content)
}

func TestIntegration_HealthcheckReflectsRealChatClient(t *testing.T) {
	upstream := fakeUpstream(t)
	baseURL, _ := newIntegrationServer(t, upstream)
	client := &http.Client{Timeout: 5 * time.Second}

	resp, err := client.Get(baseURL + "/healthcheck")
	require.NoError(t, err)
	resp.Body.Close()
	assert.Equal(t, http.StatusOK, resp.StatusCode)

	// Once the upstream is gone, the real chat.Client's error should surface
	// through the healthcheck rather than a generic failure.
	upstream.Close()

	resp, err = client.Get(baseURL + "/healthcheck")
	require.NoError(t, err)
	defer resp.Body.Close()
	assert.Equal(t, http.StatusServiceUnavailable, resp.StatusCode)

	var got map[string]string
	require.NoError(t, json.NewDecoder(resp.Body).Decode(&got))
	assert.Equal(t, "error", got["status"])
	assert.NotEmpty(t, got["error"])
}

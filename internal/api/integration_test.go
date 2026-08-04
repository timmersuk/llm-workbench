package api

// Integration tests wire real collaborators (FileStore, chat.ChatClient) into
// the real router and drive it over a real net.Listener with a real
// http.Client — unlike the rest of this package's tests, which exercise
// handlers/the mux with mocks and httptest.NewRecorder. This is the layer
// that catches real wiring bugs (interface mismatches, real YAML
// round-tripping, real path-traversal validation) that mock-based unit
// tests can't.

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/timmersuk/llm-workbench/internal/agentrunner"
	"github.com/timmersuk/llm-workbench/internal/chat"
	"github.com/timmersuk/llm-workbench/internal/knowledge"
	"github.com/timmersuk/llm-workbench/internal/project"
	"github.com/timmersuk/llm-workbench/internal/task"
)

const integrationTaskYAML = `
id: TASK-0001
title: Do the thing
project: demo-project
stage: requirements
created_at: 2026-07-05T00:00:00Z
updated_at: 2026-07-05T00:00:00Z
objective: Ship it
constraints: []
assumptions: []
success_criteria: []
agent_defaults:
  stage_conversation:
    executor: local
    model: test-model
    effort: medium
  execution:
    executor: local
    model: test-model
    effort: medium
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
default_branch: main
knowledge: []
constraints: []
created_at: 2026-07-05T00:00:00Z
updated_at: 2026-07-05T00:00:00Z
`

// newIntegrationServer boots a real router backed by a real project
// FileStore (seeded with one fixture project owning one fixture task) and a
// real chat.ChatClient pointed at a fake upstream, then serves it over a real
// net.Listener. Callers get back a ready-to-use base URL and a
// cleanup-registered chat.ChatClient so upstream behavior (e.g. closing the
// fake server) can be adjusted per test.
func newIntegrationServer(t *testing.T, upstream *httptest.Server) (baseURL string, chatClient chat.ChatClient) {
	t.Helper()

	root := t.TempDir()
	projectRoot := filepath.Join(root, "projects", "demo-project")
	require.NoError(t, os.MkdirAll(projectRoot, 0o755))
	require.NoError(t, os.WriteFile(filepath.Join(projectRoot, "project.yaml"), []byte(integrationProjectYAML), 0o644))

	taskRoot := filepath.Join(projectRoot, "tasks", "TASK-0001")
	require.NoError(t, os.MkdirAll(taskRoot, 0o755))
	require.NoError(t, os.WriteFile(filepath.Join(taskRoot, "task.yaml"), []byte(integrationTaskYAML), 0o644))

	// integrationProjectYAML's repositories: [github.com/org/demo] resolves
	// (agentrunner.ResolveWorkspace) to reposRoot/demo — every stage-message
	// executor, including "local", now requires this to exist.
	reposRoot := t.TempDir()
	require.NoError(t, os.MkdirAll(filepath.Join(reposRoot, "demo"), 0o755))

	projectsRoot := filepath.Join(root, "projects")
	projectStore := project.NewFileStore(projectsRoot)
	taskStore := task.NewFileStore(projectsRoot)
	chatClient = chat.NewOpenAIClient(upstream.URL, "test-key", 5*time.Second)
	knowledgeStore := knowledge.NewFileStore(filepath.Join(root, "knowledge"))
	agentRunners := map[string]agentrunner.AgentRunner{
		"local": agentrunner.NewChatClientRunner(chatClient, nil),
		// "claude-code" is registered too (even though nothing here spawns
		// the real CLI) because it's the default execution seed
		// (newRouter's "claude-code" argument) that task creation consults
		// via Server.newTaskAgentDefaults — Capabilities() is a static
		// descriptor, so registering it costs nothing and doesn't require
		// the CLI to be present or healthy.
		"claude-code": agentrunner.NewClaudeRunner(5*time.Second, 5*time.Second, reposRoot, nil),
	}

	router := NewRouter(projectStore, taskStore, knowledgeStore, agentRunners, reposRoot, nil, nil, testFrontendFS(), "test-build")
	server := httptest.NewServer(router)
	t.Cleanup(server.Close)

	return server.URL, chatClient
}

// fakeUpstream serves both the OpenAI-compatible /chat/completions endpoint
// (always as a Server-Sent-Events stream, since chat.ChatClient always
// requests stream: true) and the /models endpoint chat.ChatClient.CheckHealth
// probes.
func fakeUpstream(t *testing.T) *httptest.Server {
	t.Helper()
	return httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/models":
			w.Header().Set("Content-Type", "application/json")
			_ = json.NewEncoder(w).Encode(chat.ModelsResponse{
				Data: []chat.ModelInfo{{ID: "test-model"}, {ID: "other-model"}},
			})
		case "/chat/completions":
			w.Header().Set("Content-Type", "text/event-stream")
			flusher := w.(http.Flusher)
			for _, word := range []string{"hello ", "back"} {
				chunk, _ := json.Marshal(map[string]any{
					"choices": []map[string]any{{"index": 0, "delta": map[string]string{"content": word}}},
				})
				fmt.Fprintf(w, "data: %s\n\n", chunk)
				flusher.Flush()
			}
			fmt.Fprint(w, "data: [DONE]\n\n")
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

	resp, err := client.Get(baseURL + "/api/v1/projects/demo-project/tasks")
	require.NoError(t, err)
	defer resp.Body.Close()
	require.Equal(t, http.StatusOK, resp.StatusCode)
	var taskList task.ListResult
	require.NoError(t, json.NewDecoder(resp.Body).Decode(&taskList))
	require.Len(t, taskList.Tasks, 1)
	assert.Equal(t, "TASK-0001", taskList.Tasks[0].ID)
	assert.Empty(t, taskList.Errors)

	resp, err = client.Get(baseURL + "/api/v1/projects/demo-project/tasks/TASK-0001")
	require.NoError(t, err)
	defer resp.Body.Close()
	assert.Equal(t, http.StatusOK, resp.StatusCode)

	resp, err = client.Get(baseURL + "/api/v1/projects/demo-project/tasks/TASK-9999")
	require.NoError(t, err)
	defer resp.Body.Close()
	assert.Equal(t, http.StatusNotFound, resp.StatusCode)

	resp, err = client.Get(baseURL + "/api/v1/projects/nonexistent/tasks/TASK-0001")
	require.NoError(t, err)
	defer resp.Body.Close()
	assert.Equal(t, http.StatusNotFound, resp.StatusCode)

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
	projectRoot := filepath.Join(root, "projects", "demo-project")
	require.NoError(t, os.MkdirAll(projectRoot, 0o755))
	require.NoError(t, os.WriteFile(filepath.Join(projectRoot, "project.yaml"), []byte(integrationProjectYAML), 0o644))

	validDir := filepath.Join(projectRoot, "tasks", "TASK-0001")
	require.NoError(t, os.MkdirAll(validDir, 0o755))
	require.NoError(t, os.WriteFile(filepath.Join(validDir, "task.yaml"), []byte(integrationTaskYAML), 0o644))

	brokenDir := filepath.Join(projectRoot, "tasks", "TASK-0002")
	require.NoError(t, os.MkdirAll(brokenDir, 0o755))
	require.NoError(t, os.WriteFile(filepath.Join(brokenDir, "task.yaml"), []byte("id: [not valid yaml"), 0o644))

	projectsRoot := filepath.Join(root, "projects")
	projectStore := project.NewFileStore(projectsRoot)
	taskStore := task.NewFileStore(projectsRoot)
	knowledgeStore := knowledge.NewFileStore(filepath.Join(root, "knowledge"))

	router := NewRouter(projectStore, taskStore, knowledgeStore, nil, "", nil, nil, testFrontendFS(), "test-build")
	server := httptest.NewServer(router)
	defer server.Close()

	resp, err := http.Get(server.URL + "/api/v1/projects/demo-project/tasks")
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

func TestIntegration_CreateTaskPersistsToRealFileStore(t *testing.T) {
	upstream := fakeUpstream(t)
	defer upstream.Close()
	baseURL, _ := newIntegrationServer(t, upstream)

	body, err := json.Marshal(task.Task{ID: "new-task", Title: "New task"})
	require.NoError(t, err)

	resp, err := http.Post(baseURL+"/api/v1/projects/demo-project/tasks", "application/json", bytes.NewReader(body))
	require.NoError(t, err)
	defer resp.Body.Close()
	require.Equal(t, http.StatusCreated, resp.StatusCode)

	var created task.Task
	require.NoError(t, json.NewDecoder(resp.Body).Decode(&created))
	assert.Equal(t, "new-task", created.ID)
	assert.Equal(t, "demo-project", created.Project)

	resp, err = http.Get(baseURL + "/api/v1/projects/demo-project/tasks/new-task")
	require.NoError(t, err)
	defer resp.Body.Close()
	require.Equal(t, http.StatusOK, resp.StatusCode)
}

func TestIntegration_CreateTaskWithSameIDAcrossTwoProjectsBothSucceed(t *testing.T) {
	upstream := fakeUpstream(t)
	defer upstream.Close()

	root := t.TempDir()
	for _, id := range []string{"demo-project", "other-project"} {
		dir := filepath.Join(root, "projects", id)
		require.NoError(t, os.MkdirAll(dir, 0o755))
		require.NoError(t, os.WriteFile(filepath.Join(dir, "project.yaml"), []byte(integrationProjectYAML), 0o644))
	}

	projectsRoot := filepath.Join(root, "projects")
	projectStore := project.NewFileStore(projectsRoot)
	taskStore := task.NewFileStore(projectsRoot)
	knowledgeStore := knowledge.NewFileStore(filepath.Join(root, "knowledge"))
	router := NewRouter(projectStore, taskStore, knowledgeStore, nil, "", nil, nil, testFrontendFS(), "test-build")
	server := httptest.NewServer(router)
	defer server.Close()

	body, err := json.Marshal(task.Task{ID: "shared-id", Title: "Shared"})
	require.NoError(t, err)

	for _, projectID := range []string{"demo-project", "other-project"} {
		resp, err := http.Post(server.URL+"/api/v1/projects/"+projectID+"/tasks", "application/json", bytes.NewReader(body))
		require.NoError(t, err)
		defer resp.Body.Close()
		require.Equal(t, http.StatusCreated, resp.StatusCode, "expected task creation to succeed for project %s", projectID)
	}
}

func TestIntegration_CreateDuplicateTaskInSameProjectReturns409(t *testing.T) {
	upstream := fakeUpstream(t)
	defer upstream.Close()
	baseURL, _ := newIntegrationServer(t, upstream)

	body, err := json.Marshal(task.Task{ID: "dup-task", Title: "First"})
	require.NoError(t, err)

	resp, err := http.Post(baseURL+"/api/v1/projects/demo-project/tasks", "application/json", bytes.NewReader(body))
	require.NoError(t, err)
	resp.Body.Close()
	require.Equal(t, http.StatusCreated, resp.StatusCode)

	resp, err = http.Post(baseURL+"/api/v1/projects/demo-project/tasks", "application/json", bytes.NewReader(body))
	require.NoError(t, err)
	defer resp.Body.Close()
	assert.Equal(t, http.StatusConflict, resp.StatusCode)
}

func TestIntegration_UpdateTaskPreservesCreatedAt(t *testing.T) {
	upstream := fakeUpstream(t)
	defer upstream.Close()
	baseURL, _ := newIntegrationServer(t, upstream)

	resp, err := http.Get(baseURL + "/api/v1/projects/demo-project/tasks/TASK-0001")
	require.NoError(t, err)
	var before task.Task
	require.NoError(t, json.NewDecoder(resp.Body).Decode(&before))
	resp.Body.Close()

	body, err := json.Marshal(task.Task{ID: "TASK-0001", Title: "Updated title"})
	require.NoError(t, err)
	req, err := http.NewRequest(http.MethodPut, baseURL+"/api/v1/projects/demo-project/tasks/TASK-0001", bytes.NewReader(body))
	require.NoError(t, err)
	req.Header.Set("Content-Type", "application/json")

	resp, err = http.DefaultClient.Do(req)
	require.NoError(t, err)
	defer resp.Body.Close()
	require.Equal(t, http.StatusOK, resp.StatusCode)

	var after task.Task
	require.NoError(t, json.NewDecoder(resp.Body).Decode(&after))
	assert.Equal(t, "Updated title", after.Title)
	assert.True(t, before.CreatedAt.Equal(after.CreatedAt))
}

func TestIntegration_UpdateTaskRejectsIDMismatch(t *testing.T) {
	upstream := fakeUpstream(t)
	defer upstream.Close()
	baseURL, _ := newIntegrationServer(t, upstream)

	body, err := json.Marshal(task.Task{ID: "TASK-9999", Title: "Moved?"})
	require.NoError(t, err)
	req, err := http.NewRequest(http.MethodPut, baseURL+"/api/v1/projects/demo-project/tasks/TASK-0001", bytes.NewReader(body))
	require.NoError(t, err)
	req.Header.Set("Content-Type", "application/json")

	resp, err := http.DefaultClient.Do(req)
	require.NoError(t, err)
	defer resp.Body.Close()
	assert.Equal(t, http.StatusBadRequest, resp.StatusCode)
}

func TestIntegration_CreateAndUpdateProject(t *testing.T) {
	upstream := fakeUpstream(t)
	defer upstream.Close()
	baseURL, _ := newIntegrationServer(t, upstream)

	body, err := json.Marshal(project.CreateInput{Name: "Another Project"})
	require.NoError(t, err)
	resp, err := http.Post(baseURL+"/api/v1/projects", "application/json", bytes.NewReader(body))
	require.NoError(t, err)
	defer resp.Body.Close()
	require.Equal(t, http.StatusCreated, resp.StatusCode)

	var created project.Project
	require.NoError(t, json.NewDecoder(resp.Body).Decode(&created))
	assert.Equal(t, "another-project", created.ID)

	updateBody, err := json.Marshal(project.UpdateInput{Name: "Another Project", Description: "updated"})
	require.NoError(t, err)
	req, err := http.NewRequest(http.MethodPut, baseURL+"/api/v1/projects/another-project", bytes.NewReader(updateBody))
	require.NoError(t, err)
	req.Header.Set("Content-Type", "application/json")

	updateResp, err := http.DefaultClient.Do(req)
	require.NoError(t, err)
	defer updateResp.Body.Close()
	require.Equal(t, http.StatusOK, updateResp.StatusCode)

	var updated project.Project
	require.NoError(t, json.NewDecoder(updateResp.Body).Decode(&updated))
	assert.Equal(t, "updated", updated.Description)
}

func TestIntegration_CreateDuplicateProjectNameReturns409(t *testing.T) {
	upstream := fakeUpstream(t)
	defer upstream.Close()
	baseURL, _ := newIntegrationServer(t, upstream)

	body, err := json.Marshal(project.CreateInput{Name: "Demo Project"})
	require.NoError(t, err)

	resp, err := http.Post(baseURL+"/api/v1/projects", "application/json", bytes.NewReader(body))
	require.NoError(t, err)
	defer resp.Body.Close()
	assert.Equal(t, http.StatusConflict, resp.StatusCode, "demo-project already exists from the fixture seed")
}

func TestIntegration_ChatCompletionsRoundTripsThroughRealClient(t *testing.T) {
	upstream := fakeUpstream(t)
	defer upstream.Close()
	baseURL, _ := newIntegrationServer(t, upstream)

	body, err := json.Marshal(chatCompletionRequest{
		Content:    "hello",
		Model:      "test-model",
		SessionKey: "integration-test-session",
	})
	require.NoError(t, err)

	resp, err := http.Post(baseURL+"/api/v1/chat/completions", "application/json", bytes.NewReader(body))
	require.NoError(t, err)
	defer resp.Body.Close()
	require.Equal(t, http.StatusOK, resp.StatusCode)
	assert.Equal(t, "text/event-stream", resp.Header.Get("Content-Type"))

	var content strings.Builder
	scanner := bufio.NewScanner(resp.Body)
	for scanner.Scan() {
		data, ok := strings.CutPrefix(scanner.Text(), "data: ")
		if !ok {
			continue
		}
		var ev chatStreamEvent
		require.NoError(t, json.Unmarshal([]byte(data), &ev))
		require.Empty(t, ev.Error)
		content.WriteString(ev.Content)
	}
	require.NoError(t, scanner.Err())
	assert.Equal(t, "hello back", content.String())
}

func TestIntegration_ExecutorCapabilitiesRoundTripsThroughRealClient(t *testing.T) {
	upstream := fakeUpstream(t)
	defer upstream.Close()
	baseURL, _ := newIntegrationServer(t, upstream)

	resp, err := http.Get(baseURL + "/api/v1/agent-executors")
	require.NoError(t, err)
	defer resp.Body.Close()
	require.Equal(t, http.StatusOK, resp.StatusCode)

	var got struct {
		Executors []agentrunner.ExecutorCapabilities `json:"executors"`
	}
	require.NoError(t, json.NewDecoder(resp.Body).Decode(&got))
	require.Len(t, got.Executors, 2)
	var local *agentrunner.ExecutorCapabilities
	for i := range got.Executors {
		if got.Executors[i].Name == "local" {
			local = &got.Executors[i]
		}
	}
	require.NotNil(t, local, "expected a \"local\" executor entry")
	assert.Equal(t, []string{"test-model", "other-model"}, local.Models)
	assert.Equal(t, agentrunner.EffortMedium, local.DefaultEffort)
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

	var got struct {
		Status     string `json:"status"`
		Error      string `json:"error"`
		Subsystems map[string]struct {
			Status string `json:"status"`
			Error  string `json:"error,omitempty"`
		} `json:"subsystems"`
	}
	require.NoError(t, json.NewDecoder(resp.Body).Decode(&got))
	assert.Equal(t, "error", got.Status)
	assert.NotEmpty(t, got.Error)
	require.Contains(t, got.Subsystems, "agent:local")
	assert.Equal(t, "error", got.Subsystems["agent:local"].Status)
	assert.NotEmpty(t, got.Subsystems["agent:local"].Error)
}

// fakeUpstreamProposingToolCall behaves like fakeUpstream but, for
// /chat/completions requests whose body includes a non-empty "tools" array,
// streams a single fragmented tool call (matching the real fragmentation
// shape observed against a live LM Studio server: name/id on the first
// fragment, arguments as a separate fragment, then finish_reason
// "tool_calls") instead of plain content.
func fakeUpstreamProposingToolCall(t *testing.T, toolName, argumentsJSON string) *httptest.Server {
	t.Helper()
	return httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/models":
			w.Header().Set("Content-Type", "application/json")
			_ = json.NewEncoder(w).Encode(chat.ModelsResponse{Data: []chat.ModelInfo{{ID: "test-model"}}})
		case "/chat/completions":
			var req chat.CompletionRequest
			require.NoError(t, json.NewDecoder(r.Body).Decode(&req))

			w.Header().Set("Content-Type", "text/event-stream")
			flusher := w.(http.Flusher)

			if len(req.Tools) == 0 {
				fmt.Fprint(w, "data: [DONE]\n\n")
				return
			}

			writeFrag := func(id, name, args string) {
				chunk, _ := json.Marshal(map[string]any{
					"choices": []map[string]any{{
						"index": 0,
						"delta": map[string]any{
							"tool_calls": []map[string]any{{
								"index":    0,
								"id":       id,
								"function": map[string]string{"name": name, "arguments": args},
							}},
						},
					}},
				})
				fmt.Fprintf(w, "data: %s\n\n", chunk)
				flusher.Flush()
			}
			writeFrag("call-1", toolName, "")
			writeFrag("", "", argumentsJSON)
			fmt.Fprint(w, `data: {"choices":[{"index":0,"delta":{},"finish_reason":"tool_calls"}]}`+"\n\n")
			fmt.Fprint(w, "data: [DONE]\n\n")
		default:
			http.NotFound(w, r)
		}
	}))
}

// TestIntegration_ReviewConversation_CarriesDiffAndProposesReview drives the
// full Review-stage conversation over the real router/FileStore/chat.ChatClient
// chain: a task sitting at stage review with a real execution worktree. It
// asserts the end-to-end wiring PR 2 adds — resolveStageRun resolves the
// worktree, buildReviewContext puts the changed-files summary + verification
// steps into the system prompt (the diff text itself is deliberately left for
// the agent's own bash tool to fetch — see buildReviewContext's doc comment),
// the confined bash tool is offered alongside propose_review, and a
// propose_review tool call streams back and persists. This is the closest to
// M8's live-verify bar reachable without a real model: the model is faked, but
// every other collaborator (git, the stores, the tool loop) is real.
func TestIntegration_ReviewConversation_CarriesDiffAndProposesReview(t *testing.T) {
	root := t.TempDir()
	projectRoot := filepath.Join(root, "projects", "demo-project")
	require.NoError(t, os.MkdirAll(projectRoot, 0o755))
	require.NoError(t, os.WriteFile(filepath.Join(projectRoot, "project.yaml"), []byte(integrationProjectYAML), 0o644))

	// A real git repo at reposRoot/demo (integrationProjectYAML's repo is
	// github.com/org/demo) plus an execution worktree with one extra commit.
	reposRoot := t.TempDir()
	repoDir := filepath.Join(reposRoot, "demo")
	require.NoError(t, os.MkdirAll(repoDir, 0o755))
	gitRun(t, repoDir, "init", "-q", "-b", "main")
	gitRun(t, repoDir, "config", "user.email", "t@example.com")
	gitRun(t, repoDir, "config", "user.name", "T")
	require.NoError(t, os.WriteFile(filepath.Join(repoDir, "README.md"), []byte("hi\n"), 0o644))
	gitRun(t, repoDir, "add", ".")
	gitRun(t, repoDir, "commit", "-q", "-m", "init")
	ws, err := agentrunner.ResolveExecutionWorkspace(context.Background(), reposRoot, []string{"github.com/org/demo"}, "TASK-0001", "exec-001", "", "main")
	require.NoError(t, err)
	require.NoError(t, os.WriteFile(filepath.Join(ws.Path, "feature.go"), []byte("package main\n"), 0o644))
	gitRun(t, ws.Path, "add", ".")
	gitRun(t, ws.Path, "commit", "-q", "-m", "add feature")

	// Seed the task straight to stage review via the public store API, in the
	// same tasks root the router will read from. Output is populated from the
	// real worktree above (as a completed execution would via
	// agentrunner.CollectExecutionOutput, internal/api/execution.go) so
	// buildReviewContext has real commits/changed files to report.
	commits, artifacts, err := agentrunner.CollectExecutionOutput(context.Background(), ws, "")
	require.NoError(t, err)
	seedReviewableTaskWithOutput(t, task.NewFileStore(filepath.Join(root, "projects")), "TASK-0001", task.ExecutionOutput{Commits: commits, Artifacts: artifacts})

	// A capturing fake upstream: record the request the model saw, then answer
	// with a propose_review tool call (the loop's stop condition).
	var mu sync.Mutex
	var gotReq chat.CompletionRequest
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/models":
			w.Header().Set("Content-Type", "application/json")
			_ = json.NewEncoder(w).Encode(chat.ModelsResponse{Data: []chat.ModelInfo{{ID: "test-model"}}})
		case "/chat/completions":
			var req chat.CompletionRequest
			require.NoError(t, json.NewDecoder(r.Body).Decode(&req))
			mu.Lock()
			gotReq = req
			mu.Unlock()

			w.Header().Set("Content-Type", "text/event-stream")
			flusher := w.(http.Flusher)
			writeFrag := func(id, name, args string) {
				chunk, _ := json.Marshal(map[string]any{
					"choices": []map[string]any{{"index": 0, "delta": map[string]any{
						"tool_calls": []map[string]any{{"index": 0, "id": id, "function": map[string]string{"name": name, "arguments": args}}},
					}}},
				})
				fmt.Fprintf(w, "data: %s\n\n", chunk)
				flusher.Flush()
			}
			writeFrag("call-1", proposeReviewToolName, "")
			writeFrag("", "", `{"decision":"approved","notes":"tests pass"}`)
			fmt.Fprint(w, `data: {"choices":[{"index":0,"delta":{},"finish_reason":"tool_calls"}]}`+"\n\n")
			fmt.Fprint(w, "data: [DONE]\n\n")
		default:
			http.NotFound(w, r)
		}
	}))
	defer upstream.Close()

	projectsRoot := filepath.Join(root, "projects")
	projectStore := project.NewFileStore(projectsRoot)
	taskStore := task.NewFileStore(projectsRoot)
	chatClient := chat.NewOpenAIClient(upstream.URL, "test-key", 5*time.Second)
	knowledgeStore := knowledge.NewFileStore(filepath.Join(root, "knowledge"))
	agentRunners := map[string]agentrunner.AgentRunner{"local": agentrunner.NewChatClientRunner(chatClient, nil)}

	router := NewRouter(projectStore, taskStore, knowledgeStore, agentRunners, reposRoot, nil, nil, testFrontendFS(), "test-build")
	server := httptest.NewServer(router)
	defer server.Close()

	body, err := json.Marshal(stageMessageRequest{Content: "start the review", Model: "test-model", Executor: "local", Effort: "medium"})
	require.NoError(t, err)
	resp, err := http.Post(server.URL+"/api/v1/projects/demo-project/tasks/TASK-0001/stages/review/messages", "application/json", bytes.NewReader(body))
	require.NoError(t, err)
	defer resp.Body.Close()
	require.Equal(t, http.StatusOK, resp.StatusCode)

	respBody, err := io.ReadAll(resp.Body)
	require.NoError(t, err)

	var sawReview bool
	for _, line := range strings.Split(string(respBody), "\n") {
		data, ok := strings.CutPrefix(strings.TrimSpace(line), "data: ")
		if !ok || data == "" {
			continue
		}
		var ev chatStreamEvent
		require.NoError(t, json.Unmarshal([]byte(data), &ev))
		if ev.ToolCall != nil {
			sawReview = true
			assert.Equal(t, proposeReviewToolName, ev.ToolCall.Name)
			assert.Contains(t, ev.ToolCall.Arguments, "approved")
		}
	}
	require.True(t, sawReview, "expected a propose_review tool_call SSE event")

	// The model actually saw the execution's changed files and verification
	// steps in its system prompt, and was offered the confined bash tool plus
	// propose_review. The diff text itself is deliberately not inlined (see
	// buildReviewContext's doc comment) — the model is told to fetch it itself.
	mu.Lock()
	captured := gotReq
	mu.Unlock()
	require.NotEmpty(t, captured.Messages)
	system := captured.Messages[0].Content
	assert.Contains(t, system, "feature.go", "the changed-files list must reach the model")
	assert.Contains(t, system, "git diff main...HEAD", "the model must be told how to fetch the real diff itself")
	assert.NotContains(t, system, "```diff", "the diff text itself must not be inlined")
	assert.Contains(t, system, "run go test", "verification steps must reach the model")
	var toolNames []string
	for _, tl := range captured.Tools {
		toolNames = append(toolNames, tl.Function.Name)
	}
	assert.Contains(t, toolNames, "bash", "review must offer the confined bash tool")
	assert.Contains(t, toolNames, proposeReviewToolName)

	// The propose_review tool call must have persisted onto conversation-review.yaml.
	convResp, err := http.Get(server.URL + "/api/v1/projects/demo-project/tasks/TASK-0001/stages/review/conversation")
	require.NoError(t, err)
	defer convResp.Body.Close()
	var conv task.Conversation
	require.NoError(t, json.NewDecoder(convResp.Body).Decode(&conv))
	require.Len(t, conv.Messages, 2)
	require.NotNil(t, conv.Messages[1].ToolCall)
	assert.Equal(t, proposeReviewToolName, conv.Messages[1].ToolCall.Name)
}

func TestIntegration_StageMessageStreamsProposedDraftAsToolCallEvent(t *testing.T) {
	upstream := fakeUpstreamProposingToolCall(t, proposeContextToolName, `{"objective":"ship login","context":{"summary":"s"}}`)
	defer upstream.Close()
	baseURL, _ := newIntegrationServer(t, upstream)

	body, err := json.Marshal(stageMessageRequest{Content: "let's get started"})
	require.NoError(t, err)

	resp, err := http.Post(baseURL+"/api/v1/projects/demo-project/tasks/TASK-0001/stages/requirements/messages", "application/json", bytes.NewReader(body))
	require.NoError(t, err)
	defer resp.Body.Close()
	require.Equal(t, http.StatusOK, resp.StatusCode)

	respBody, err := io.ReadAll(resp.Body)
	require.NoError(t, err)

	var sawToolCall bool
	for _, line := range strings.Split(string(respBody), "\n") {
		data, ok := strings.CutPrefix(strings.TrimSpace(line), "data: ")
		if !ok || data == "" {
			continue
		}
		var ev chatStreamEvent
		require.NoError(t, json.Unmarshal([]byte(data), &ev))
		if ev.ToolCall != nil {
			sawToolCall = true
			assert.Equal(t, proposeContextToolName, ev.ToolCall.Name)
			assert.Equal(t, `{"objective":"ship login","context":{"summary":"s"}}`, ev.ToolCall.Arguments)
		}
	}
	require.True(t, sawToolCall, "expected a tool_call SSE event in the response body")

	// The conversation must have been persisted with the tool call attached.
	convResp, err := http.Get(baseURL + "/api/v1/projects/demo-project/tasks/TASK-0001/stages/requirements/conversation")
	require.NoError(t, err)
	defer convResp.Body.Close()
	var conv task.Conversation
	require.NoError(t, json.NewDecoder(convResp.Body).Decode(&conv))
	require.Len(t, conv.Messages, 2)
	assert.Equal(t, "let's get started", conv.Messages[0].Content)
	require.NotNil(t, conv.Messages[1].ToolCall)
	assert.Equal(t, proposeContextToolName, conv.Messages[1].ToolCall.Name)
}

// TestIntegration_StartStageConversation_SeedsFirstQuestionAndRejectsRestart
// drives the real router/FileStore/chat.ChatClient chain for a brand-new
// task's empty Conversation: /start must run one turn on the agent's own
// initiative (no human message exists yet to trigger a reply otherwise),
// persist only the resulting assistant turn, and refuse a second /start
// once the conversation is no longer empty.
func TestIntegration_StartStageConversation_SeedsFirstQuestionAndRejectsRestart(t *testing.T) {
	upstream := fakeUpstream(t)
	defer upstream.Close()
	baseURL, _ := newIntegrationServer(t, upstream)

	body, err := json.Marshal(stageStartRequest{})
	require.NoError(t, err)

	resp, err := http.Post(baseURL+"/api/v1/projects/demo-project/tasks/TASK-0001/stages/requirements/start", "application/json", bytes.NewReader(body))
	require.NoError(t, err)
	defer resp.Body.Close()
	require.Equal(t, http.StatusOK, resp.StatusCode)

	respBody, err := io.ReadAll(resp.Body)
	require.NoError(t, err)
	assert.Contains(t, string(respBody), "hello ", "fakeUpstream's content should have streamed through as the opening question")

	convResp, err := http.Get(baseURL + "/api/v1/projects/demo-project/tasks/TASK-0001/stages/requirements/conversation")
	require.NoError(t, err)
	defer convResp.Body.Close()
	var conv task.Conversation
	require.NoError(t, json.NewDecoder(convResp.Body).Decode(&conv))
	require.Len(t, conv.Messages, 1, "only the assistant's opening question is persisted, no synthetic user turn")
	assert.Equal(t, "assistant", conv.Messages[0].Role)
	assert.Equal(t, "hello back", conv.Messages[0].Content)

	// A second /start on the now-non-empty conversation must be rejected.
	resp2, err := http.Post(baseURL+"/api/v1/projects/demo-project/tasks/TASK-0001/stages/requirements/start", "application/json", bytes.NewReader(body))
	require.NoError(t, err)
	defer resp2.Body.Close()
	assert.Equal(t, http.StatusConflict, resp2.StatusCode)
}

// TestIntegration_FullLifecycle_FinalizeAndReviseBothWays drives a task
// through the full GrillMe -> Planning Mode -> Revise loop against real
// FileStores, matching the milestone's manual verification walkthrough:
// Finalize requirements advances to planning and writes context.yaml,
// Finalize plan advances to implementation and writes plan.yaml, then
// Revise Plan and Revise Requirements each move stage back one step,
// resuming (not resetting) each stage's persisted Conversation.
func TestIntegration_FullLifecycle_FinalizeAndReviseBothWays(t *testing.T) {
	upstream := fakeUpstream(t)
	defer upstream.Close()
	baseURL, _ := newIntegrationServer(t, upstream)
	client := &http.Client{Timeout: 5 * time.Second}

	getStage := func() string {
		resp, err := client.Get(baseURL + "/api/v1/projects/demo-project/tasks/TASK-0001")
		require.NoError(t, err)
		defer resp.Body.Close()
		var got task.Task
		require.NoError(t, json.NewDecoder(resp.Body).Decode(&got))
		return got.Stage
	}
	require.Equal(t, task.StageRequirements, getStage())

	draft := task.RequirementsDraft{
		Objective: "ship login",
		Context: task.Context{
			Summary: "adds a login page",
			// Structured verification (docs/adr/0008): the exact wire shape
			// the frontend now POSTs. This is the finalize break Milestone 6
			// PR 2 has to fix — a string[] body would no longer decode into
			// []VerificationStep — so the round-trip is asserted end-to-end.
			Verification: []task.VerificationStep{
				{Description: "hit POST /login and get a 200", Kind: task.VerificationKindAgentExecutable},
				{Description: "the empty-state copy reads naturally", Kind: task.VerificationKindHumanJudgment},
			},
		},
	}
	draftBody, err := json.Marshal(draft)
	require.NoError(t, err)
	resp, err := client.Post(baseURL+"/api/v1/projects/demo-project/tasks/TASK-0001/requirements/finalize", "application/json", bytes.NewReader(draftBody))
	require.NoError(t, err)
	resp.Body.Close()
	require.Equal(t, http.StatusOK, resp.StatusCode)
	assert.Equal(t, task.StagePlanning, getStage())

	ctxResp, err := client.Get(baseURL + "/api/v1/projects/demo-project/tasks/TASK-0001/context")
	require.NoError(t, err)
	defer ctxResp.Body.Close()
	require.Equal(t, http.StatusOK, ctxResp.StatusCode)
	var gotCtx task.Context
	require.NoError(t, json.NewDecoder(ctxResp.Body).Decode(&gotCtx))
	assert.Equal(t, "adds a login page", gotCtx.Summary)
	assert.Equal(t, draft.Context.Verification, gotCtx.Verification)

	plan := task.Plan{Approach: "incremental", EstimatedComplexity: "low"}
	planBody, err := json.Marshal(plan)
	require.NoError(t, err)
	resp, err = client.Post(baseURL+"/api/v1/projects/demo-project/tasks/TASK-0001/plan/finalize", "application/json", bytes.NewReader(planBody))
	require.NoError(t, err)
	resp.Body.Close()
	require.Equal(t, http.StatusOK, resp.StatusCode)
	assert.Equal(t, task.StageImplementation, getStage())

	planResp, err := client.Get(baseURL + "/api/v1/projects/demo-project/tasks/TASK-0001/plan")
	require.NoError(t, err)
	defer planResp.Body.Close()
	require.Equal(t, http.StatusOK, planResp.StatusCode)
	var gotPlan task.Plan
	require.NoError(t, json.NewDecoder(planResp.Body).Decode(&gotPlan))
	assert.Equal(t, "incremental", gotPlan.Approach)

	// Revise Plan: implementation -> planning.
	resp, err = client.Post(baseURL+"/api/v1/projects/demo-project/tasks/TASK-0001/plan/revise", "application/json", nil)
	require.NoError(t, err)
	resp.Body.Close()
	require.Equal(t, http.StatusOK, resp.StatusCode)
	assert.Equal(t, task.StagePlanning, getStage())

	// Revise Requirements: planning -> requirements.
	resp, err = client.Post(baseURL+"/api/v1/projects/demo-project/tasks/TASK-0001/requirements/revise", "application/json", nil)
	require.NoError(t, err)
	resp.Body.Close()
	require.Equal(t, http.StatusOK, resp.StatusCode)
	assert.Equal(t, task.StageRequirements, getStage())

	// Finalize is now invalid from planning (we're back in requirements) and
	// Revise Plan is invalid from requirements -- both must 409.
	resp, err = client.Post(baseURL+"/api/v1/projects/demo-project/tasks/TASK-0001/plan/finalize", "application/json", bytes.NewReader(planBody))
	require.NoError(t, err)
	resp.Body.Close()
	assert.Equal(t, http.StatusConflict, resp.StatusCode)
}

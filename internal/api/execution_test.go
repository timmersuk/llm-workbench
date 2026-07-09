package api

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/mock"
	"github.com/stretchr/testify/require"

	"github.com/timmersuk/llm-workbench/internal/agentrunner"
	"github.com/timmersuk/llm-workbench/internal/project"
	"github.com/timmersuk/llm-workbench/internal/task"
)

// newExecutionTestRepo creates a real git repository under
// reposRoot/repoName with one commit — ResolveExecutionWorkspace shells
// out to real `git` commands (git worktree add, rev-parse), so these
// handler tests need a real repo fixture, not a bare directory like the
// stage-conversation tests use.
func newExecutionTestRepo(t *testing.T, repoName string) (reposRoot string, repositories []string) {
	t.Helper()
	reposRoot = t.TempDir()
	dir := filepath.Join(reposRoot, repoName)
	require.NoError(t, os.MkdirAll(dir, 0o755))

	run := func(args ...string) {
		cmd := exec.Command("git", args...)
		cmd.Dir = dir
		out, err := cmd.CombinedOutput()
		require.NoErrorf(t, err, "git %v: %s", args, out)
	}
	run("init", "-q")
	run("config", "user.email", "test@example.com")
	run("config", "user.name", "Test")
	require.NoError(t, os.WriteFile(filepath.Join(dir, "README.md"), []byte("hello\n"), 0o644))
	run("add", ".")
	run("commit", "-q", "-m", "init")

	return reposRoot, []string{"github.com/x/" + repoName}
}

func newExecutionProjectStore(repositories []string) ProjectStore {
	projects := new(mockProjectStore)
	projects.On("Get", "demo-project").Return(project.Project{ID: "demo-project", Repositories: repositories}, nil)
	projects.On("TasksRoot", "demo-project").Return("/data/projects/demo-project/tasks", nil)
	return projects
}

func newExecutionRequest(t *testing.T, body any) *http.Request {
	t.Helper()
	req := newProjectRequest(t, http.MethodPost, "/api/v1/projects/demo-project/tasks/TASK-0001/execute", body)
	req.SetPathValue("projectId", "demo-project")
	req.SetPathValue("taskId", "TASK-0001")
	return req
}

// decodeSSEEvents parses a recorded SSE response body into its
// executeStreamEvent sequence.
func decodeSSEEvents(t *testing.T, body string) []executeStreamEvent {
	t.Helper()
	var events []executeStreamEvent
	for _, line := range splitLines(body) {
		if len(line) < 6 || line[:6] != "data: " {
			continue
		}
		var ev executeStreamEvent
		require.NoError(t, json.Unmarshal([]byte(line[6:]), &ev))
		events = append(events, ev)
	}
	return events
}

func splitLines(s string) []string {
	var lines []string
	start := 0
	for i := 0; i < len(s); i++ {
		if s[i] == '\n' {
			lines = append(lines, s[start:i])
			start = i + 1
		}
	}
	if start < len(s) {
		lines = append(lines, s[start:])
	}
	return lines
}

func TestHandleStartExecution_UnknownExecutor(t *testing.T) {
	req := newExecutionRequest(t, executionStartRequest{Executor: "does-not-exist"})
	w := httptest.NewRecorder()
	handleStartExecution(new(mockProjectStore), fixedTaskStoreFactory(new(mockTaskStore)), nil, "")(w, req)

	assert.Equal(t, http.StatusBadRequest, w.Code)
}

func TestHandleStartExecution_WrongStageRejected(t *testing.T) {
	tasks := new(mockTaskStore)
	tasks.On("Get", "TASK-0001").Return(task.Task{ID: "TASK-0001", Stage: task.StagePlanning}, nil)

	runner := new(mockAgentRunner)
	reposRoot, repositories := newExecutionTestRepo(t, "demo-repo")

	req := newExecutionRequest(t, executionStartRequest{})
	w := httptest.NewRecorder()
	handleStartExecution(newExecutionProjectStore(repositories), fixedTaskStoreFactory(tasks),
		map[string]agentrunner.AgentRunner{"claude-code": runner}, reposRoot)(w, req)

	assert.Equal(t, http.StatusConflict, w.Code)
	runner.AssertNotCalled(t, "Execute", mock.Anything, mock.Anything, mock.Anything)
}

// TestHandleStartExecution_SuccessStreamsAndRecords locks in the full happy
// path: a successful Execute call streams every ExecuteEvent as SSE,
// resolves a real isolated git worktree, and records a task.Execution with
// status success plus the worktree's actual branch/commits — verifying
// RecordExecution actually advances the task via the real store would
// belong to internal/task's own tests (execution_test.go), so here it's
// enough to assert the record handed to RecordExecution is correct.
func TestHandleStartExecution_SuccessStreamsAndRecords(t *testing.T) {
	tasks := new(mockTaskStore)
	tasks.On("Get", "TASK-0001").Return(task.Task{ID: "TASK-0001", Stage: task.StageImplementation, Objective: "ship it"}, nil)
	tasks.On("GetPlan", "TASK-0001").Return(task.Plan{Approach: "do it"}, nil)
	tasks.On("NextExecutionID", "TASK-0001").Return("exec-001", nil)

	var recorded task.Execution
	tasks.On("RecordExecution", "TASK-0001", mock.MatchedBy(func(e task.Execution) bool {
		recorded = e
		return true
	})).Return(task.Execution{ExecutionID: "exec-001", Status: task.ExecutionStatusSuccess}, nil)

	events := []agentrunner.ExecuteEvent{
		{Kind: "text", Text: "starting"},
		{Kind: "tool_call", ToolName: "Write", ToolInput: `{"path":"a.go"}`},
		{Kind: "tool_result", ToolResult: "ok"},
	}
	runner := new(mockAgentRunner)
	var gotIn agentrunner.ExecuteInput
	runner.On("Execute", mock.Anything, mock.MatchedBy(func(in agentrunner.ExecuteInput) bool {
		gotIn = in
		return true
	}), mock.Anything).Return(events, agentrunner.ExecuteOutput{Content: "done", DurationSeconds: 1.5}, nil)

	reposRoot, repositories := newExecutionTestRepo(t, "demo-repo")

	req := newExecutionRequest(t, executionStartRequest{})
	w := httptest.NewRecorder()
	handleStartExecution(newExecutionProjectStore(repositories), fixedTaskStoreFactory(tasks),
		map[string]agentrunner.AgentRunner{"claude-code": runner}, reposRoot)(w, req)

	require.Equal(t, http.StatusOK, w.Code)

	sseEvents := decodeSSEEvents(t, w.Body.String())
	require.Len(t, sseEvents, 4)
	assert.Equal(t, "text", sseEvents[0].Type)
	assert.Equal(t, "tool_call", sseEvents[1].Type)
	assert.Equal(t, "Write", sseEvents[1].ToolName)
	assert.Equal(t, "tool_result", sseEvents[2].Type)
	assert.Equal(t, "done", sseEvents[3].Type)
	require.NotNil(t, sseEvents[3].Execution)
	assert.Equal(t, "exec-001", sseEvents[3].Execution.ExecutionID)

	assert.Equal(t, "TASK-0001:execute", gotIn.SessionKey)
	assert.NotEmpty(t, gotIn.Workspace)
	assert.Contains(t, gotIn.SystemPrompt, "ship it")
	assert.Contains(t, gotIn.SystemPrompt, "do it")

	assert.Equal(t, task.ExecutionStatusSuccess, recorded.Status)
	assert.Equal(t, "claude-code", recorded.Executor.Type)
	assert.Equal(t, "task-exec/TASK-0001/exec-001", recorded.Output.GitBranch)
	assert.Equal(t, 1.5, recorded.Metrics.DurationSeconds)
}

// TestHandleStartExecution_ExecuteErrorRecordsFailure locks in the
// deterministic failure classification: a non-context error from Execute
// becomes failure.type "execution", still recorded (never silently
// dropped), and still surfaced as a "done" event so the frontend learns
// the outcome without a second request.
func TestHandleStartExecution_ExecuteErrorRecordsFailure(t *testing.T) {
	tasks := new(mockTaskStore)
	tasks.On("Get", "TASK-0001").Return(task.Task{ID: "TASK-0001", Stage: task.StageImplementation}, nil)
	tasks.On("GetPlan", "TASK-0001").Return(task.Plan{}, nil)
	tasks.On("NextExecutionID", "TASK-0001").Return("exec-001", nil)

	var recorded task.Execution
	tasks.On("RecordExecution", "TASK-0001", mock.MatchedBy(func(e task.Execution) bool {
		recorded = e
		return true
	})).Return(task.Execution{ExecutionID: "exec-001", Status: task.ExecutionStatusFailure}, nil)

	wantErr := errors.New("claude code execution failed: boom")
	runner := new(mockAgentRunner)
	runner.On("Execute", mock.Anything, mock.Anything, mock.Anything).
		Return(nil, agentrunner.ExecuteOutput{}, wantErr)

	reposRoot, repositories := newExecutionTestRepo(t, "demo-repo")

	req := newExecutionRequest(t, executionStartRequest{})
	w := httptest.NewRecorder()
	handleStartExecution(newExecutionProjectStore(repositories), fixedTaskStoreFactory(tasks),
		map[string]agentrunner.AgentRunner{"claude-code": runner}, reposRoot)(w, req)

	require.Equal(t, http.StatusOK, w.Code)
	assert.Equal(t, task.ExecutionStatusFailure, recorded.Status)
	require.NotNil(t, recorded.Failure)
	assert.Equal(t, task.FailureTypeExecution, recorded.Failure.Type)
	assert.Contains(t, recorded.Failure.Message, "boom")
}

func TestClassifyExecutionOutcome_ContextCanceledIsResourceFailure(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	cancel()

	var exec task.Execution
	classifyExecutionOutcome(&exec, errors.New("query: context canceled"), ctx)

	assert.Equal(t, task.ExecutionStatusFailure, exec.Status)
	require.NotNil(t, exec.Failure)
	assert.Equal(t, task.FailureTypeResource, exec.Failure.Type)
}

func TestClassifyExecutionOutcome_NoErrorIsSuccess(t *testing.T) {
	var exec task.Execution
	classifyExecutionOutcome(&exec, nil, context.Background())

	assert.Equal(t, task.ExecutionStatusSuccess, exec.Status)
	assert.Nil(t, exec.Failure)
}

func TestHandleListExecutions_OK(t *testing.T) {
	tasks := new(mockTaskStore)
	tasks.On("ListExecutions", "TASK-0001").Return([]task.Execution{
		{ExecutionID: "exec-001", Status: task.ExecutionStatusSuccess},
	}, nil)

	req := httptest.NewRequest(http.MethodGet, "/api/v1/projects/demo-project/tasks/TASK-0001/executions", nil)
	req.SetPathValue("projectId", "demo-project")
	req.SetPathValue("taskId", "TASK-0001")
	w := httptest.NewRecorder()
	handleListExecutions(newExecutionProjectStore(nil), fixedTaskStoreFactory(tasks))(w, req)

	require.Equal(t, http.StatusOK, w.Code)
	var got map[string][]task.Execution
	require.NoError(t, json.Unmarshal(w.Body.Bytes(), &got))
	require.Len(t, got["executions"], 1)
	assert.Equal(t, "exec-001", got["executions"][0].ExecutionID)
}

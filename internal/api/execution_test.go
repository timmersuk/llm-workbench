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
	"strings"
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
	run("init", "-q", "-b", "main")
	run("config", "user.email", "test@example.com")
	run("config", "user.name", "Test")
	require.NoError(t, os.WriteFile(filepath.Join(dir, "README.md"), []byte("hello\n"), 0o644))
	run("add", ".")
	run("commit", "-q", "-m", "init")

	return reposRoot, []string{"github.com/x/" + repoName}
}

// newExecutionProjectStore's fixture Project carries DefaultBranch: "main"
// pre-set (matching newExecutionTestRepo's pinned init branch), so
// ensureDefaultBranch short-circuits without ever calling the resolver or
// Update — these tests aren't exercising the default-branch-determination
// path, only relying on it not blocking their unrelated assertions.
func newExecutionProjectStore(repositories []string) ProjectStore {
	projects := new(mockProjectStore)
	projects.On("Get", "demo-project").Return(project.Project{ID: "demo-project", Repositories: repositories, DefaultBranch: "main"}, nil)
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

func TestExecuteEventToLogEvent_ExcludesReasoning(t *testing.T) {
	_, ok := executeEventToLogEvent(agentrunner.ExecuteEvent{Kind: "reasoning", Text: "thinking..."})
	assert.False(t, ok, "reasoning events must never be persisted to the execution log")
}

func TestExecuteEventToLogEvent_MapsOtherKindsInFull(t *testing.T) {
	cases := []agentrunner.ExecuteEvent{
		{Kind: "text", Text: "hello"},
		{Kind: "tool_call", ToolName: "Bash", ToolInput: "go test ./..."},
		{Kind: "tool_result", ToolResult: "ok", IsError: false},
		{Kind: "tool_result", ToolResult: "boom", IsError: true},
	}
	for _, ev := range cases {
		got, ok := executeEventToLogEvent(ev)
		require.True(t, ok)
		assert.Equal(t, ev.Kind, got.Kind)
		assert.Equal(t, ev.Text, got.Text)
		assert.Equal(t, ev.ToolName, got.ToolName)
		assert.Equal(t, ev.ToolInput, got.ToolInput)
		assert.Equal(t, ev.ToolResult, got.ToolResult)
		assert.Equal(t, ev.IsError, got.IsError)
	}
}

func TestHandleStartExecution_UnknownExecutor(t *testing.T) {
	req := newExecutionRequest(t, executionStartRequest{Executor: "does-not-exist"})
	w := httptest.NewRecorder()
	(&Server{Projects: new(mockProjectStore), Tasks: new(mockTaskStore)}).handleStartExecution()(w, req)

	assert.Equal(t, http.StatusBadRequest, w.Code)
}

func TestHandleStartExecution_WrongStageRejected(t *testing.T) {
	tasks := new(mockTaskStore)
	tasks.On("Get", "demo-project", "TASK-0001").Return(task.Task{ID: "TASK-0001", Stage: task.StagePlanning}, nil)

	runner := new(mockAgentRunner)
	reposRoot, repositories := newExecutionTestRepo(t, "demo-repo")

	req := newExecutionRequest(t, executionStartRequest{})
	w := httptest.NewRecorder()
	(&Server{Projects: newExecutionProjectStore(repositories), Tasks: tasks,
		AgentRunners: map[string]agentrunner.AgentRunner{"claude-code": runner}, ReposRoot: reposRoot}).handleStartExecution()(w, req)

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
	tasks.On("Get", "demo-project", "TASK-0001").Return(task.Task{ID: "TASK-0001", Stage: task.StageImplementation, Objective: "ship it"}, nil)
	tasks.On("GetPlan", "demo-project", "TASK-0001").Return(task.Plan{Approach: "do it"}, nil)
	tasks.On("ListReviews", "demo-project", "TASK-0001").Return(nil, nil)
	tasks.On("ListExecutions", "demo-project", "TASK-0001").Return(nil, nil)
	tasks.On("NextExecutionID", "demo-project", "TASK-0001").Return("exec-001", nil)
	tasks.On("CreateExecutionLog", "demo-project", "TASK-0001", "exec-001").Return(nil)
	tasks.On("AppendExecutionLogEvent", "demo-project", "TASK-0001", "exec-001", mock.Anything).Return(nil)

	var recorded task.Execution
	tasks.On("RecordExecution", "demo-project", "TASK-0001", mock.MatchedBy(func(e task.Execution) bool {
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
	(&Server{Projects: newExecutionProjectStore(repositories), Tasks: tasks,
		AgentRunners: map[string]agentrunner.AgentRunner{"claude-code": runner}, ReposRoot: reposRoot}).handleStartExecution()(w, req)

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

// TestHandleStartExecution_LogsEveryEventExceptReasoning proves the
// execution-log wiring end to end: every ExecuteEvent still streams over
// SSE (reasoning included — that stream is unrelated), but only the
// non-reasoning events are appended to the persisted execution log.
func TestHandleStartExecution_LogsEveryEventExceptReasoning(t *testing.T) {
	tasks := new(mockTaskStore)
	tasks.On("Get", "demo-project", "TASK-0001").Return(task.Task{ID: "TASK-0001", Stage: task.StageImplementation, Objective: "ship it"}, nil)
	tasks.On("GetPlan", "demo-project", "TASK-0001").Return(task.Plan{Approach: "do it"}, nil)
	tasks.On("ListReviews", "demo-project", "TASK-0001").Return(nil, nil)
	tasks.On("ListExecutions", "demo-project", "TASK-0001").Return(nil, nil)
	tasks.On("NextExecutionID", "demo-project", "TASK-0001").Return("exec-001", nil)
	tasks.On("CreateExecutionLog", "demo-project", "TASK-0001", "exec-001").Return(nil)
	var loggedKinds []string
	tasks.On("AppendExecutionLogEvent", "demo-project", "TASK-0001", "exec-001", mock.MatchedBy(func(ev task.ExecutionLogEvent) bool {
		loggedKinds = append(loggedKinds, ev.Kind)
		return true
	})).Return(nil)
	tasks.On("RecordExecution", "demo-project", "TASK-0001", mock.Anything).
		Return(task.Execution{ExecutionID: "exec-001", Status: task.ExecutionStatusSuccess}, nil)

	events := []agentrunner.ExecuteEvent{
		{Kind: "reasoning", Text: "thinking about it"},
		{Kind: "text", Text: "starting"},
		{Kind: "reasoning", Text: "more thinking"},
		{Kind: "tool_call", ToolName: "Write"},
		{Kind: "tool_result", ToolResult: "ok"},
	}
	runner := new(mockAgentRunner)
	runner.On("Execute", mock.Anything, mock.Anything, mock.Anything).
		Return(events, agentrunner.ExecuteOutput{Content: "done"}, nil)

	reposRoot, repositories := newExecutionTestRepo(t, "demo-repo")

	req := newExecutionRequest(t, executionStartRequest{})
	w := httptest.NewRecorder()
	(&Server{Projects: newExecutionProjectStore(repositories), Tasks: tasks,
		AgentRunners: map[string]agentrunner.AgentRunner{"claude-code": runner}, ReposRoot: reposRoot}).handleStartExecution()(w, req)

	require.Equal(t, http.StatusOK, w.Code)

	sseEvents := decodeSSEEvents(t, w.Body.String())
	require.Len(t, sseEvents, 6, "reasoning events still stream over SSE")
	assert.Equal(t, "reasoning", sseEvents[0].Type)

	assert.Equal(t, []string{"text", "tool_call", "tool_result"}, loggedKinds, "reasoning must never reach the execution log")
}

// TestHandleStartExecution_ExecuteErrorRecordsFailure locks in the
// deterministic failure classification: a non-context error from Execute
// becomes failure.type "execution", still recorded (never silently
// dropped), and still surfaced as a "done" event so the frontend learns
// the outcome without a second request.
func TestHandleStartExecution_ExecuteErrorRecordsFailure(t *testing.T) {
	tasks := new(mockTaskStore)
	tasks.On("Get", "demo-project", "TASK-0001").Return(task.Task{ID: "TASK-0001", Stage: task.StageImplementation}, nil)
	tasks.On("GetPlan", "demo-project", "TASK-0001").Return(task.Plan{}, nil)
	tasks.On("ListReviews", "demo-project", "TASK-0001").Return(nil, nil)
	tasks.On("ListExecutions", "demo-project", "TASK-0001").Return(nil, nil)
	tasks.On("NextExecutionID", "demo-project", "TASK-0001").Return("exec-001", nil)
	tasks.On("CreateExecutionLog", "demo-project", "TASK-0001", "exec-001").Return(nil)
	tasks.On("AppendExecutionLogEvent", "demo-project", "TASK-0001", "exec-001", mock.Anything).Return(nil)

	var recorded task.Execution
	tasks.On("RecordExecution", "demo-project", "TASK-0001", mock.MatchedBy(func(e task.Execution) bool {
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
	(&Server{Projects: newExecutionProjectStore(repositories), Tasks: tasks,
		AgentRunners: map[string]agentrunner.AgentRunner{"claude-code": runner}, ReposRoot: reposRoot}).handleStartExecution()(w, req)

	require.Equal(t, http.StatusOK, w.Code)
	assert.Equal(t, task.ExecutionStatusFailure, recorded.Status)
	require.NotNil(t, recorded.Failure)
	assert.Equal(t, task.FailureTypeExecution, recorded.Failure.Type)
	assert.Contains(t, recorded.Failure.Message, "boom")
}

// TestHandleStartExecution_WorkspaceResolutionFailure_RecordsFailureExecution
// is a regression test: CreateExecutionLog runs before workspace resolution
// is even attempted, so a failure here used to just emit a bare SSE error
// and return, leaving that log with no matching execution.yaml record. A
// human retrying would then get the exact same executionID from
// NextExecutionID, which CreateExecutionLog would reject as already
// existing — an unrecoverable deadlock. This asserts the failure is instead
// recorded like any other, and streamed as a normal "done" event so the
// frontend's execution history shows what actually happened.
func TestHandleStartExecution_WorkspaceResolutionFailure_RecordsFailureExecution(t *testing.T) {
	tasks := new(mockTaskStore)
	tasks.On("Get", "demo-project", "TASK-0001").Return(task.Task{ID: "TASK-0001", Stage: task.StageImplementation}, nil)
	tasks.On("GetPlan", "demo-project", "TASK-0001").Return(task.Plan{}, nil)
	tasks.On("ListReviews", "demo-project", "TASK-0001").Return(nil, nil)
	tasks.On("ListExecutions", "demo-project", "TASK-0001").Return(nil, nil)
	tasks.On("NextExecutionID", "demo-project", "TASK-0001").Return("exec-001", nil)
	tasks.On("CreateExecutionLog", "demo-project", "TASK-0001", "exec-001").Return(nil)

	var recorded task.Execution
	tasks.On("RecordExecution", "demo-project", "TASK-0001", mock.MatchedBy(func(e task.Execution) bool {
		recorded = e
		return true
	})).Return(task.Execution{ExecutionID: "exec-001", Status: task.ExecutionStatusFailure}, nil)

	runner := new(mockAgentRunner)
	reposRoot, repositories := newExecutionTestRepo(t, "demo-repo")

	// The real repo is on "main" (newExecutionTestRepo), but the project
	// fixture claims a different default branch — ensureDefaultBranch
	// short-circuits on Project.DefaultBranch without ever touching the
	// repo, so ResolveExecutionWorkspace is the first thing to actually
	// notice the mismatch and fail, exactly like the checkDefaultBranch
	// error a real "shared checkout on the wrong branch" produces.
	projects := new(mockProjectStore)
	projects.On("Get", "demo-project").Return(project.Project{ID: "demo-project", Repositories: repositories, DefaultBranch: "not-main"}, nil)

	req := newExecutionRequest(t, executionStartRequest{})
	w := httptest.NewRecorder()
	(&Server{Projects: projects, Tasks: tasks,
		AgentRunners: map[string]agentrunner.AgentRunner{"claude-code": runner}, ReposRoot: reposRoot}).handleStartExecution()(w, req)

	require.Equal(t, http.StatusOK, w.Code)
	runner.AssertNotCalled(t, "Execute", mock.Anything, mock.Anything, mock.Anything)

	sseEvents := decodeSSEEvents(t, w.Body.String())
	require.Len(t, sseEvents, 1)
	assert.Equal(t, "done", sseEvents[0].Type, "a failure here must surface the same way a real Execute failure does — via the recorded Execution, not a bare error event")
	require.NotNil(t, sseEvents[0].Execution)

	assert.Equal(t, task.ExecutionStatusFailure, recorded.Status)
	require.NotNil(t, recorded.Failure)
	assert.Equal(t, task.FailureTypeExecution, recorded.Failure.Type)
	assert.Contains(t, recorded.Failure.Message, "resolving execution workspace")
	assert.Empty(t, recorded.Output.GitBranch, "the workspace was never created, so there is no branch to report")
}

// TestHandleStartExecution_PRCommentsFetchFailure_RecordsFailureExecution
// is the same regression as the workspace-resolution test above, for the
// other early-return path that runs after CreateExecutionLog but before
// runner.Execute — fetching the reopened PR's review comments.
func TestHandleStartExecution_PRCommentsFetchFailure_RecordsFailureExecution(t *testing.T) {
	reposRoot, repositories := newExecutionTestRepo(t, "demo-repo")
	repoDir := filepath.Join(reposRoot, "demo-repo")

	run := func(args ...string) string {
		cmd := exec.Command("git", args...)
		cmd.Dir = repoDir
		out, err := cmd.CombinedOutput()
		require.NoErrorf(t, err, "git %v: %s", args, out)
		return string(out)
	}
	baseBranch := strings.TrimSpace(run("rev-parse", "--abbrev-ref", "HEAD"))

	priorBranch := "task-exec/TASK-0001/exec-001"
	run("checkout", "-b", priorBranch)
	require.NoError(t, os.WriteFile(filepath.Join(repoDir, "wip.txt"), []byte("prior attempt\n"), 0o644))
	run("add", ".")
	run("commit", "-q", "-m", "prior attempt")
	run("checkout", baseBranch)

	tasks := new(mockTaskStore)
	tasks.On("Get", "demo-project", "TASK-0001").Return(task.Task{
		ID: "TASK-0001", Stage: task.StageImplementation,
		PullRequest: &task.PullRequest{URL: "https://github.com/org/repo/pull/42", Number: 42, Branch: priorBranch},
	}, nil)
	tasks.On("GetPlan", "demo-project", "TASK-0001").Return(task.Plan{}, nil)
	tasks.On("ListReviews", "demo-project", "TASK-0001").Return([]task.Review{
		{ExecutionID: "exec-001", Decision: task.ReviewDecisionNeedsChanges, Notes: "fix the widget"},
	}, nil)
	tasks.On("ListExecutions", "demo-project", "TASK-0001").Return([]task.Execution{
		{ExecutionID: "exec-001", Status: task.ExecutionStatusSuccess, Output: task.ExecutionOutput{GitBranch: priorBranch}},
	}, nil)
	tasks.On("NextExecutionID", "demo-project", "TASK-0001").Return("exec-002", nil)
	tasks.On("CreateExecutionLog", "demo-project", "TASK-0001", "exec-002").Return(nil)

	var recorded task.Execution
	tasks.On("RecordExecution", "demo-project", "TASK-0001", mock.MatchedBy(func(e task.Execution) bool {
		recorded = e
		return true
	})).Return(task.Execution{ExecutionID: "exec-002", Status: task.ExecutionStatusFailure}, nil)

	prClient := &fakeGitHubPRClient{commentsErr: errors.New(`gh api ...: 422 "per_page" is not a permitted key`)}
	runner := new(mockAgentRunner)

	req := newExecutionRequest(t, executionStartRequest{})
	w := httptest.NewRecorder()
	(&Server{Projects: newExecutionProjectStore(repositories), Tasks: tasks,
		AgentRunners: map[string]agentrunner.AgentRunner{"claude-code": runner}, ReposRoot: reposRoot, PRClient: prClient}).handleStartExecution()(w, req)

	require.Equal(t, http.StatusOK, w.Code)
	runner.AssertNotCalled(t, "Execute", mock.Anything, mock.Anything, mock.Anything)

	sseEvents := decodeSSEEvents(t, w.Body.String())
	require.Len(t, sseEvents, 1)
	assert.Equal(t, "done", sseEvents[0].Type)

	assert.Equal(t, task.ExecutionStatusFailure, recorded.Status)
	require.NotNil(t, recorded.Failure)
	assert.Contains(t, recorded.Failure.Message, "fetching PR comments")
	assert.Equal(t, "task-exec/TASK-0001/exec-002", recorded.Output.GitBranch, "the worktree itself was created successfully before this step failed")
	assert.Equal(t, priorBranch, recorded.Output.ForkedFromBranch)
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

// TestResolveReviewContinuation locks in the gating rules docs/adr/0012
// describes: only the *latest* review matters, and only a needs_changes
// decision triggers continuation — everything else (no reviews yet, or the
// latest review being some other decision) yields a fresh-from-main attempt.
func TestResolveReviewContinuation(t *testing.T) {
	t.Run("no reviews yet", func(t *testing.T) {
		tasks := new(mockTaskStore)
		tasks.On("ListReviews", "demo-project", "TASK-0001").Return(nil, nil)

		forkFrom, feedback, err := resolveReviewContinuation(tasks, "demo-project", "TASK-0001")
		require.NoError(t, err)
		assert.Empty(t, forkFrom)
		assert.Empty(t, feedback)
	})

	t.Run("latest review is rejected, not needs_changes", func(t *testing.T) {
		tasks := new(mockTaskStore)
		tasks.On("ListReviews", "demo-project", "TASK-0001").Return([]task.Review{
			{Decision: task.ReviewDecisionRejected, Notes: "wrong approach entirely"},
		}, nil)

		forkFrom, feedback, err := resolveReviewContinuation(tasks, "demo-project", "TASK-0001")
		require.NoError(t, err)
		assert.Empty(t, forkFrom)
		assert.Empty(t, feedback)
	})

	t.Run("latest review is needs_changes, uses the execution named by its ExecutionID", func(t *testing.T) {
		tasks := new(mockTaskStore)
		tasks.On("ListReviews", "demo-project", "TASK-0001").Return([]task.Review{
			{ExecutionID: "exec-001", Decision: task.ReviewDecisionApproved, Notes: "stale, from an earlier cycle"},
			{ExecutionID: "exec-002", Decision: task.ReviewDecisionNeedsChanges, Notes: "fix the widget"},
		}, nil)
		tasks.On("ListExecutions", "demo-project", "TASK-0001").Return([]task.Execution{
			{ExecutionID: "exec-001", Output: task.ExecutionOutput{GitBranch: "task-exec/TASK-0001/exec-001"}},
			{ExecutionID: "exec-002", Output: task.ExecutionOutput{GitBranch: "task-exec/TASK-0001/exec-002"}},
		}, nil)

		forkFrom, feedback, err := resolveReviewContinuation(tasks, "demo-project", "TASK-0001")
		require.NoError(t, err)
		assert.Equal(t, "task-exec/TASK-0001/exec-002", forkFrom)
		assert.Equal(t, "fix the widget", feedback)
	})

	t.Run("a failed retry after needs_changes doesn't desync the fork branch from the review", func(t *testing.T) {
		// exec-001 succeeded and was reviewed (needs_changes) — the review
		// records ExecutionID "exec-001" at FinalizeReview time
		// (internal/task/lifecycle.go). The retry, exec-002, itself failed:
		// RecordExecution records it but never advances Stage on failure, so
		// no new review exists. A further execute attempt must still fork
		// from exec-001 (what the review's ExecutionID actually names), not
		// exec-002 (a failed run nobody reviewed and irrelevant to the link).
		tasks := new(mockTaskStore)
		tasks.On("ListReviews", "demo-project", "TASK-0001").Return([]task.Review{
			{ExecutionID: "exec-001", Decision: task.ReviewDecisionNeedsChanges, Notes: "fix the widget"},
		}, nil)
		tasks.On("ListExecutions", "demo-project", "TASK-0001").Return([]task.Execution{
			{ExecutionID: "exec-001", Status: task.ExecutionStatusSuccess, Output: task.ExecutionOutput{GitBranch: "task-exec/TASK-0001/exec-001"}},
			{ExecutionID: "exec-002", Status: task.ExecutionStatusFailure, Output: task.ExecutionOutput{GitBranch: "task-exec/TASK-0001/exec-002"}},
		}, nil)

		forkFrom, feedback, err := resolveReviewContinuation(tasks, "demo-project", "TASK-0001")
		require.NoError(t, err)
		assert.Equal(t, "task-exec/TASK-0001/exec-001", forkFrom)
		assert.Equal(t, "fix the widget", feedback)
	})

	t.Run("review's ExecutionID names no execution on record yields no fork branch", func(t *testing.T) {
		tasks := new(mockTaskStore)
		tasks.On("ListReviews", "demo-project", "TASK-0001").Return([]task.Review{
			{ExecutionID: "exec-999", Decision: task.ReviewDecisionNeedsChanges, Notes: "fix the widget"},
		}, nil)
		tasks.On("ListExecutions", "demo-project", "TASK-0001").Return([]task.Execution{
			{ExecutionID: "exec-001", Status: task.ExecutionStatusSuccess, Output: task.ExecutionOutput{GitBranch: "task-exec/TASK-0001/exec-001"}},
		}, nil)

		forkFrom, feedback, err := resolveReviewContinuation(tasks, "demo-project", "TASK-0001")
		require.NoError(t, err)
		assert.Empty(t, forkFrom)
		assert.Equal(t, "fix the widget", feedback)
	})
}

// TestHandleStartExecution_NeedsChangesForksFromPriorBranch is the
// end-to-end proof for docs/adr/0012: a needs_changes retry's worktree is
// forked from the prior execution's real branch tip (so its file shows up
// in the new worktree), not a blank checkout of main, and the review's
// notes reach both the prompt and the recorded ExecutionInput.
func TestHandleStartExecution_NeedsChangesForksFromPriorBranch(t *testing.T) {
	reposRoot, repositories := newExecutionTestRepo(t, "demo-repo")
	repoDir := filepath.Join(reposRoot, "demo-repo")

	run := func(args ...string) string {
		cmd := exec.Command("git", args...)
		cmd.Dir = repoDir
		out, err := cmd.CombinedOutput()
		require.NoErrorf(t, err, "git %v: %s", args, out)
		return string(out)
	}
	baseBranch := strings.TrimSpace(run("rev-parse", "--abbrev-ref", "HEAD"))

	// Simulate a prior execution attempt's branch, with a file that exists
	// only there — proves the new worktree came from this branch rather
	// than a fresh checkout of main.
	priorBranch := "task-exec/TASK-0001/exec-001"
	run("checkout", "-b", priorBranch)
	require.NoError(t, os.WriteFile(filepath.Join(repoDir, "wip.txt"), []byte("prior attempt\n"), 0o644))
	run("add", ".")
	run("commit", "-q", "-m", "prior attempt")
	run("checkout", baseBranch)

	tasks := new(mockTaskStore)
	tasks.On("Get", "demo-project", "TASK-0001").Return(task.Task{ID: "TASK-0001", Stage: task.StageImplementation, Objective: "ship it"}, nil)
	tasks.On("GetPlan", "demo-project", "TASK-0001").Return(task.Plan{Approach: "do it"}, nil)
	tasks.On("ListReviews", "demo-project", "TASK-0001").Return([]task.Review{
		{ExecutionID: "exec-001", Decision: task.ReviewDecisionNeedsChanges, Notes: "fix the widget"},
	}, nil)
	tasks.On("ListExecutions", "demo-project", "TASK-0001").Return([]task.Execution{
		{ExecutionID: "exec-001", Status: task.ExecutionStatusSuccess, Output: task.ExecutionOutput{GitBranch: priorBranch}},
	}, nil)
	tasks.On("NextExecutionID", "demo-project", "TASK-0001").Return("exec-002", nil)
	tasks.On("CreateExecutionLog", "demo-project", "TASK-0001", "exec-002").Return(nil)
	tasks.On("AppendExecutionLogEvent", "demo-project", "TASK-0001", "exec-002", mock.Anything).Return(nil)

	var recorded task.Execution
	tasks.On("RecordExecution", "demo-project", "TASK-0001", mock.MatchedBy(func(e task.Execution) bool {
		recorded = e
		return true
	})).Return(task.Execution{ExecutionID: "exec-002", Status: task.ExecutionStatusSuccess}, nil)

	runner := new(mockAgentRunner)
	var gotIn agentrunner.ExecuteInput
	runner.On("Execute", mock.Anything, mock.MatchedBy(func(in agentrunner.ExecuteInput) bool {
		gotIn = in
		return true
	}), mock.Anything).Return(nil, agentrunner.ExecuteOutput{Content: "done"}, nil)

	req := newExecutionRequest(t, executionStartRequest{})
	w := httptest.NewRecorder()
	(&Server{Projects: newExecutionProjectStore(repositories), Tasks: tasks,
		AgentRunners: map[string]agentrunner.AgentRunner{"claude-code": runner}, ReposRoot: reposRoot}).handleStartExecution()(w, req)

	require.Equal(t, http.StatusOK, w.Code)

	assert.Equal(t, "fix the widget", recorded.Input.ReviewFeedback)
	assert.Equal(t, priorBranch, recorded.Output.ForkedFromBranch, "recorded so a later retry's Commits can be scoped to just its own contribution")
	assert.Contains(t, gotIn.SystemPrompt, "Continuing prior work")
	assert.Contains(t, gotIn.SystemPrompt, "fix the widget")

	assert.FileExists(t, filepath.Join(reposRoot, ".worktrees", "demo-repo", "TASK-0001", "exec-002", "wip.txt"))
}

// TestHandleStartExecution_NeedsChangesWithOpenPR_WritesAndCleansUpPRComments
// is the end-to-end proof for docs/adr/0015: a needs_changes retry with a PR
// already open gets the PR's comments fetched and written into the worktree
// before Execute runs (visible to Execute via the file the mock asserts
// inside its own call), referenced in the prompt by name, and deleted again
// immediately after Execute returns — before the response is sent, so it can
// never leak into the pushed branch.
func TestHandleStartExecution_NeedsChangesWithOpenPR_WritesAndCleansUpPRComments(t *testing.T) {
	reposRoot, repositories := newExecutionTestRepo(t, "demo-repo")
	repoDir := filepath.Join(reposRoot, "demo-repo")

	run := func(args ...string) string {
		cmd := exec.Command("git", args...)
		cmd.Dir = repoDir
		out, err := cmd.CombinedOutput()
		require.NoErrorf(t, err, "git %v: %s", args, out)
		return string(out)
	}
	baseBranch := strings.TrimSpace(run("rev-parse", "--abbrev-ref", "HEAD"))

	priorBranch := "task-exec/TASK-0001/exec-001"
	run("checkout", "-b", priorBranch)
	require.NoError(t, os.WriteFile(filepath.Join(repoDir, "wip.txt"), []byte("prior attempt\n"), 0o644))
	run("add", ".")
	run("commit", "-q", "-m", "prior attempt")
	run("checkout", baseBranch)

	tasks := new(mockTaskStore)
	tasks.On("Get", "demo-project", "TASK-0001").Return(task.Task{
		ID: "TASK-0001", Stage: task.StageImplementation, Objective: "ship it",
		PullRequest: &task.PullRequest{URL: "https://github.com/org/repo/pull/42", Number: 42, Branch: "task-exec/TASK-0001/exec-001"},
	}, nil)
	tasks.On("GetPlan", "demo-project", "TASK-0001").Return(task.Plan{Approach: "do it"}, nil)
	tasks.On("ListReviews", "demo-project", "TASK-0001").Return([]task.Review{
		{ExecutionID: "exec-001", Decision: task.ReviewDecisionNeedsChanges, Notes: "fix the widget"},
	}, nil)
	tasks.On("ListExecutions", "demo-project", "TASK-0001").Return([]task.Execution{
		{ExecutionID: "exec-001", Status: task.ExecutionStatusSuccess, Output: task.ExecutionOutput{GitBranch: priorBranch}},
	}, nil)
	tasks.On("NextExecutionID", "demo-project", "TASK-0001").Return("exec-002", nil)
	tasks.On("CreateExecutionLog", "demo-project", "TASK-0001", "exec-002").Return(nil)
	tasks.On("AppendExecutionLogEvent", "demo-project", "TASK-0001", "exec-002", mock.Anything).Return(nil)
	tasks.On("RecordExecution", "demo-project", "TASK-0001", mock.Anything).Return(task.Execution{ExecutionID: "exec-002", Status: task.ExecutionStatusSuccess}, nil)

	prClient := &fakeGitHubPRClient{comments: "- kind: review\n  author: alice\n  state: CHANGES_REQUESTED\n  body: fix it\n"}

	var worktreePath string
	var fileExistedDuringExecute bool
	runner := new(mockAgentRunner)
	var gotIn agentrunner.ExecuteInput
	runner.On("Execute", mock.Anything, mock.MatchedBy(func(in agentrunner.ExecuteInput) bool {
		gotIn = in
		return true
	}), mock.Anything).
		Run(func(args mock.Arguments) {
			in := args.Get(1).(agentrunner.ExecuteInput)
			worktreePath = in.Workspace
			_, err := os.Stat(filepath.Join(worktreePath, prCommentsExecutionFilename))
			fileExistedDuringExecute = err == nil
		}).
		Return(nil, agentrunner.ExecuteOutput{Content: "done"}, nil)

	req := newExecutionRequest(t, executionStartRequest{})
	w := httptest.NewRecorder()
	(&Server{Projects: newExecutionProjectStore(repositories), Tasks: tasks,
		AgentRunners: map[string]agentrunner.AgentRunner{"claude-code": runner}, ReposRoot: reposRoot, PRClient: prClient}).handleStartExecution()(w, req)

	require.Equal(t, http.StatusOK, w.Code)

	assert.True(t, fileExistedDuringExecute, "pr-comments.yaml must exist in the worktree while Execute is running")
	assert.Contains(t, gotIn.SystemPrompt, prCommentsExecutionFilename)
	assert.NoFileExists(t, filepath.Join(worktreePath, prCommentsExecutionFilename), "must be deleted immediately after Execute returns")
}

func TestHandleListExecutions_OK(t *testing.T) {
	tasks := new(mockTaskStore)
	tasks.On("ListExecutions", "demo-project", "TASK-0001").Return([]task.Execution{
		{ExecutionID: "exec-001", Status: task.ExecutionStatusSuccess},
	}, nil)

	req := httptest.NewRequest(http.MethodGet, "/api/v1/projects/demo-project/tasks/TASK-0001/executions", nil)
	req.SetPathValue("projectId", "demo-project")
	req.SetPathValue("taskId", "TASK-0001")
	w := httptest.NewRecorder()
	(&Server{Projects: newExecutionProjectStore(nil), Tasks: tasks}).handleListExecutions()(w, req)

	require.Equal(t, http.StatusOK, w.Code)
	var got map[string][]task.Execution
	require.NoError(t, json.Unmarshal(w.Body.Bytes(), &got))
	require.Len(t, got["executions"], 1)
	assert.Equal(t, "exec-001", got["executions"][0].ExecutionID)
}

// TestResolveFailureContinuation locks in resolveFailureContinuation's
// eligibility rules: only the most recent execution is ever considered, it
// must not have succeeded, it must actually have commits (nothing worth
// continuing from otherwise), and a non-empty reviewForkFrom (a
// needs_changes retry already auto-continuing, docs/adr/0012) always wins,
// hiding this path entirely rather than competing with it.
func TestResolveFailureContinuation(t *testing.T) {
	t.Run("no executions yet", func(t *testing.T) {
		tasks := new(mockTaskStore)
		tasks.On("ListExecutions", "demo-project", "TASK-0001").Return(nil, nil)

		executionID, forkFrom, msg, err := resolveFailureContinuation(tasks, "demo-project", "TASK-0001", "")
		require.NoError(t, err)
		assert.Empty(t, executionID)
		assert.Empty(t, forkFrom)
		assert.Empty(t, msg)
	})

	t.Run("most recent execution succeeded", func(t *testing.T) {
		tasks := new(mockTaskStore)
		tasks.On("ListExecutions", "demo-project", "TASK-0001").Return([]task.Execution{
			{ExecutionID: "exec-001", Status: task.ExecutionStatusSuccess, Output: task.ExecutionOutput{Commits: []string{"abc"}}},
		}, nil)

		executionID, _, _, err := resolveFailureContinuation(tasks, "demo-project", "TASK-0001", "")
		require.NoError(t, err)
		assert.Empty(t, executionID)
	})

	t.Run("most recent execution failed but has no commits", func(t *testing.T) {
		tasks := new(mockTaskStore)
		tasks.On("ListExecutions", "demo-project", "TASK-0001").Return([]task.Execution{
			{ExecutionID: "exec-001", Status: task.ExecutionStatusFailure, Failure: &task.ExecutionFailure{Message: "context deadline exceeded"}},
		}, nil)

		executionID, _, _, err := resolveFailureContinuation(tasks, "demo-project", "TASK-0001", "")
		require.NoError(t, err)
		assert.Empty(t, executionID, "an instant failure before any work happened has nothing worth continuing from")
	})

	t.Run("most recent execution failed with commits is eligible", func(t *testing.T) {
		tasks := new(mockTaskStore)
		tasks.On("ListExecutions", "demo-project", "TASK-0001").Return([]task.Execution{
			{ExecutionID: "exec-001", Status: task.ExecutionStatusFailure, Output: task.ExecutionOutput{
				GitBranch: "task-exec/TASK-0001/exec-001", Commits: []string{"abc"},
			}, Failure: &task.ExecutionFailure{Message: "Reached maximum number of turns (100)"}},
		}, nil)

		executionID, forkFrom, msg, err := resolveFailureContinuation(tasks, "demo-project", "TASK-0001", "")
		require.NoError(t, err)
		assert.Equal(t, "exec-001", executionID)
		assert.Equal(t, "task-exec/TASK-0001/exec-001", forkFrom)
		assert.Equal(t, "Reached maximum number of turns (100)", msg)
	})

	t.Run("a non-empty reviewForkFrom hides this path entirely", func(t *testing.T) {
		tasks := new(mockTaskStore)
		// Deliberately no ListExecutions expectation: reviewForkFrom being
		// set must short-circuit before ever calling it.
		executionID, forkFrom, msg, err := resolveFailureContinuation(tasks, "demo-project", "TASK-0001", "task-exec/TASK-0001/exec-001")
		require.NoError(t, err)
		assert.Empty(t, executionID)
		assert.Empty(t, forkFrom)
		assert.Empty(t, msg)
		tasks.AssertNotCalled(t, "ListExecutions", "demo-project", "demo-project", mock.Anything)
	})
}

// TestHandleStartExecution_ContinueFromFailureForksFromPriorBranch is the
// end-to-end proof that a human-chosen continuation behaves like ADR-0012's
// needs_changes retry: the new worktree is forked from the prior failed
// execution's real branch tip, the choice is recorded on
// Input.ContinuedFromExecutionID, and the prompt carries the prior failure
// message plus explicit resume guidance.
func TestHandleStartExecution_ContinueFromFailureForksFromPriorBranch(t *testing.T) {
	reposRoot, repositories := newExecutionTestRepo(t, "demo-repo")
	repoDir := filepath.Join(reposRoot, "demo-repo")

	run := func(args ...string) string {
		cmd := exec.Command("git", args...)
		cmd.Dir = repoDir
		out, err := cmd.CombinedOutput()
		require.NoErrorf(t, err, "git %v: %s", args, out)
		return string(out)
	}
	baseBranch := strings.TrimSpace(run("rev-parse", "--abbrev-ref", "HEAD"))

	priorBranch := "task-exec/TASK-0001/exec-001"
	run("checkout", "-b", priorBranch)
	require.NoError(t, os.WriteFile(filepath.Join(repoDir, "wip.txt"), []byte("prior attempt\n"), 0o644))
	run("add", ".")
	run("commit", "-q", "-m", "prior attempt")
	run("checkout", baseBranch)

	tasks := new(mockTaskStore)
	tasks.On("Get", "demo-project", "TASK-0001").Return(task.Task{ID: "TASK-0001", Stage: task.StageImplementation, Objective: "ship it"}, nil)
	tasks.On("GetPlan", "demo-project", "TASK-0001").Return(task.Plan{Approach: "do it"}, nil)
	tasks.On("ListReviews", "demo-project", "TASK-0001").Return(nil, nil)
	tasks.On("ListExecutions", "demo-project", "TASK-0001").Return([]task.Execution{
		{ExecutionID: "exec-001", Status: task.ExecutionStatusFailure, Output: task.ExecutionOutput{
			GitBranch: priorBranch, Commits: []string{"deadbeef"},
		}, Failure: &task.ExecutionFailure{Type: task.FailureTypeExecution, Message: "Reached maximum number of turns (100)"}},
	}, nil)
	tasks.On("NextExecutionID", "demo-project", "TASK-0001").Return("exec-002", nil)
	tasks.On("CreateExecutionLog", "demo-project", "TASK-0001", "exec-002").Return(nil)
	tasks.On("AppendExecutionLogEvent", "demo-project", "TASK-0001", "exec-002", mock.Anything).Return(nil)

	var recorded task.Execution
	tasks.On("RecordExecution", "demo-project", "TASK-0001", mock.MatchedBy(func(e task.Execution) bool {
		recorded = e
		return true
	})).Return(task.Execution{ExecutionID: "exec-002", Status: task.ExecutionStatusSuccess}, nil)

	runner := new(mockAgentRunner)
	var gotIn agentrunner.ExecuteInput
	runner.On("Execute", mock.Anything, mock.MatchedBy(func(in agentrunner.ExecuteInput) bool {
		gotIn = in
		return true
	}), mock.Anything).Return(nil, agentrunner.ExecuteOutput{Content: "done"}, nil)

	req := newExecutionRequest(t, executionStartRequest{ContinueFromExecutionID: "exec-001"})
	w := httptest.NewRecorder()
	(&Server{Projects: newExecutionProjectStore(repositories), Tasks: tasks,
		AgentRunners: map[string]agentrunner.AgentRunner{"claude-code": runner}, ReposRoot: reposRoot}).handleStartExecution()(w, req)

	require.Equal(t, http.StatusOK, w.Code)

	assert.Equal(t, "exec-001", recorded.Input.ContinuedFromExecutionID)
	assert.Equal(t, priorBranch, recorded.Output.ForkedFromBranch)
	assert.Contains(t, gotIn.SystemPrompt, "Continuing a prior failed attempt")
	assert.Contains(t, gotIn.SystemPrompt, "Reached maximum number of turns (100)")

	assert.FileExists(t, filepath.Join(reposRoot, ".worktrees", "demo-repo", "TASK-0001", "exec-002", "wip.txt"))
}

// TestHandleStartExecution_ContinueFromExecutionID_MismatchRejected proves
// the human's choice is re-validated server-side rather than trusted
// outright — if the requested execution id no longer matches what
// resolveFailureContinuation currently considers eligible (state moved on
// since the hint was fetched), the request is rejected rather than silently
// falling back to a fresh run or continuing from the wrong branch.
func TestHandleStartExecution_ContinueFromExecutionID_MismatchRejected(t *testing.T) {
	tasks := new(mockTaskStore)
	tasks.On("Get", "demo-project", "TASK-0001").Return(task.Task{ID: "TASK-0001", Stage: task.StageImplementation}, nil)
	tasks.On("GetPlan", "demo-project", "TASK-0001").Return(task.Plan{}, nil)
	tasks.On("ListReviews", "demo-project", "TASK-0001").Return(nil, nil)
	tasks.On("ListExecutions", "demo-project", "TASK-0001").Return(nil, nil) // nothing eligible anymore

	runner := new(mockAgentRunner)
	reposRoot, repositories := newExecutionTestRepo(t, "demo-repo")

	req := newExecutionRequest(t, executionStartRequest{ContinueFromExecutionID: "exec-001"})
	w := httptest.NewRecorder()
	(&Server{Projects: newExecutionProjectStore(repositories), Tasks: tasks,
		AgentRunners: map[string]agentrunner.AgentRunner{"claude-code": runner}, ReposRoot: reposRoot}).handleStartExecution()(w, req)

	assert.Equal(t, http.StatusConflict, w.Code)
	runner.AssertNotCalled(t, "Execute", mock.Anything, mock.Anything, mock.Anything)
}

// TestHandleStartExecution_SafetyCommitsUncommittedWorkOnFailure proves the
// data-loss fix: when Execute ends without success and leaves the worktree
// dirty, the harness commits that outstanding work itself before
// CollectExecutionOutput runs — otherwise it would be invisible to git
// log/diff and silently left behind the moment a later attempt forks a
// fresh worktree from this branch's tip.
func TestHandleStartExecution_SafetyCommitsUncommittedWorkOnFailure(t *testing.T) {
	tasks := new(mockTaskStore)
	tasks.On("Get", "demo-project", "TASK-0001").Return(task.Task{ID: "TASK-0001", Stage: task.StageImplementation}, nil)
	tasks.On("GetPlan", "demo-project", "TASK-0001").Return(task.Plan{}, nil)
	tasks.On("ListReviews", "demo-project", "TASK-0001").Return(nil, nil)
	tasks.On("ListExecutions", "demo-project", "TASK-0001").Return(nil, nil)
	tasks.On("NextExecutionID", "demo-project", "TASK-0001").Return("exec-001", nil)
	tasks.On("CreateExecutionLog", "demo-project", "TASK-0001", "exec-001").Return(nil)
	tasks.On("AppendExecutionLogEvent", "demo-project", "TASK-0001", "exec-001", mock.Anything).Return(nil)

	var recorded task.Execution
	tasks.On("RecordExecution", "demo-project", "TASK-0001", mock.MatchedBy(func(e task.Execution) bool {
		recorded = e
		return true
	})).Return(task.Execution{ExecutionID: "exec-001", Status: task.ExecutionStatusFailure}, nil)

	runner := new(mockAgentRunner)
	runner.On("Execute", mock.Anything, mock.Anything, mock.Anything).
		Run(func(args mock.Arguments) {
			in := args.Get(1).(agentrunner.ExecuteInput)
			require.NoError(t, os.WriteFile(filepath.Join(in.Workspace, "unfinished.txt"), []byte("mid-edit\n"), 0o644))
		}).
		Return(nil, agentrunner.ExecuteOutput{}, errors.New("claude code execution failed: Reached maximum number of turns (100)"))

	reposRoot, repositories := newExecutionTestRepo(t, "demo-repo")

	req := newExecutionRequest(t, executionStartRequest{})
	w := httptest.NewRecorder()
	(&Server{Projects: newExecutionProjectStore(repositories), Tasks: tasks,
		AgentRunners: map[string]agentrunner.AgentRunner{"claude-code": runner}, ReposRoot: reposRoot}).handleStartExecution()(w, req)

	require.Equal(t, http.StatusOK, w.Code)
	assert.Equal(t, task.ExecutionStatusFailure, recorded.Status)
	require.Len(t, recorded.Output.Commits, 1, "the safety commit must be recorded as this attempt's own contribution")
	assert.Contains(t, recorded.Output.Artifacts, "unfinished.txt", "the uncommitted file must show up once safety-committed")
}

// TestHandleGetContinuableExecution_Eligible/NotEligible/HiddenByNeedsChanges
// lock in that the GET endpoint is a thin wrapper over
// resolveReviewContinuation+resolveFailureContinuation — the same rule
// handleStartExecution enforces, not a separately-maintained one.
func TestHandleGetContinuableExecution_Eligible(t *testing.T) {
	tasks := new(mockTaskStore)
	tasks.On("ListReviews", "demo-project", "TASK-0001").Return(nil, nil)
	tasks.On("ListExecutions", "demo-project", "TASK-0001").Return([]task.Execution{
		{ExecutionID: "exec-002", Status: task.ExecutionStatusFailure, Output: task.ExecutionOutput{
			GitBranch: "task-exec/TASK-0001/exec-002", Commits: []string{"abc"},
		}},
	}, nil)

	req := httptest.NewRequest(http.MethodGet, "/api/v1/projects/demo-project/tasks/TASK-0001/executions/continuable", nil)
	req.SetPathValue("projectId", "demo-project")
	req.SetPathValue("taskId", "TASK-0001")
	w := httptest.NewRecorder()
	(&Server{Projects: newExecutionProjectStore(nil), Tasks: tasks}).handleGetContinuableExecution()(w, req)

	require.Equal(t, http.StatusOK, w.Code)
	var got continuableExecutionResponse
	require.NoError(t, json.Unmarshal(w.Body.Bytes(), &got))
	assert.Equal(t, "exec-002", got.ExecutionID)
}

func TestHandleGetContinuableExecution_NotEligible(t *testing.T) {
	tasks := new(mockTaskStore)
	tasks.On("ListReviews", "demo-project", "TASK-0001").Return(nil, nil)
	tasks.On("ListExecutions", "demo-project", "TASK-0001").Return(nil, nil)

	req := httptest.NewRequest(http.MethodGet, "/api/v1/projects/demo-project/tasks/TASK-0001/executions/continuable", nil)
	req.SetPathValue("projectId", "demo-project")
	req.SetPathValue("taskId", "TASK-0001")
	w := httptest.NewRecorder()
	(&Server{Projects: newExecutionProjectStore(nil), Tasks: tasks}).handleGetContinuableExecution()(w, req)

	require.Equal(t, http.StatusOK, w.Code)
	var got continuableExecutionResponse
	require.NoError(t, json.Unmarshal(w.Body.Bytes(), &got))
	assert.Empty(t, got.ExecutionID)
}

func TestHandleGetContinuableExecution_HiddenByNeedsChanges(t *testing.T) {
	tasks := new(mockTaskStore)
	tasks.On("ListReviews", "demo-project", "TASK-0001").Return([]task.Review{
		{ExecutionID: "exec-001", Decision: task.ReviewDecisionNeedsChanges, Notes: "fix the widget"},
	}, nil)
	tasks.On("ListExecutions", "demo-project", "TASK-0001").Return([]task.Execution{
		{ExecutionID: "exec-001", Status: task.ExecutionStatusSuccess, Output: task.ExecutionOutput{GitBranch: "task-exec/TASK-0001/exec-001"}},
	}, nil)

	req := httptest.NewRequest(http.MethodGet, "/api/v1/projects/demo-project/tasks/TASK-0001/executions/continuable", nil)
	req.SetPathValue("projectId", "demo-project")
	req.SetPathValue("taskId", "TASK-0001")
	w := httptest.NewRecorder()
	(&Server{Projects: newExecutionProjectStore(nil), Tasks: tasks}).handleGetContinuableExecution()(w, req)

	require.Equal(t, http.StatusOK, w.Code)
	var got continuableExecutionResponse
	require.NoError(t, json.Unmarshal(w.Body.Bytes(), &got))
	assert.Empty(t, got.ExecutionID, "a needs_changes retry already owns forkFrom, so this must stay empty")
}

// isCleanupPrompt distinguishes the workspace-cleanup follow-up turn's
// system prompt (buildWorkspaceCleanupPrompt) from the main execution's
// (buildExecutionPrompt) in these mock Execute expectations, since a single
// success-with-dirty-workspace run issues both, in order.
func isCleanupPrompt(in agentrunner.ExecuteInput) bool {
	return strings.Contains(in.SystemPrompt, "uncommitted changes")
}

// TestHandleStartExecution_CleanupTurnResolvesDirtyWorkspace proves the
// success-path counterpart to the failure-path safety commit: a successful
// run that leaves scratch files uncommitted gets a dedicated follow-up turn
// naming exactly what's dirty, and when that turn itself commits the
// remainder, the execution records clean (WorkspaceDirty false) with the
// cleanup's own commit picked up by CollectExecutionOutput.
func TestHandleStartExecution_CleanupTurnResolvesDirtyWorkspace(t *testing.T) {
	tasks := new(mockTaskStore)
	tasks.On("Get", "demo-project", "TASK-0001").Return(task.Task{ID: "TASK-0001", Stage: task.StageImplementation}, nil)
	tasks.On("GetPlan", "demo-project", "TASK-0001").Return(task.Plan{}, nil)
	tasks.On("ListReviews", "demo-project", "TASK-0001").Return(nil, nil)
	tasks.On("ListExecutions", "demo-project", "TASK-0001").Return(nil, nil)
	tasks.On("NextExecutionID", "demo-project", "TASK-0001").Return("exec-001", nil)
	tasks.On("CreateExecutionLog", "demo-project", "TASK-0001", "exec-001").Return(nil)
	tasks.On("AppendExecutionLogEvent", "demo-project", "TASK-0001", "exec-001", mock.Anything).Return(nil)

	var recorded task.Execution
	tasks.On("RecordExecution", "demo-project", "TASK-0001", mock.MatchedBy(func(e task.Execution) bool {
		recorded = e
		return true
	})).Return(task.Execution{ExecutionID: "exec-001", Status: task.ExecutionStatusSuccess}, nil)

	runner := new(mockAgentRunner)
	runner.On("Execute", mock.Anything, mock.MatchedBy(func(in agentrunner.ExecuteInput) bool { return !isCleanupPrompt(in) }), mock.Anything).
		Run(func(args mock.Arguments) {
			in := args.Get(1).(agentrunner.ExecuteInput)
			require.NoError(t, os.WriteFile(filepath.Join(in.Workspace, "temp.txt"), []byte("scratch\n"), 0o644))
		}).
		Return(nil, agentrunner.ExecuteOutput{Content: "done", DurationSeconds: 1}, nil)
	runner.On("Execute", mock.Anything, mock.MatchedBy(isCleanupPrompt), mock.Anything).
		Run(func(args mock.Arguments) {
			in := args.Get(1).(agentrunner.ExecuteInput)
			runGit := func(gitArgs ...string) {
				cmd := exec.Command("git", gitArgs...)
				cmd.Dir = in.Workspace
				out, err := cmd.CombinedOutput()
				require.NoErrorf(t, err, "git %v: %s", gitArgs, out)
			}
			runGit("add", "-A")
			runGit("commit", "-q", "-m", "cleanup")
		}).
		Return(nil, agentrunner.ExecuteOutput{Content: "cleaned up", DurationSeconds: 0.5}, nil)

	reposRoot, repositories := newExecutionTestRepo(t, "demo-repo")

	req := newExecutionRequest(t, executionStartRequest{})
	w := httptest.NewRecorder()
	(&Server{Projects: newExecutionProjectStore(repositories), Tasks: tasks,
		AgentRunners: map[string]agentrunner.AgentRunner{"claude-code": runner}, ReposRoot: reposRoot}).handleStartExecution()(w, req)

	require.Equal(t, http.StatusOK, w.Code)
	runner.AssertNumberOfCalls(t, "Execute", 2)
	assert.False(t, recorded.Output.WorkspaceDirty)
	assert.Len(t, recorded.Output.Commits, 1, "the cleanup turn's own commit must be picked up")
	assert.Equal(t, 1.5, recorded.Metrics.DurationSeconds, "both turns' durations are folded into one recorded attempt")
}

// TestHandleStartExecution_CleanupTurnLeavesWorkspaceDirty proves the other
// half: when the agent's cleanup turn doesn't actually resolve the dirty
// state (ignored the instruction, disagreed, or ran out of turns), the
// harness doesn't silently commit or delete anything itself — it just
// records WorkspaceDirty so a human notices, and the execution still
// reports success (the original implementation work did succeed).
func TestHandleStartExecution_CleanupTurnLeavesWorkspaceDirty(t *testing.T) {
	tasks := new(mockTaskStore)
	tasks.On("Get", "demo-project", "TASK-0001").Return(task.Task{ID: "TASK-0001", Stage: task.StageImplementation}, nil)
	tasks.On("GetPlan", "demo-project", "TASK-0001").Return(task.Plan{}, nil)
	tasks.On("ListReviews", "demo-project", "TASK-0001").Return(nil, nil)
	tasks.On("ListExecutions", "demo-project", "TASK-0001").Return(nil, nil)
	tasks.On("NextExecutionID", "demo-project", "TASK-0001").Return("exec-001", nil)
	tasks.On("CreateExecutionLog", "demo-project", "TASK-0001", "exec-001").Return(nil)
	tasks.On("AppendExecutionLogEvent", "demo-project", "TASK-0001", "exec-001", mock.Anything).Return(nil)

	var recorded task.Execution
	tasks.On("RecordExecution", "demo-project", "TASK-0001", mock.MatchedBy(func(e task.Execution) bool {
		recorded = e
		return true
	})).Return(task.Execution{ExecutionID: "exec-001", Status: task.ExecutionStatusSuccess}, nil)

	runner := new(mockAgentRunner)
	runner.On("Execute", mock.Anything, mock.MatchedBy(func(in agentrunner.ExecuteInput) bool { return !isCleanupPrompt(in) }), mock.Anything).
		Run(func(args mock.Arguments) {
			in := args.Get(1).(agentrunner.ExecuteInput)
			require.NoError(t, os.WriteFile(filepath.Join(in.Workspace, "temp.txt"), []byte("scratch\n"), 0o644))
		}).
		Return(nil, agentrunner.ExecuteOutput{Content: "done"}, nil)
	// The cleanup turn runs but does nothing to the workspace — simulates
	// the agent leaving it dirty anyway.
	runner.On("Execute", mock.Anything, mock.MatchedBy(isCleanupPrompt), mock.Anything).
		Return(nil, agentrunner.ExecuteOutput{Content: "left it"}, nil)

	reposRoot, repositories := newExecutionTestRepo(t, "demo-repo")

	req := newExecutionRequest(t, executionStartRequest{})
	w := httptest.NewRecorder()
	(&Server{Projects: newExecutionProjectStore(repositories), Tasks: tasks,
		AgentRunners: map[string]agentrunner.AgentRunner{"claude-code": runner}, ReposRoot: reposRoot}).handleStartExecution()(w, req)

	require.Equal(t, http.StatusOK, w.Code)
	assert.Equal(t, task.ExecutionStatusSuccess, recorded.Status, "the original implementation work still succeeded")
	assert.True(t, recorded.Output.WorkspaceDirty)
	assert.Empty(t, recorded.Output.Commits, "nothing was committed on the agent's behalf")
}

// TestHandleStartExecution_CleanWorkspaceSkipsCleanupTurn proves the
// cleanup turn is reactive, not standing overhead: a successful run that
// already leaves a clean tree gets exactly one Execute call, not two.
func TestHandleStartExecution_CleanWorkspaceSkipsCleanupTurn(t *testing.T) {
	tasks := new(mockTaskStore)
	tasks.On("Get", "demo-project", "TASK-0001").Return(task.Task{ID: "TASK-0001", Stage: task.StageImplementation}, nil)
	tasks.On("GetPlan", "demo-project", "TASK-0001").Return(task.Plan{}, nil)
	tasks.On("ListReviews", "demo-project", "TASK-0001").Return(nil, nil)
	tasks.On("ListExecutions", "demo-project", "TASK-0001").Return(nil, nil)
	tasks.On("NextExecutionID", "demo-project", "TASK-0001").Return("exec-001", nil)
	tasks.On("CreateExecutionLog", "demo-project", "TASK-0001", "exec-001").Return(nil)
	tasks.On("AppendExecutionLogEvent", "demo-project", "TASK-0001", "exec-001", mock.Anything).Return(nil)
	tasks.On("RecordExecution", "demo-project", "TASK-0001", mock.Anything).Return(task.Execution{ExecutionID: "exec-001", Status: task.ExecutionStatusSuccess}, nil)

	runner := new(mockAgentRunner)
	runner.On("Execute", mock.Anything, mock.Anything, mock.Anything).Return(nil, agentrunner.ExecuteOutput{Content: "done"}, nil)

	reposRoot, repositories := newExecutionTestRepo(t, "demo-repo")

	req := newExecutionRequest(t, executionStartRequest{})
	w := httptest.NewRecorder()
	(&Server{Projects: newExecutionProjectStore(repositories), Tasks: tasks,
		AgentRunners: map[string]agentrunner.AgentRunner{"claude-code": runner}, ReposRoot: reposRoot}).handleStartExecution()(w, req)

	require.Equal(t, http.StatusOK, w.Code)
	runner.AssertNumberOfCalls(t, "Execute", 1)
}

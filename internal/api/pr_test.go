package api

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/mock"
	"github.com/stretchr/testify/require"

	"github.com/timmersuk/llm-workbench/internal/agentrunner"
	"github.com/timmersuk/llm-workbench/internal/gitutil"
	"github.com/timmersuk/llm-workbench/internal/project"
	"github.com/timmersuk/llm-workbench/internal/task"
)

// fakeGitHubPRClient stands in for the real `gh` CLI in tests, mirroring
// agentrunner's own test double of the same shape (docs/milestones/done/milestone7.md
// PR 2 decision 4) — no network or GitHub auth involved.
type fakeGitHubPRClient struct {
	createCalls int
	createURL   string
	createErr   error
	state       string
	comments    agentrunner.PRCommentsYAML
	commentsErr error
}

func (f *fakeGitHubPRClient) Create(_ context.Context, _, _, _, _, _ string) (string, int, error) {
	f.createCalls++
	if f.createErr != nil {
		return "", 0, f.createErr
	}
	url := f.createURL
	if url == "" {
		url = "https://github.com/org/repo/pull/42"
	}
	return url, 42, nil
}

func (f *fakeGitHubPRClient) State(_ context.Context, _ string, _ int) (string, error) {
	if f.state == "" {
		return "OPEN", nil
	}
	return f.state, nil
}

func (f *fakeGitHubPRClient) Comments(_ context.Context, _ string, _ int) (agentrunner.PRCommentsYAML, error) {
	if f.commentsErr != nil {
		return "", f.commentsErr
	}
	return f.comments, nil
}

// initBareRemoteForPush wires a local bare repo as dir's "origin" remote — a
// real remote to push against, mirroring agentrunner/pr_test.go's
// initBareRemote (unexported there, so reproduced here rather than exported
// solely for this test).
func initBareRemoteForPush(t *testing.T, root, dir string) string {
	t.Helper()
	remoteDir := filepath.Join(root, "origin.git")
	require.NoError(t, os.MkdirAll(remoteDir, 0o755))
	gitRun(t, remoteDir, "init", "--bare", "-q")
	gitRun(t, dir, "remote", "add", "origin", remoteDir)
	return remoteDir
}

// seedPRReviewTask drives a task through the public store API to pr_review
// (requirements -> planning -> implementation -> review -> pr_review, the
// last hop via an approved verdict — task.FinalizeReview's Milestone 7 PR 2
// retarget), recording an execution whose GitBranch matches the real branch
// initReviewRepo's worktree created, so PushAndOpenPR has a real ref to push.
func seedPRReviewTask(t *testing.T, store *task.FileStore, id, reviewNotes string) {
	t.Helper()
	_, err := store.Create("demo-project", task.Task{ID: id, Title: "A"})
	require.NoError(t, err)
	_, err = store.FinalizeRequirements("demo-project", id, task.RequirementsDraft{Objective: "ship it"})
	require.NoError(t, err)
	_, err = store.FinalizePlan("demo-project", id, task.Plan{Approach: "do it"})
	require.NoError(t, err)
	require.NoError(t, store.CreateExecutionLog("demo-project", id, "exec-001"))
	_, err = store.RecordExecution("demo-project", id, task.Execution{
		ExecutionID: "exec-001",
		Status:      task.ExecutionStatusSuccess,
		Output:      task.ExecutionOutput{GitBranch: agentrunner.ExecutionBranchName(id, "exec-001")},
	})
	require.NoError(t, err)
	_, err = store.FinalizeReview("demo-project", id, task.ReviewDraft{Decision: task.ReviewDecisionApproved, Notes: reviewNotes})
	require.NoError(t, err)
}

func TestHandlePushPR_NoExistingPR_PushesAndRecords(t *testing.T) {
	reposRoot := t.TempDir()
	initReviewRepo(t, reposRoot) // shared checkout "myrepo" + exec-001 worktree with a real commit
	remoteDir := initBareRemoteForPush(t, reposRoot, filepath.Join(reposRoot, "myrepo"))

	store := task.NewFileStore(t.TempDir())
	seedPRReviewTask(t, store, "task-a", "tests pass")

	projects := new(mockProjectStore)
	projects.On("Get", "demo-project").Return(project.Project{ID: "demo-project", Repositories: []string{"github.com/x/myrepo"}}, nil)

	client := &fakeGitHubPRClient{createURL: "https://github.com/org/repo/pull/7"}

	req := newProjectRequest(t, http.MethodPost, "/api/v1/projects/demo-project/tasks/task-a/pr/push", nil)
	req.SetPathValue("projectId", "demo-project")
	req.SetPathValue("taskId", "task-a")
	w := httptest.NewRecorder()
	(&Server{Projects: projects, Tasks: store, ReposRoot: reposRoot, PRClient: client}).handlePushPR()(w, req)

	require.Equal(t, http.StatusOK, w.Code, w.Body.String())
	var got task.Task
	require.NoError(t, json.Unmarshal(w.Body.Bytes(), &got))
	require.NotNil(t, got.PullRequest)
	assert.Equal(t, "https://github.com/org/repo/pull/7", got.PullRequest.URL)
	assert.Equal(t, 42, got.PullRequest.Number)
	assert.Equal(t, "task-exec/task-a/exec-001", got.PullRequest.Branch)
	assert.Equal(t, task.StagePRReview, got.Stage, "pushing doesn't itself change stage")
	assert.Equal(t, 1, client.createCalls)

	out, err := gitutil.RunGit(context.Background(), remoteDir, "branch", "--list", "task-exec/task-a/exec-001")
	require.NoError(t, err)
	assert.NotEmpty(t, out, "the execution branch must actually land on the remote")
}

func TestHandlePushPR_WrongStageRejectedBeforeAnyGitActivity(t *testing.T) {
	reposRoot := t.TempDir()
	initReviewRepo(t, reposRoot)
	initBareRemoteForPush(t, reposRoot, filepath.Join(reposRoot, "myrepo"))

	store := task.NewFileStore(t.TempDir())
	_, err := store.Create("demo-project", task.Task{ID: "task-a", Title: "A"})
	require.NoError(t, err) // stays at requirements, nowhere near pr_review

	projects := new(mockProjectStore)
	projects.On("Get", "demo-project").Return(project.Project{ID: "demo-project", Repositories: []string{"github.com/x/myrepo"}}, nil)

	client := &fakeGitHubPRClient{}

	req := newProjectRequest(t, http.MethodPost, "/api/v1/projects/demo-project/tasks/task-a/pr/push", nil)
	req.SetPathValue("projectId", "demo-project")
	req.SetPathValue("taskId", "task-a")
	w := httptest.NewRecorder()
	(&Server{Projects: projects, Tasks: store, ReposRoot: reposRoot, PRClient: client}).handlePushPR()(w, req)

	assert.Equal(t, http.StatusConflict, w.Code)
	assert.Equal(t, 0, client.createCalls, "no PR should be opened for a task not at pr_review")
}

func TestHandlePushPR_GitHubClientErrorMapsTo500(t *testing.T) {
	reposRoot := t.TempDir()
	initReviewRepo(t, reposRoot)
	initBareRemoteForPush(t, reposRoot, filepath.Join(reposRoot, "myrepo"))

	store := task.NewFileStore(t.TempDir())
	seedPRReviewTask(t, store, "task-a", "")

	projects := new(mockProjectStore)
	projects.On("Get", "demo-project").Return(project.Project{ID: "demo-project", Repositories: []string{"github.com/x/myrepo"}}, nil)

	client := &fakeGitHubPRClient{createErr: fmt.Errorf("gh: not authenticated")}

	req := newProjectRequest(t, http.MethodPost, "/api/v1/projects/demo-project/tasks/task-a/pr/push", nil)
	req.SetPathValue("projectId", "demo-project")
	req.SetPathValue("taskId", "task-a")
	w := httptest.NewRecorder()
	(&Server{Projects: projects, Tasks: store, ReposRoot: reposRoot, PRClient: client}).handlePushPR()(w, req)

	assert.Equal(t, http.StatusInternalServerError, w.Code)
}

// TestHandleMarkPRMerged_NoExecutionsAdvancesStraightToMerged covers a task
// with nothing to clean up (no recorded executions — an edge case, but the
// simplest one that doesn't need a real git checkout): CleanupTaskWorktrees
// has nothing to iterate, so the pass is vacuously all-clean and the
// handler advances straight through cleanup to merged.
func TestHandleMarkPRMerged_NoExecutionsAdvancesStraightToMerged(t *testing.T) {
	projects := new(mockProjectStore)
	projects.On("Get", "demo-project").Return(project.Project{ID: "demo-project"}, nil)

	parked := task.Task{ID: "task-a", Stage: task.StageCleanup, PullRequest: &task.PullRequest{URL: "https://github.com/org/repo/pull/7", Number: 7, Branch: "task-exec/task-a/exec-001"}}
	merged := task.Task{ID: "task-a", Stage: task.StageCompleted, PullRequest: parked.PullRequest}
	tasks := new(mockTaskStore)
	tasks.On("MarkPRMerged", "demo-project", "task-a").Return(parked, nil)
	tasks.On("ListExecutions", "demo-project", "task-a").Return([]task.Execution{}, nil)
	tasks.On("SetCleanupStatus", "demo-project", "task-a", []task.CleanupWorktreeStatus{}).Return(parked, nil)
	tasks.On("CompleteCleanup", "demo-project", "task-a").Return(merged, nil)

	req := newProjectRequest(t, http.MethodPost, "/api/v1/projects/demo-project/tasks/task-a/pr/merged", nil)
	req.SetPathValue("projectId", "demo-project")
	req.SetPathValue("taskId", "task-a")
	w := httptest.NewRecorder()
	(&Server{Projects: projects, Tasks: tasks}).handleMarkPRMerged()(w, req)

	require.Equal(t, http.StatusOK, w.Code)
	var got task.Task
	require.NoError(t, json.Unmarshal(w.Body.Bytes(), &got))
	assert.Equal(t, task.StageCompleted, got.Stage)
	tasks.AssertCalled(t, "CompleteCleanup", "demo-project", "task-a")
}

// TestHandleMarkPRMerged_ProjectResolutionFailureStillRecordsMerge asserts
// the ordering fix: store.MarkPRMerged must run (and its result must be what
// comes back, still 200) even when resolving the project for the cleanup
// pass itself fails afterwards. Cleanup is best-effort with respect to the
// HTTP response — it must never cost the human their "mark as merged"
// assertion.
func TestHandleMarkPRMerged_ProjectResolutionFailureStillRecordsMerge(t *testing.T) {
	projects := new(mockProjectStore)
	projects.On("Get", "demo-project").Return(project.Project{}, fmt.Errorf("boom"))

	parked := task.Task{ID: "task-a", Stage: task.StageCleanup, PullRequest: &task.PullRequest{URL: "https://github.com/org/repo/pull/7", Number: 7, Branch: "task-exec/task-a/exec-001"}}
	tasks := new(mockTaskStore)
	tasks.On("MarkPRMerged", "demo-project", "task-a").Return(parked, nil)

	req := newProjectRequest(t, http.MethodPost, "/api/v1/projects/demo-project/tasks/task-a/pr/merged", nil)
	req.SetPathValue("projectId", "demo-project")
	req.SetPathValue("taskId", "task-a")
	w := httptest.NewRecorder()
	(&Server{Projects: projects, Tasks: tasks}).handleMarkPRMerged()(w, req)

	require.Equal(t, http.StatusOK, w.Code, "project resolution failing must never turn a recorded merge into an HTTP error")
	tasks.AssertCalled(t, "MarkPRMerged", "demo-project", "task-a")
	var got task.Task
	require.NoError(t, json.Unmarshal(w.Body.Bytes(), &got))
	assert.Equal(t, task.StageCleanup, got.Stage)
	tasks.AssertNotCalled(t, "ListExecutions", mock.Anything, mock.Anything)
	tasks.AssertNotCalled(t, "SetCleanupStatus", mock.Anything, mock.Anything, mock.Anything)
}

func TestHandleMarkPRMerged_WrongStageRejected(t *testing.T) {
	projects := new(mockProjectStore)
	projects.On("Get", "demo-project").Return(project.Project{ID: "demo-project"}, nil)

	tasks := new(mockTaskStore)
	tasks.On("MarkPRMerged", "demo-project", "task-a").Return(nil, task.ErrWrongStage)

	req := newProjectRequest(t, http.MethodPost, "/api/v1/projects/demo-project/tasks/task-a/pr/merged", nil)
	req.SetPathValue("projectId", "demo-project")
	req.SetPathValue("taskId", "task-a")
	w := httptest.NewRecorder()
	(&Server{Projects: projects, Tasks: tasks}).handleMarkPRMerged()(w, req)

	assert.Equal(t, http.StatusConflict, w.Code)
	tasks.AssertNotCalled(t, "ListExecutions", mock.Anything, mock.Anything)
}

// TestHandleMarkPRMerged_SkippedWorktreeParksAtCleanupWithReport drives the
// handler against a real worktree with an uncommitted change, so the
// cleanup routine's safety check actually trips: the task must be returned
// still parked at task.StageCleanup, carrying a CleanupStatus report, never
// advanced to merged, and never turned into an HTTP error.
func TestHandleMarkPRMerged_SkippedWorktreeParksAtCleanupWithReport(t *testing.T) {
	reposRoot := t.TempDir()
	ws := initReviewRepo(t, reposRoot) // "myrepo" shared checkout + exec-001 worktree
	require.NoError(t, os.WriteFile(filepath.Join(ws.Path, "dirty.txt"), []byte("uncommitted\n"), 0o644))

	projects := new(mockProjectStore)
	projects.On("Get", "demo-project").Return(project.Project{ID: "demo-project", Repositories: []string{"github.com/x/myrepo"}}, nil)

	parked := task.Task{ID: "task-a", Stage: task.StageCleanup, PullRequest: &task.PullRequest{URL: "https://github.com/org/repo/pull/7", Number: 7, Branch: "task-exec/task-a/exec-001"}}
	tasks := new(mockTaskStore)
	tasks.On("MarkPRMerged", "demo-project", "task-a").Return(parked, nil)
	tasks.On("ListExecutions", "demo-project", "task-a").Return([]task.Execution{{ExecutionID: "exec-001"}}, nil)
	var savedStatus []task.CleanupWorktreeStatus
	tasks.On("SetCleanupStatus", "demo-project", "task-a", mock.Anything).
		Run(func(args mock.Arguments) { savedStatus = args.Get(2).([]task.CleanupWorktreeStatus) }).
		Return(parked, nil)

	req := newProjectRequest(t, http.MethodPost, "/api/v1/projects/demo-project/tasks/task-a/pr/merged", nil)
	req.SetPathValue("projectId", "demo-project")
	req.SetPathValue("taskId", "task-a")
	w := httptest.NewRecorder()
	(&Server{Projects: projects, Tasks: tasks, ReposRoot: reposRoot}).handleMarkPRMerged()(w, req)

	require.Equal(t, http.StatusOK, w.Code, "a skipped worktree must never fail the response")
	var got task.Task
	require.NoError(t, json.Unmarshal(w.Body.Bytes(), &got))
	assert.Equal(t, task.StageCleanup, got.Stage, "parked, not merged")
	tasks.AssertNotCalled(t, "CompleteCleanup", mock.Anything, mock.Anything)

	require.Len(t, savedStatus, 1)
	assert.Equal(t, "exec-001", savedStatus[0].ExecutionID)
	assert.Equal(t, task.CleanupOutcomeSkipped, savedStatus[0].Outcome)
	assert.NotEmpty(t, savedStatus[0].Reason)
	assert.DirExists(t, ws.Path, "the dirty worktree must be left in place")
}

func TestHandleTaskCleanup_WrongStageRejected(t *testing.T) {
	projects := new(mockProjectStore)
	projects.On("Get", "demo-project").Return(project.Project{ID: "demo-project"}, nil)

	tasks := new(mockTaskStore)
	tasks.On("Get", "demo-project", "task-a").Return(task.Task{ID: "task-a", Stage: task.StagePRReview}, nil)

	req := newProjectRequest(t, http.MethodPost, "/api/v1/projects/demo-project/tasks/task-a/cleanup", nil)
	req.SetPathValue("projectId", "demo-project")
	req.SetPathValue("taskId", "task-a")
	w := httptest.NewRecorder()
	(&Server{Projects: projects, Tasks: tasks}).handleTaskCleanup()(w, req)

	assert.Equal(t, http.StatusConflict, w.Code)
	tasks.AssertNotCalled(t, "ListExecutions", mock.Anything, mock.Anything)
}

// TestHandleTaskCleanup_RetrySucceedsAfterWorktreeCommitted drives a task
// parked at cleanup through a real retry: once the previously-dirty
// worktree is committed, POST .../cleanup (force: false) must remove it and
// advance to merged.
func TestHandleTaskCleanup_RetrySucceedsAfterWorktreeCommitted(t *testing.T) {
	reposRoot := t.TempDir()
	ws := initReviewRepo(t, reposRoot)
	initBareRemoteForPush(t, reposRoot, filepath.Join(reposRoot, "myrepo"))
	_, err := gitutil.RunGit(context.Background(), filepath.Join(reposRoot, "myrepo"), "push", "origin", ws.Branch)
	require.NoError(t, err)

	projects := new(mockProjectStore)
	projects.On("Get", "demo-project").Return(project.Project{ID: "demo-project", Repositories: []string{"github.com/x/myrepo"}}, nil)

	current := task.Task{ID: "task-a", Stage: task.StageCleanup, PullRequest: &task.PullRequest{URL: "https://github.com/org/repo/pull/7", Number: 7, Branch: ws.Branch}}
	merged := task.Task{ID: "task-a", Stage: task.StageCompleted, PullRequest: current.PullRequest}
	tasks := new(mockTaskStore)
	tasks.On("Get", "demo-project", "task-a").Return(current, nil)
	tasks.On("ListExecutions", "demo-project", "task-a").Return([]task.Execution{{ExecutionID: "exec-001"}}, nil)
	tasks.On("SetCleanupStatus", "demo-project", "task-a", mock.Anything).Return(current, nil)
	tasks.On("CompleteCleanup", "demo-project", "task-a").Return(merged, nil)

	req := newProjectRequest(t, http.MethodPost, "/api/v1/projects/demo-project/tasks/task-a/cleanup", nil)
	req.SetPathValue("projectId", "demo-project")
	req.SetPathValue("taskId", "task-a")
	w := httptest.NewRecorder()
	(&Server{Projects: projects, Tasks: tasks, ReposRoot: reposRoot}).handleTaskCleanup()(w, req)

	require.Equal(t, http.StatusOK, w.Code, w.Body.String())
	var got task.Task
	require.NoError(t, json.Unmarshal(w.Body.Bytes(), &got))
	assert.Equal(t, task.StageCompleted, got.Stage)
	assert.NoDirExists(t, ws.Path)
}

// TestHandleTaskCleanup_ForceRemovesDirtyWorktree drives the force override
// against a real dirty worktree: without force it would be skipped, with
// force it must be removed and the task advanced to merged.
func TestHandleTaskCleanup_ForceRemovesDirtyWorktree(t *testing.T) {
	reposRoot := t.TempDir()
	ws := initReviewRepo(t, reposRoot)
	require.NoError(t, os.WriteFile(filepath.Join(ws.Path, "dirty.txt"), []byte("uncommitted\n"), 0o644))

	projects := new(mockProjectStore)
	projects.On("Get", "demo-project").Return(project.Project{ID: "demo-project", Repositories: []string{"github.com/x/myrepo"}}, nil)

	current := task.Task{ID: "task-a", Stage: task.StageCleanup, PullRequest: &task.PullRequest{URL: "https://github.com/org/repo/pull/7", Number: 7, Branch: ws.Branch}}
	merged := task.Task{ID: "task-a", Stage: task.StageCompleted, PullRequest: current.PullRequest}
	tasks := new(mockTaskStore)
	tasks.On("Get", "demo-project", "task-a").Return(current, nil)
	tasks.On("ListExecutions", "demo-project", "task-a").Return([]task.Execution{{ExecutionID: "exec-001"}}, nil)
	var savedStatus []task.CleanupWorktreeStatus
	tasks.On("SetCleanupStatus", "demo-project", "task-a", mock.Anything).
		Run(func(args mock.Arguments) { savedStatus = args.Get(2).([]task.CleanupWorktreeStatus) }).
		Return(current, nil)
	tasks.On("CompleteCleanup", "demo-project", "task-a").Return(merged, nil)

	req := newProjectRequest(t, http.MethodPost, "/api/v1/projects/demo-project/tasks/task-a/cleanup", map[string]bool{"force": true})
	req.SetPathValue("projectId", "demo-project")
	req.SetPathValue("taskId", "task-a")
	w := httptest.NewRecorder()
	(&Server{Projects: projects, Tasks: tasks, ReposRoot: reposRoot}).handleTaskCleanup()(w, req)

	require.Equal(t, http.StatusOK, w.Code, w.Body.String())
	var got task.Task
	require.NoError(t, json.Unmarshal(w.Body.Bytes(), &got))
	assert.Equal(t, task.StageCompleted, got.Stage)
	assert.NoDirExists(t, ws.Path)
	require.Len(t, savedStatus, 1)
	assert.Equal(t, task.CleanupOutcomeRemoved, savedStatus[0].Outcome)
}

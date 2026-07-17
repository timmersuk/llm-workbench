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
	"github.com/stretchr/testify/require"

	"github.com/timmersuk/llm-workbench/internal/agentrunner"
	"github.com/timmersuk/llm-workbench/internal/gitutil"
	"github.com/timmersuk/llm-workbench/internal/project"
	"github.com/timmersuk/llm-workbench/internal/task"
)

// fakeGitHubPRClient stands in for the real `gh` CLI in tests, mirroring
// agentrunner's own test double of the same shape (docs/milestones/milestone7.md
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
	_, err := store.Create(task.Task{ID: id, Title: "A"})
	require.NoError(t, err)
	_, err = store.FinalizeRequirements(id, task.RequirementsDraft{Objective: "ship it"})
	require.NoError(t, err)
	_, err = store.FinalizePlan(id, task.Plan{Approach: "do it"})
	require.NoError(t, err)
	_, err = store.RecordExecution(id, task.Execution{
		ExecutionID: "exec-001",
		Status:      task.ExecutionStatusSuccess,
		Output:      task.ExecutionOutput{GitBranch: agentrunner.ExecutionBranchName(id, "exec-001")},
	})
	require.NoError(t, err)
	_, err = store.FinalizeReview(id, task.ReviewDraft{Decision: task.ReviewDecisionApproved, Notes: reviewNotes})
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
	projects.On("TasksRoot", "demo-project").Return("unused", nil)

	client := &fakeGitHubPRClient{createURL: "https://github.com/org/repo/pull/7"}

	req := newProjectRequest(t, http.MethodPost, "/api/v1/projects/demo-project/tasks/task-a/pr/push", nil)
	req.SetPathValue("projectId", "demo-project")
	req.SetPathValue("taskId", "task-a")
	w := httptest.NewRecorder()
	handlePushPR(projects, fixedTaskStoreFactory(store), reposRoot, client)(w, req)

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
	_, err := store.Create(task.Task{ID: "task-a", Title: "A"})
	require.NoError(t, err) // stays at requirements, nowhere near pr_review

	projects := new(mockProjectStore)
	projects.On("Get", "demo-project").Return(project.Project{ID: "demo-project", Repositories: []string{"github.com/x/myrepo"}}, nil)
	projects.On("TasksRoot", "demo-project").Return("unused", nil)

	client := &fakeGitHubPRClient{}

	req := newProjectRequest(t, http.MethodPost, "/api/v1/projects/demo-project/tasks/task-a/pr/push", nil)
	req.SetPathValue("projectId", "demo-project")
	req.SetPathValue("taskId", "task-a")
	w := httptest.NewRecorder()
	handlePushPR(projects, fixedTaskStoreFactory(store), reposRoot, client)(w, req)

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
	projects.On("TasksRoot", "demo-project").Return("unused", nil)

	client := &fakeGitHubPRClient{createErr: fmt.Errorf("gh: not authenticated")}

	req := newProjectRequest(t, http.MethodPost, "/api/v1/projects/demo-project/tasks/task-a/pr/push", nil)
	req.SetPathValue("projectId", "demo-project")
	req.SetPathValue("taskId", "task-a")
	w := httptest.NewRecorder()
	handlePushPR(projects, fixedTaskStoreFactory(store), reposRoot, client)(w, req)

	assert.Equal(t, http.StatusInternalServerError, w.Code)
}

func TestHandleMarkPRMerged_OK(t *testing.T) {
	projects := new(mockProjectStore)
	projects.On("Get", "demo-project").Return(project.Project{ID: "demo-project"}, nil)
	projects.On("TasksRoot", "demo-project").Return("/data/projects/demo-project/tasks", nil)

	updated := task.Task{ID: "task-a", Stage: task.StageMerged, PullRequest: &task.PullRequest{URL: "https://github.com/org/repo/pull/7", Number: 7}}
	tasks := new(mockTaskStore)
	tasks.On("MarkPRMerged", "task-a").Return(updated, nil)

	req := newProjectRequest(t, http.MethodPost, "/api/v1/projects/demo-project/tasks/task-a/pr/merged", nil)
	req.SetPathValue("projectId", "demo-project")
	req.SetPathValue("taskId", "task-a")
	w := httptest.NewRecorder()
	handleMarkPRMerged(projects, fixedTaskStoreFactory(tasks))(w, req)

	require.Equal(t, http.StatusOK, w.Code)
	var got task.Task
	require.NoError(t, json.Unmarshal(w.Body.Bytes(), &got))
	assert.Equal(t, task.StageMerged, got.Stage)
}

func TestHandleMarkPRMerged_WrongStageRejected(t *testing.T) {
	projects := new(mockProjectStore)
	projects.On("Get", "demo-project").Return(project.Project{ID: "demo-project"}, nil)
	projects.On("TasksRoot", "demo-project").Return("/data/projects/demo-project/tasks", nil)

	tasks := new(mockTaskStore)
	tasks.On("MarkPRMerged", "task-a").Return(nil, task.ErrWrongStage)

	req := newProjectRequest(t, http.MethodPost, "/api/v1/projects/demo-project/tasks/task-a/pr/merged", nil)
	req.SetPathValue("projectId", "demo-project")
	req.SetPathValue("taskId", "task-a")
	w := httptest.NewRecorder()
	handleMarkPRMerged(projects, fixedTaskStoreFactory(tasks))(w, req)

	assert.Equal(t, http.StatusConflict, w.Code)
}

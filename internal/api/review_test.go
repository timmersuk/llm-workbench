package api

import (
	"bytes"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/mock"
	"github.com/stretchr/testify/require"

	"github.com/timmersuk/llm-workbench/internal/agentrunner"
	"github.com/timmersuk/llm-workbench/internal/project"
	"github.com/timmersuk/llm-workbench/internal/task"
)

func TestHandleFinalizeReview_OK(t *testing.T) {
	projects := new(mockProjectStore)
	projects.On("Get", "demo-project").Return(project.Project{ID: "demo-project"}, nil)

	draft := task.ReviewDraft{Decision: task.ReviewDecisionApproved, Notes: "looks good"}
	updated := task.Task{ID: "TASK-0001", Stage: task.StageCompleted}
	recorded := task.Review{ReviewID: "review-001", TaskID: "TASK-0001", Decision: task.ReviewDecisionApproved, Notes: "looks good"}

	tasks := new(mockTaskStore)
	tasks.On("FinalizeReview", "demo-project", "TASK-0001", draft).Return(updated, nil)
	tasks.On("ListReviews", "demo-project", "TASK-0001").Return([]task.Review{recorded}, nil)

	req := newProjectRequest(t, http.MethodPost, "/api/v1/projects/demo-project/tasks/TASK-0001/review/finalize", draft)
	req.SetPathValue("projectId", "demo-project")
	req.SetPathValue("taskId", "TASK-0001")
	w := httptest.NewRecorder()
	(&Server{Projects: projects, Tasks: tasks}).handleFinalizeReview()(w, req)

	require.Equal(t, http.StatusOK, w.Code)
	var got finalizeReviewResponse
	require.NoError(t, json.Unmarshal(w.Body.Bytes(), &got))
	assert.Equal(t, task.StageCompleted, got.Task.Stage)
	assert.Equal(t, task.ReviewDecisionApproved, got.Review.Decision)
	assert.Equal(t, "looks good", got.Review.Notes)
}

func TestHandleFinalizeReview_WrongStage(t *testing.T) {
	projects := new(mockProjectStore)
	projects.On("Get", "demo-project").Return(project.Project{ID: "demo-project"}, nil)

	draft := task.ReviewDraft{Decision: task.ReviewDecisionApproved}
	tasks := new(mockTaskStore)
	tasks.On("FinalizeReview", "demo-project", "TASK-0001", draft).Return(nil, task.ErrWrongStage)

	req := newProjectRequest(t, http.MethodPost, "/api/v1/projects/demo-project/tasks/TASK-0001/review/finalize", draft)
	req.SetPathValue("projectId", "demo-project")
	req.SetPathValue("taskId", "TASK-0001")
	w := httptest.NewRecorder()
	(&Server{Projects: projects, Tasks: tasks}).handleFinalizeReview()(w, req)

	assert.Equal(t, http.StatusConflict, w.Code)
}

func TestHandleFinalizeReview_InvalidBody(t *testing.T) {
	projects := new(mockProjectStore)
	projects.On("Get", "demo-project").Return(project.Project{ID: "demo-project"}, nil)

	tasks := new(mockTaskStore)

	req := httptest.NewRequest(http.MethodPost, "/api/v1/projects/demo-project/tasks/TASK-0001/review/finalize", bytes.NewReader([]byte("not json")))
	req.SetPathValue("projectId", "demo-project")
	req.SetPathValue("taskId", "TASK-0001")
	w := httptest.NewRecorder()
	(&Server{Projects: projects, Tasks: tasks}).handleFinalizeReview()(w, req)

	assert.Equal(t, http.StatusBadRequest, w.Code)
}

// The review conversation is conceptually done once finalized, so its agent
// session is torn down — same contract handleFinalizeRequirements/Plan honor.
func TestHandleFinalizeReview_ClosesAgentSessionsOnSuccess(t *testing.T) {
	projects := new(mockProjectStore)
	projects.On("Get", "demo-project").Return(project.Project{ID: "demo-project"}, nil)

	draft := task.ReviewDraft{Decision: task.ReviewDecisionNeedsChanges}
	updated := task.Task{ID: "TASK-0001", Stage: task.StageImplementation}
	tasks := new(mockTaskStore)
	tasks.On("FinalizeReview", "demo-project", "TASK-0001", draft).Return(updated, nil)
	tasks.On("ListReviews", "demo-project", "TASK-0001").Return([]task.Review{{ReviewID: "review-001", Decision: task.ReviewDecisionNeedsChanges}}, nil)

	runner := new(mockAgentRunner)
	runner.On("CloseSession", "TASK-0001:"+task.StageReview)

	req := newProjectRequest(t, http.MethodPost, "/api/v1/projects/demo-project/tasks/TASK-0001/review/finalize", draft)
	req.SetPathValue("projectId", "demo-project")
	req.SetPathValue("taskId", "TASK-0001")
	w := httptest.NewRecorder()
	(&Server{Projects: projects, Tasks: tasks, AgentRunners: map[string]agentrunner.AgentRunner{"claude-code": runner}}).handleFinalizeReview()(w, req)

	require.Equal(t, http.StatusOK, w.Code)
	runner.AssertCalled(t, "CloseSession", "TASK-0001:"+task.StageReview)
}

func TestHandleFinalizeReview_DoesNotCloseSessionsWhenFinalizeFails(t *testing.T) {
	projects := new(mockProjectStore)
	projects.On("Get", "demo-project").Return(project.Project{ID: "demo-project"}, nil)

	draft := task.ReviewDraft{Decision: task.ReviewDecisionApproved}
	tasks := new(mockTaskStore)
	tasks.On("FinalizeReview", "demo-project", "TASK-0001", draft).Return(nil, task.ErrWrongStage)

	runner := new(mockAgentRunner)

	req := newProjectRequest(t, http.MethodPost, "/api/v1/projects/demo-project/tasks/TASK-0001/review/finalize", draft)
	req.SetPathValue("projectId", "demo-project")
	req.SetPathValue("taskId", "TASK-0001")
	w := httptest.NewRecorder()
	(&Server{Projects: projects, Tasks: tasks, AgentRunners: map[string]agentrunner.AgentRunner{"claude-code": runner}}).handleFinalizeReview()(w, req)

	assert.Equal(t, http.StatusConflict, w.Code)
	runner.AssertNotCalled(t, "CloseSession", mock.Anything)
}

func TestHandleListReviews_OK(t *testing.T) {
	projects := new(mockProjectStore)
	projects.On("Get", "demo-project").Return(project.Project{ID: "demo-project"}, nil)

	reviews := []task.Review{
		{ReviewID: "review-001", Decision: task.ReviewDecisionNeedsChanges, Notes: "fix the edge case"},
		{ReviewID: "review-002", Decision: task.ReviewDecisionApproved, Notes: "good now"},
	}
	tasks := new(mockTaskStore)
	tasks.On("ListReviews", "demo-project", "TASK-0001").Return(reviews, nil)

	req := newProjectRequest(t, http.MethodGet, "/api/v1/projects/demo-project/tasks/TASK-0001/reviews", nil)
	req.SetPathValue("projectId", "demo-project")
	req.SetPathValue("taskId", "TASK-0001")
	w := httptest.NewRecorder()
	(&Server{Projects: projects, Tasks: tasks}).handleListReviews()(w, req)

	require.Equal(t, http.StatusOK, w.Code)
	var got struct {
		Reviews []task.Review `json:"reviews"`
	}
	require.NoError(t, json.Unmarshal(w.Body.Bytes(), &got))
	require.Len(t, got.Reviews, 2)
	assert.Equal(t, "review-002", got.Reviews[1].ReviewID)
	assert.Equal(t, task.ReviewDecisionApproved, got.Reviews[1].Decision)
}

func TestHandleReviewDiff_OK(t *testing.T) {
	reposRoot := t.TempDir()
	initReviewRepo(t, reposRoot)

	store := task.NewFileStore(t.TempDir())
	seedReviewableTask(t, store, "task-a")

	projects := new(mockProjectStore)
	projects.On("Get", "demo-project").Return(project.Project{ID: "demo-project", Repositories: []string{"github.com/x/myrepo"}, DefaultBranch: "main"}, nil)

	req := newProjectRequest(t, http.MethodGet, "/api/v1/projects/demo-project/tasks/task-a/review/diff", nil)
	req.SetPathValue("projectId", "demo-project")
	req.SetPathValue("taskId", "task-a")
	w := httptest.NewRecorder()
	(&Server{Projects: projects, Tasks: store, ReposRoot: reposRoot}).handleReviewDiff()(w, req)

	require.Equal(t, http.StatusOK, w.Code)
	var got reviewDiffResponse
	require.NoError(t, json.Unmarshal(w.Body.Bytes(), &got))
	assert.Contains(t, got.Patch, "feature.go") // the real diff is returned verbatim
}

func TestHandleReviewDiff_NoExecutionIsNotFound(t *testing.T) {
	reposRoot := t.TempDir()
	initReviewRepo(t, reposRoot)

	store := task.NewFileStore(t.TempDir())
	_, err := store.Create("demo-project", task.Task{ID: "task-b", Title: "B"})
	require.NoError(t, err)

	projects := new(mockProjectStore)
	projects.On("Get", "demo-project").Return(project.Project{ID: "demo-project", Repositories: []string{"github.com/x/myrepo"}, DefaultBranch: "main"}, nil)

	req := newProjectRequest(t, http.MethodGet, "/api/v1/projects/demo-project/tasks/task-b/review/diff", nil)
	req.SetPathValue("projectId", "demo-project")
	req.SetPathValue("taskId", "task-b")
	w := httptest.NewRecorder()
	(&Server{Projects: projects, Tasks: store, ReposRoot: reposRoot}).handleReviewDiff()(w, req)

	assert.Equal(t, http.StatusNotFound, w.Code)
}

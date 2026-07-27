package api

import (
	"bytes"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/mock"
	"github.com/stretchr/testify/require"

	"github.com/timmersuk/llm-workbench/internal/agentrunner"
	"github.com/timmersuk/llm-workbench/internal/project"
	"github.com/timmersuk/llm-workbench/internal/task"
)

func TestHandleFinalizeRequirements_OK(t *testing.T) {
	projects := new(mockProjectStore)
	projects.On("Get", "demo-project").Return(project.Project{ID: "demo-project"}, nil)

	draft := task.RequirementsDraft{Objective: "ship login", Context: task.Context{Summary: "adds login"}}
	updated := task.Task{ID: "TASK-0001", Stage: task.StagePlanning, Objective: "ship login"}

	tasks := new(mockTaskStore)
	tasks.On("FinalizeRequirements", "demo-project", "TASK-0001", draft).Return(updated, nil)
	tasks.On("GetContext", "demo-project", "TASK-0001").Return(draft.Context, nil)

	req := newProjectRequest(t, http.MethodPost, "/api/v1/projects/demo-project/tasks/TASK-0001/requirements/finalize", draft)
	req.SetPathValue("projectId", "demo-project")
	req.SetPathValue("taskId", "TASK-0001")
	w := httptest.NewRecorder()
	(&Server{Projects: projects, Tasks: tasks}).handleFinalizeRequirements()(w, req)

	require.Equal(t, http.StatusOK, w.Code)
	var got finalizeRequirementsResponse
	require.NoError(t, json.Unmarshal(w.Body.Bytes(), &got))
	assert.Equal(t, task.StagePlanning, got.Task.Stage)
	assert.Equal(t, "adds login", got.Context.Summary)
}

func TestHandleFinalizeRequirements_WrongStage(t *testing.T) {
	projects := new(mockProjectStore)
	projects.On("Get", "demo-project").Return(project.Project{ID: "demo-project"}, nil)

	draft := task.RequirementsDraft{}
	tasks := new(mockTaskStore)
	tasks.On("FinalizeRequirements", "demo-project", "TASK-0001", draft).Return(nil, task.ErrWrongStage)

	req := newProjectRequest(t, http.MethodPost, "/api/v1/projects/demo-project/tasks/TASK-0001/requirements/finalize", draft)
	req.SetPathValue("projectId", "demo-project")
	req.SetPathValue("taskId", "TASK-0001")
	w := httptest.NewRecorder()
	(&Server{Projects: projects, Tasks: tasks}).handleFinalizeRequirements()(w, req)

	assert.Equal(t, http.StatusConflict, w.Code)
}

func TestHandleFinalizeRequirements_InvalidBody(t *testing.T) {
	projects := new(mockProjectStore)
	projects.On("Get", "demo-project").Return(project.Project{ID: "demo-project"}, nil)

	tasks := new(mockTaskStore)

	req := httptest.NewRequest(http.MethodPost, "/api/v1/projects/demo-project/tasks/TASK-0001/requirements/finalize", bytes.NewReader([]byte("not json")))
	req.SetPathValue("projectId", "demo-project")
	req.SetPathValue("taskId", "TASK-0001")
	w := httptest.NewRecorder()
	(&Server{Projects: projects, Tasks: tasks}).handleFinalizeRequirements()(w, req)

	assert.Equal(t, http.StatusBadRequest, w.Code)
}

// TestHandleFinalizeRequirements_RemovesPRCommentsScratchFileOnSuccess is
// the end-to-end proof for docs/adr/0015's cleanup timing: the
// rejected-review PR-comments scratch file (buildRejectedReviewContext)
// survives exactly as long as the whole reopened Requirements conversation,
// deleted only once FinalizeRequirements actually succeeds.
func TestHandleFinalizeRequirements_RemovesPRCommentsScratchFileOnSuccess(t *testing.T) {
	reposRoot, repositories := newExecutionTestRepo(t, "demo-repo")
	workspace := filepath.Join(reposRoot, "demo-repo")

	scratchPath := prCommentsRequirementsPath(workspace, "TASK-0001")
	require.NoError(t, os.MkdirAll(filepath.Dir(scratchPath), 0o755))
	require.NoError(t, os.WriteFile(scratchPath, []byte("- kind: comment\n"), 0o644))

	projects := new(mockProjectStore)
	projects.On("Get", "demo-project").Return(project.Project{ID: "demo-project", Repositories: repositories}, nil)

	draft := task.RequirementsDraft{Objective: "ship login"}
	updated := task.Task{ID: "TASK-0001", Stage: task.StagePlanning}
	tasks := new(mockTaskStore)
	tasks.On("FinalizeRequirements", "demo-project", "TASK-0001", draft).Return(updated, nil)
	tasks.On("GetContext", "demo-project", "TASK-0001").Return(task.Context{}, nil)

	req := newProjectRequest(t, http.MethodPost, "/api/v1/projects/demo-project/tasks/TASK-0001/requirements/finalize", draft)
	req.SetPathValue("projectId", "demo-project")
	req.SetPathValue("taskId", "TASK-0001")
	w := httptest.NewRecorder()
	(&Server{Projects: projects, Tasks: tasks, ReposRoot: reposRoot}).handleFinalizeRequirements()(w, req)

	require.Equal(t, http.StatusOK, w.Code)
	assert.NoFileExists(t, scratchPath)
}

func TestHandleFinalizePlan_OK(t *testing.T) {
	projects := new(mockProjectStore)
	projects.On("Get", "demo-project").Return(project.Project{ID: "demo-project"}, nil)

	plan := task.Plan{Approach: "incremental", EstimatedComplexity: "low"}
	updated := task.Task{ID: "TASK-0001", Stage: task.StageImplementation}

	tasks := new(mockTaskStore)
	tasks.On("FinalizePlan", "demo-project", "TASK-0001", plan).Return(updated, nil)
	tasks.On("GetPlan", "demo-project", "TASK-0001").Return(plan, nil)

	req := newProjectRequest(t, http.MethodPost, "/api/v1/projects/demo-project/tasks/TASK-0001/plan/finalize", plan)
	req.SetPathValue("projectId", "demo-project")
	req.SetPathValue("taskId", "TASK-0001")
	w := httptest.NewRecorder()
	(&Server{Projects: projects, Tasks: tasks}).handleFinalizePlan()(w, req)

	require.Equal(t, http.StatusOK, w.Code)
	var got finalizePlanResponse
	require.NoError(t, json.Unmarshal(w.Body.Bytes(), &got))
	assert.Equal(t, task.StageImplementation, got.Task.Stage)
	assert.Equal(t, "incremental", got.Plan.Approach)
}

func TestHandleFinalizePlan_WrongStage(t *testing.T) {
	projects := new(mockProjectStore)
	projects.On("Get", "demo-project").Return(project.Project{ID: "demo-project"}, nil)

	plan := task.Plan{}
	tasks := new(mockTaskStore)
	tasks.On("FinalizePlan", "demo-project", "TASK-0001", plan).Return(nil, task.ErrWrongStage)

	req := newProjectRequest(t, http.MethodPost, "/api/v1/projects/demo-project/tasks/TASK-0001/plan/finalize", plan)
	req.SetPathValue("projectId", "demo-project")
	req.SetPathValue("taskId", "TASK-0001")
	w := httptest.NewRecorder()
	(&Server{Projects: projects, Tasks: tasks}).handleFinalizePlan()(w, req)

	assert.Equal(t, http.StatusConflict, w.Code)
}

func TestHandleFinalizeRequirements_ClosesAgentSessionsOnSuccess(t *testing.T) {
	projects := new(mockProjectStore)
	projects.On("Get", "demo-project").Return(project.Project{ID: "demo-project"}, nil)

	draft := task.RequirementsDraft{}
	updated := task.Task{ID: "TASK-0001", Stage: task.StagePlanning}
	tasks := new(mockTaskStore)
	tasks.On("FinalizeRequirements", "demo-project", "TASK-0001", draft).Return(updated, nil)
	tasks.On("GetContext", "demo-project", "TASK-0001").Return(task.Context{}, nil)

	runner := new(mockAgentRunner)
	runner.On("CloseSession", "TASK-0001:"+task.StageRequirements)

	req := newProjectRequest(t, http.MethodPost, "/api/v1/projects/demo-project/tasks/TASK-0001/requirements/finalize", draft)
	req.SetPathValue("projectId", "demo-project")
	req.SetPathValue("taskId", "TASK-0001")
	w := httptest.NewRecorder()
	(&Server{Projects: projects, Tasks: tasks, AgentRunners: map[string]agentrunner.AgentRunner{"claude-code": runner}}).handleFinalizeRequirements()(w, req)

	require.Equal(t, http.StatusOK, w.Code)
	runner.AssertCalled(t, "CloseSession", "TASK-0001:"+task.StageRequirements)
}

func TestHandleFinalizeRequirements_DoesNotCloseSessionsWhenFinalizeFails(t *testing.T) {
	projects := new(mockProjectStore)
	projects.On("Get", "demo-project").Return(project.Project{ID: "demo-project"}, nil)

	draft := task.RequirementsDraft{}
	tasks := new(mockTaskStore)
	tasks.On("FinalizeRequirements", "demo-project", "TASK-0001", draft).Return(nil, task.ErrWrongStage)

	runner := new(mockAgentRunner)

	req := newProjectRequest(t, http.MethodPost, "/api/v1/projects/demo-project/tasks/TASK-0001/requirements/finalize", draft)
	req.SetPathValue("projectId", "demo-project")
	req.SetPathValue("taskId", "TASK-0001")
	w := httptest.NewRecorder()
	(&Server{Projects: projects, Tasks: tasks, AgentRunners: map[string]agentrunner.AgentRunner{"claude-code": runner}}).handleFinalizeRequirements()(w, req)

	assert.Equal(t, http.StatusConflict, w.Code)
	runner.AssertNotCalled(t, "CloseSession", mock.Anything)
}

func TestHandleFinalizePlan_ClosesAgentSessionsOnSuccess(t *testing.T) {
	projects := new(mockProjectStore)
	projects.On("Get", "demo-project").Return(project.Project{ID: "demo-project"}, nil)

	plan := task.Plan{}
	updated := task.Task{ID: "TASK-0001", Stage: task.StageImplementation}
	tasks := new(mockTaskStore)
	tasks.On("FinalizePlan", "demo-project", "TASK-0001", plan).Return(updated, nil)
	tasks.On("GetPlan", "demo-project", "TASK-0001").Return(task.Plan{}, nil)

	runner := new(mockAgentRunner)
	runner.On("CloseSession", "TASK-0001:"+task.StagePlanning)

	req := newProjectRequest(t, http.MethodPost, "/api/v1/projects/demo-project/tasks/TASK-0001/plan/finalize", plan)
	req.SetPathValue("projectId", "demo-project")
	req.SetPathValue("taskId", "TASK-0001")
	w := httptest.NewRecorder()
	(&Server{Projects: projects, Tasks: tasks, AgentRunners: map[string]agentrunner.AgentRunner{"claude-code": runner}}).handleFinalizePlan()(w, req)

	require.Equal(t, http.StatusOK, w.Code)
	runner.AssertCalled(t, "CloseSession", "TASK-0001:"+task.StagePlanning)
}

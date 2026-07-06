package api

import (
	"bytes"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/timmersuk/llm-workbench/internal/project"
	"github.com/timmersuk/llm-workbench/internal/task"
)

func TestHandleFinalizeRequirements_OK(t *testing.T) {
	projects := new(mockProjectStore)
	projects.On("Get", "demo-project").Return(project.Project{ID: "demo-project"}, nil)
	projects.On("TasksRoot", "demo-project").Return("/data/projects/demo-project/tasks", nil)

	draft := task.RequirementsDraft{Objective: "ship login", Context: task.Context{Summary: "adds login"}}
	updated := task.Task{ID: "TASK-0001", Stage: task.StagePlanning, Objective: "ship login"}

	tasks := new(mockTaskStore)
	tasks.On("FinalizeRequirements", "TASK-0001", draft).Return(updated, nil)
	tasks.On("GetContext", "TASK-0001").Return(draft.Context, nil)

	req := newProjectRequest(t, http.MethodPost, "/api/v1/projects/demo-project/tasks/TASK-0001/requirements/finalize", draft)
	req.SetPathValue("projectId", "demo-project")
	req.SetPathValue("taskId", "TASK-0001")
	w := httptest.NewRecorder()
	handleFinalizeRequirements(projects, fixedTaskStoreFactory(tasks))(w, req)

	require.Equal(t, http.StatusOK, w.Code)
	var got finalizeRequirementsResponse
	require.NoError(t, json.Unmarshal(w.Body.Bytes(), &got))
	assert.Equal(t, task.StagePlanning, got.Task.Stage)
	assert.Equal(t, "adds login", got.Context.Summary)
}

func TestHandleFinalizeRequirements_WrongStage(t *testing.T) {
	projects := new(mockProjectStore)
	projects.On("Get", "demo-project").Return(project.Project{ID: "demo-project"}, nil)
	projects.On("TasksRoot", "demo-project").Return("/data/projects/demo-project/tasks", nil)

	draft := task.RequirementsDraft{}
	tasks := new(mockTaskStore)
	tasks.On("FinalizeRequirements", "TASK-0001", draft).Return(nil, task.ErrWrongStage)

	req := newProjectRequest(t, http.MethodPost, "/api/v1/projects/demo-project/tasks/TASK-0001/requirements/finalize", draft)
	req.SetPathValue("projectId", "demo-project")
	req.SetPathValue("taskId", "TASK-0001")
	w := httptest.NewRecorder()
	handleFinalizeRequirements(projects, fixedTaskStoreFactory(tasks))(w, req)

	assert.Equal(t, http.StatusConflict, w.Code)
}

func TestHandleFinalizeRequirements_InvalidBody(t *testing.T) {
	projects := new(mockProjectStore)
	projects.On("Get", "demo-project").Return(project.Project{ID: "demo-project"}, nil)
	projects.On("TasksRoot", "demo-project").Return("/data/projects/demo-project/tasks", nil)

	tasks := new(mockTaskStore)

	req := httptest.NewRequest(http.MethodPost, "/api/v1/projects/demo-project/tasks/TASK-0001/requirements/finalize", bytes.NewReader([]byte("not json")))
	req.SetPathValue("projectId", "demo-project")
	req.SetPathValue("taskId", "TASK-0001")
	w := httptest.NewRecorder()
	handleFinalizeRequirements(projects, fixedTaskStoreFactory(tasks))(w, req)

	assert.Equal(t, http.StatusBadRequest, w.Code)
}

func TestHandleFinalizePlan_OK(t *testing.T) {
	projects := new(mockProjectStore)
	projects.On("Get", "demo-project").Return(project.Project{ID: "demo-project"}, nil)
	projects.On("TasksRoot", "demo-project").Return("/data/projects/demo-project/tasks", nil)

	plan := task.Plan{Approach: "incremental", EstimatedComplexity: "low"}
	updated := task.Task{ID: "TASK-0001", Stage: task.StageImplementation}

	tasks := new(mockTaskStore)
	tasks.On("FinalizePlan", "TASK-0001", plan).Return(updated, nil)
	tasks.On("GetPlan", "TASK-0001").Return(plan, nil)

	req := newProjectRequest(t, http.MethodPost, "/api/v1/projects/demo-project/tasks/TASK-0001/plan/finalize", plan)
	req.SetPathValue("projectId", "demo-project")
	req.SetPathValue("taskId", "TASK-0001")
	w := httptest.NewRecorder()
	handleFinalizePlan(projects, fixedTaskStoreFactory(tasks))(w, req)

	require.Equal(t, http.StatusOK, w.Code)
	var got finalizePlanResponse
	require.NoError(t, json.Unmarshal(w.Body.Bytes(), &got))
	assert.Equal(t, task.StageImplementation, got.Task.Stage)
	assert.Equal(t, "incremental", got.Plan.Approach)
}

func TestHandleFinalizePlan_WrongStage(t *testing.T) {
	projects := new(mockProjectStore)
	projects.On("Get", "demo-project").Return(project.Project{ID: "demo-project"}, nil)
	projects.On("TasksRoot", "demo-project").Return("/data/projects/demo-project/tasks", nil)

	plan := task.Plan{}
	tasks := new(mockTaskStore)
	tasks.On("FinalizePlan", "TASK-0001", plan).Return(nil, task.ErrWrongStage)

	req := newProjectRequest(t, http.MethodPost, "/api/v1/projects/demo-project/tasks/TASK-0001/plan/finalize", plan)
	req.SetPathValue("projectId", "demo-project")
	req.SetPathValue("taskId", "TASK-0001")
	w := httptest.NewRecorder()
	handleFinalizePlan(projects, fixedTaskStoreFactory(tasks))(w, req)

	assert.Equal(t, http.StatusConflict, w.Code)
}

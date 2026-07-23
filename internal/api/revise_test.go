package api

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/timmersuk/llm-workbench/internal/project"
	"github.com/timmersuk/llm-workbench/internal/task"
)

func TestHandleReviseRequirements_OK(t *testing.T) {
	projects := new(mockProjectStore)
	projects.On("Get", "demo-project").Return(project.Project{ID: "demo-project"}, nil)
	projects.On("TasksRoot", "demo-project").Return("/data/projects/demo-project/tasks", nil)

	tasks := new(mockTaskStore)
	tasks.On("ReviseToRequirements", "TASK-0001").Return(task.Task{ID: "TASK-0001", Stage: task.StageRequirements}, nil)

	req := httptest.NewRequest(http.MethodPost, "/api/v1/projects/demo-project/tasks/TASK-0001/requirements/revise", nil)
	req.SetPathValue("projectId", "demo-project")
	req.SetPathValue("taskId", "TASK-0001")
	w := httptest.NewRecorder()
	(&Server{Projects: projects, TaskStores: fixedTaskStoreFactory(tasks)}).handleReviseRequirements()(w, req)

	require.Equal(t, http.StatusOK, w.Code)
	var got task.Task
	require.NoError(t, json.Unmarshal(w.Body.Bytes(), &got))
	assert.Equal(t, task.StageRequirements, got.Stage)
}

func TestHandleReviseRequirements_WrongStage(t *testing.T) {
	projects := new(mockProjectStore)
	projects.On("Get", "demo-project").Return(project.Project{ID: "demo-project"}, nil)
	projects.On("TasksRoot", "demo-project").Return("/data/projects/demo-project/tasks", nil)

	tasks := new(mockTaskStore)
	tasks.On("ReviseToRequirements", "TASK-0001").Return(nil, task.ErrWrongStage)

	req := httptest.NewRequest(http.MethodPost, "/api/v1/projects/demo-project/tasks/TASK-0001/requirements/revise", nil)
	req.SetPathValue("projectId", "demo-project")
	req.SetPathValue("taskId", "TASK-0001")
	w := httptest.NewRecorder()
	(&Server{Projects: projects, TaskStores: fixedTaskStoreFactory(tasks)}).handleReviseRequirements()(w, req)

	assert.Equal(t, http.StatusConflict, w.Code)
}

func TestHandleRevisePlan_OK(t *testing.T) {
	projects := new(mockProjectStore)
	projects.On("Get", "demo-project").Return(project.Project{ID: "demo-project"}, nil)
	projects.On("TasksRoot", "demo-project").Return("/data/projects/demo-project/tasks", nil)

	tasks := new(mockTaskStore)
	tasks.On("ReviseToPlanning", "TASK-0001").Return(task.Task{ID: "TASK-0001", Stage: task.StagePlanning}, nil)

	req := httptest.NewRequest(http.MethodPost, "/api/v1/projects/demo-project/tasks/TASK-0001/plan/revise", nil)
	req.SetPathValue("projectId", "demo-project")
	req.SetPathValue("taskId", "TASK-0001")
	w := httptest.NewRecorder()
	(&Server{Projects: projects, TaskStores: fixedTaskStoreFactory(tasks)}).handleRevisePlan()(w, req)

	require.Equal(t, http.StatusOK, w.Code)
	var got task.Task
	require.NoError(t, json.Unmarshal(w.Body.Bytes(), &got))
	assert.Equal(t, task.StagePlanning, got.Stage)
}

func TestHandleRevisePlan_WrongStage(t *testing.T) {
	projects := new(mockProjectStore)
	projects.On("Get", "demo-project").Return(project.Project{ID: "demo-project"}, nil)
	projects.On("TasksRoot", "demo-project").Return("/data/projects/demo-project/tasks", nil)

	tasks := new(mockTaskStore)
	tasks.On("ReviseToPlanning", "TASK-0001").Return(nil, task.ErrWrongStage)

	req := httptest.NewRequest(http.MethodPost, "/api/v1/projects/demo-project/tasks/TASK-0001/plan/revise", nil)
	req.SetPathValue("projectId", "demo-project")
	req.SetPathValue("taskId", "TASK-0001")
	w := httptest.NewRecorder()
	(&Server{Projects: projects, TaskStores: fixedTaskStoreFactory(tasks)}).handleRevisePlan()(w, req)

	assert.Equal(t, http.StatusConflict, w.Code)
}

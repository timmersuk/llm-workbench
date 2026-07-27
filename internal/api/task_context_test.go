package api

import (
	"encoding/json"
	"io/fs"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/timmersuk/llm-workbench/internal/project"
	"github.com/timmersuk/llm-workbench/internal/task"
)

func TestHandleGetTaskContext_OK(t *testing.T) {
	projects := new(mockProjectStore)
	projects.On("Get", "demo-project").Return(project.Project{ID: "demo-project"}, nil)

	tasks := new(mockTaskStore)
	tasks.On("GetContext", "demo-project", "TASK-0001").Return(task.Context{Summary: "adds login"}, nil)

	req := httptest.NewRequest(http.MethodGet, "/api/v1/projects/demo-project/tasks/TASK-0001/context", nil)
	req.SetPathValue("projectId", "demo-project")
	req.SetPathValue("taskId", "TASK-0001")
	w := httptest.NewRecorder()
	(&Server{Projects: projects, Tasks: tasks}).handleGetTaskContext()(w, req)

	require.Equal(t, http.StatusOK, w.Code)
	var got task.Context
	require.NoError(t, json.Unmarshal(w.Body.Bytes(), &got))
	assert.Equal(t, "adds login", got.Summary)
}

func TestHandleGetTaskContext_NotFinalizedYet(t *testing.T) {
	projects := new(mockProjectStore)
	projects.On("Get", "demo-project").Return(project.Project{ID: "demo-project"}, nil)

	tasks := new(mockTaskStore)
	tasks.On("GetContext", "demo-project", "TASK-0001").Return(nil, fs.ErrNotExist)

	req := httptest.NewRequest(http.MethodGet, "/api/v1/projects/demo-project/tasks/TASK-0001/context", nil)
	req.SetPathValue("projectId", "demo-project")
	req.SetPathValue("taskId", "TASK-0001")
	w := httptest.NewRecorder()
	(&Server{Projects: projects, Tasks: tasks}).handleGetTaskContext()(w, req)

	assert.Equal(t, http.StatusNotFound, w.Code)
}

func TestHandleGetTaskPlan_OK(t *testing.T) {
	projects := new(mockProjectStore)
	projects.On("Get", "demo-project").Return(project.Project{ID: "demo-project"}, nil)

	tasks := new(mockTaskStore)
	tasks.On("GetPlan", "demo-project", "TASK-0001").Return(task.Plan{Approach: "incremental"}, nil)

	req := httptest.NewRequest(http.MethodGet, "/api/v1/projects/demo-project/tasks/TASK-0001/plan", nil)
	req.SetPathValue("projectId", "demo-project")
	req.SetPathValue("taskId", "TASK-0001")
	w := httptest.NewRecorder()
	(&Server{Projects: projects, Tasks: tasks}).handleGetTaskPlan()(w, req)

	require.Equal(t, http.StatusOK, w.Code)
	var got task.Plan
	require.NoError(t, json.Unmarshal(w.Body.Bytes(), &got))
	assert.Equal(t, "incremental", got.Approach)
}

func TestHandleGetTaskPlan_NotFinalizedYet(t *testing.T) {
	projects := new(mockProjectStore)
	projects.On("Get", "demo-project").Return(project.Project{ID: "demo-project"}, nil)

	tasks := new(mockTaskStore)
	tasks.On("GetPlan", "demo-project", "TASK-0001").Return(nil, fs.ErrNotExist)

	req := httptest.NewRequest(http.MethodGet, "/api/v1/projects/demo-project/tasks/TASK-0001/plan", nil)
	req.SetPathValue("projectId", "demo-project")
	req.SetPathValue("taskId", "TASK-0001")
	w := httptest.NewRecorder()
	(&Server{Projects: projects, Tasks: tasks}).handleGetTaskPlan()(w, req)

	assert.Equal(t, http.StatusNotFound, w.Code)
}

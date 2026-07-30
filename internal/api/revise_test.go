package api

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/timmersuk/llm-workbench/internal/project"
	"github.com/timmersuk/llm-workbench/internal/task"
)

func TestHandleReviseRequirements_OK(t *testing.T) {
	projects := new(mockProjectStore)
	projects.On("Get", "demo-project").Return(project.Project{ID: "demo-project"}, nil)

	tasks := new(mockTaskStore)
	tasks.On("ReviseToRequirements", "demo-project", "TASK-0001", "").Return(task.Task{ID: "TASK-0001", Stage: task.StageRequirements}, nil)

	req := httptest.NewRequest(http.MethodPost, "/api/v1/projects/demo-project/tasks/TASK-0001/requirements/revise", http.NoBody)
	req.SetPathValue("projectId", "demo-project")
	req.SetPathValue("taskId", "TASK-0001")
	w := httptest.NewRecorder()
	(&Server{Projects: projects, Tasks: tasks}).handleReviseRequirements()(w, req)

	require.Equal(t, http.StatusOK, w.Code)
	var got task.Task
	require.NoError(t, json.Unmarshal(w.Body.Bytes(), &got))
	assert.Equal(t, task.StageRequirements, got.Stage)
}

// TestHandleReviseRequirements_WithReason locks in that an optional
// {"reason": "..."} body is threaded through to the store — the reason a
// human gives for sending a task back to requirements, surfaced later on
// the resulting StageTransition (TimelinePanel.tsx).
func TestHandleReviseRequirements_WithReason(t *testing.T) {
	projects := new(mockProjectStore)
	projects.On("Get", "demo-project").Return(project.Project{ID: "demo-project"}, nil)

	tasks := new(mockTaskStore)
	tasks.On("ReviseToRequirements", "demo-project", "TASK-0001", "the plan missed X").Return(task.Task{ID: "TASK-0001", Stage: task.StageRequirements}, nil)

	req := httptest.NewRequest(http.MethodPost, "/api/v1/projects/demo-project/tasks/TASK-0001/requirements/revise", strings.NewReader(`{"reason":"the plan missed X"}`))
	req.SetPathValue("projectId", "demo-project")
	req.SetPathValue("taskId", "TASK-0001")
	w := httptest.NewRecorder()
	(&Server{Projects: projects, Tasks: tasks}).handleReviseRequirements()(w, req)

	require.Equal(t, http.StatusOK, w.Code)
}

func TestHandleReviseRequirements_WrongStage(t *testing.T) {
	projects := new(mockProjectStore)
	projects.On("Get", "demo-project").Return(project.Project{ID: "demo-project"}, nil)

	tasks := new(mockTaskStore)
	tasks.On("ReviseToRequirements", "demo-project", "TASK-0001", "").Return(nil, task.ErrWrongStage)

	req := httptest.NewRequest(http.MethodPost, "/api/v1/projects/demo-project/tasks/TASK-0001/requirements/revise", http.NoBody)
	req.SetPathValue("projectId", "demo-project")
	req.SetPathValue("taskId", "TASK-0001")
	w := httptest.NewRecorder()
	(&Server{Projects: projects, Tasks: tasks}).handleReviseRequirements()(w, req)

	assert.Equal(t, http.StatusConflict, w.Code)
}

func TestHandleRevisePlan_OK(t *testing.T) {
	projects := new(mockProjectStore)
	projects.On("Get", "demo-project").Return(project.Project{ID: "demo-project"}, nil)

	tasks := new(mockTaskStore)
	tasks.On("ReviseToPlanning", "demo-project", "TASK-0001", "").Return(task.Task{ID: "TASK-0001", Stage: task.StagePlanning}, nil)

	req := httptest.NewRequest(http.MethodPost, "/api/v1/projects/demo-project/tasks/TASK-0001/plan/revise", http.NoBody)
	req.SetPathValue("projectId", "demo-project")
	req.SetPathValue("taskId", "TASK-0001")
	w := httptest.NewRecorder()
	(&Server{Projects: projects, Tasks: tasks}).handleRevisePlan()(w, req)

	require.Equal(t, http.StatusOK, w.Code)
	var got task.Task
	require.NoError(t, json.Unmarshal(w.Body.Bytes(), &got))
	assert.Equal(t, task.StagePlanning, got.Stage)
}

func TestHandleRevisePlan_WithReason(t *testing.T) {
	projects := new(mockProjectStore)
	projects.On("Get", "demo-project").Return(project.Project{ID: "demo-project"}, nil)

	tasks := new(mockTaskStore)
	tasks.On("ReviseToPlanning", "demo-project", "TASK-0001", "I wanted icons, not words").Return(task.Task{ID: "TASK-0001", Stage: task.StagePlanning}, nil)

	req := httptest.NewRequest(http.MethodPost, "/api/v1/projects/demo-project/tasks/TASK-0001/plan/revise", strings.NewReader(`{"reason":"I wanted icons, not words"}`))
	req.SetPathValue("projectId", "demo-project")
	req.SetPathValue("taskId", "TASK-0001")
	w := httptest.NewRecorder()
	(&Server{Projects: projects, Tasks: tasks}).handleRevisePlan()(w, req)

	require.Equal(t, http.StatusOK, w.Code)
}

func TestHandleRevisePlan_WrongStage(t *testing.T) {
	projects := new(mockProjectStore)
	projects.On("Get", "demo-project").Return(project.Project{ID: "demo-project"}, nil)

	tasks := new(mockTaskStore)
	tasks.On("ReviseToPlanning", "demo-project", "TASK-0001", "").Return(nil, task.ErrWrongStage)

	req := httptest.NewRequest(http.MethodPost, "/api/v1/projects/demo-project/tasks/TASK-0001/plan/revise", http.NoBody)
	req.SetPathValue("projectId", "demo-project")
	req.SetPathValue("taskId", "TASK-0001")
	w := httptest.NewRecorder()
	(&Server{Projects: projects, Tasks: tasks}).handleRevisePlan()(w, req)

	assert.Equal(t, http.StatusConflict, w.Code)
}

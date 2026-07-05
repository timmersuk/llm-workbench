package api

import (
	"encoding/json"
	"errors"
	"io/fs"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/timmersuk/llm-workbench/internal/task"
)

func TestHandleListTasks_OK(t *testing.T) {
	lister := new(mockTaskLister)
	lister.On("List").Return([]task.Task{{ID: "TASK-0001"}}, nil)

	req := httptest.NewRequest(http.MethodGet, "/api/v1/tasks", nil)
	w := httptest.NewRecorder()
	handleListTasks(lister)(w, req)

	require.Equal(t, http.StatusOK, w.Code)
	var got []task.Task
	require.NoError(t, json.Unmarshal(w.Body.Bytes(), &got))
	assert.Equal(t, "TASK-0001", got[0].ID)
	lister.AssertExpectations(t)
}

func TestHandleListTasks_Error(t *testing.T) {
	lister := new(mockTaskLister)
	lister.On("List").Return(nil, errors.New("disk on fire"))

	req := httptest.NewRequest(http.MethodGet, "/api/v1/tasks", nil)
	w := httptest.NewRecorder()
	handleListTasks(lister)(w, req)

	assert.Equal(t, http.StatusInternalServerError, w.Code)
}

func TestHandleGetTask_OK(t *testing.T) {
	lister := new(mockTaskLister)
	lister.On("Get", "TASK-0001").Return(task.Task{ID: "TASK-0001", Title: "Do it"}, nil)

	req := httptest.NewRequest(http.MethodGet, "/api/v1/tasks/TASK-0001", nil)
	req.SetPathValue("id", "TASK-0001")
	w := httptest.NewRecorder()
	handleGetTask(lister)(w, req)

	require.Equal(t, http.StatusOK, w.Code)
	var got task.Task
	require.NoError(t, json.Unmarshal(w.Body.Bytes(), &got))
	assert.Equal(t, "Do it", got.Title)
}

func TestHandleGetTask_NotFound(t *testing.T) {
	lister := new(mockTaskLister)
	lister.On("Get", "TASK-9999").Return(nil, fs.ErrNotExist)

	req := httptest.NewRequest(http.MethodGet, "/api/v1/tasks/TASK-9999", nil)
	req.SetPathValue("id", "TASK-9999")
	w := httptest.NewRecorder()
	handleGetTask(lister)(w, req)

	assert.Equal(t, http.StatusNotFound, w.Code)
}

func TestHandleGetTask_InvalidID(t *testing.T) {
	lister := new(mockTaskLister)
	lister.On("Get", "not-a-task-id").Return(nil, task.ErrInvalidID)

	req := httptest.NewRequest(http.MethodGet, "/api/v1/tasks/not-a-task-id", nil)
	req.SetPathValue("id", "not-a-task-id")
	w := httptest.NewRecorder()
	handleGetTask(lister)(w, req)

	assert.Equal(t, http.StatusBadRequest, w.Code)
}

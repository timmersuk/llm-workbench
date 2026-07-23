package api

import (
	"bytes"
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

func newTaskRequest(t *testing.T, method, path string, body any) *http.Request {
	t.Helper()
	var reader *bytes.Reader
	if body != nil {
		data, err := json.Marshal(body)
		require.NoError(t, err)
		reader = bytes.NewReader(data)
	} else {
		reader = bytes.NewReader(nil)
	}
	return httptest.NewRequest(method, path, reader)
}

func TestHandleListProjectTasks_OK(t *testing.T) {
	projects := new(mockProjectStore)
	projects.On("Get", "demo-project").Return(project.Project{ID: "demo-project"}, nil)
	projects.On("TasksRoot", "demo-project").Return("/data/projects/demo-project/tasks", nil)

	tasks := new(mockTaskStore)
	tasks.On("List").Return(task.ListResult{Tasks: []task.Task{{ID: "TASK-0001"}}}, nil)

	req := newTaskRequest(t, http.MethodGet, "/api/v1/projects/demo-project/tasks", nil)
	req.SetPathValue("projectId", "demo-project")
	w := httptest.NewRecorder()
	(&Server{Projects: projects, TaskStores: fixedTaskStoreFactory(tasks)}).handleListProjectTasks()(w, req)

	require.Equal(t, http.StatusOK, w.Code)
	var got task.ListResult
	require.NoError(t, json.Unmarshal(w.Body.Bytes(), &got))
	require.Len(t, got.Tasks, 1)
	assert.Equal(t, "TASK-0001", got.Tasks[0].ID)
}

func TestHandleListProjectTasks_ProjectNotFound(t *testing.T) {
	projects := new(mockProjectStore)
	projects.On("Get", "nonexistent").Return(nil, fs.ErrNotExist)

	tasks := new(mockTaskStore)

	req := newTaskRequest(t, http.MethodGet, "/api/v1/projects/nonexistent/tasks", nil)
	req.SetPathValue("projectId", "nonexistent")
	w := httptest.NewRecorder()
	// tasks has no "List" expectation registered, so if the handler wrongly
	// reached the task store despite the missing project, the mock would
	// panic on the unexpected call and fail this test.
	(&Server{Projects: projects, TaskStores: fixedTaskStoreFactory(tasks)}).handleListProjectTasks()(w, req)

	assert.Equal(t, http.StatusNotFound, w.Code)
}

func TestHandleGetProjectTask_OK(t *testing.T) {
	projects := new(mockProjectStore)
	projects.On("Get", "demo-project").Return(project.Project{ID: "demo-project"}, nil)
	projects.On("TasksRoot", "demo-project").Return("/data/projects/demo-project/tasks", nil)

	tasks := new(mockTaskStore)
	tasks.On("Get", "TASK-0001").Return(task.Task{ID: "TASK-0001", Title: "Do it"}, nil)

	req := newTaskRequest(t, http.MethodGet, "/api/v1/projects/demo-project/tasks/TASK-0001", nil)
	req.SetPathValue("projectId", "demo-project")
	req.SetPathValue("taskId", "TASK-0001")
	w := httptest.NewRecorder()
	(&Server{Projects: projects, TaskStores: fixedTaskStoreFactory(tasks)}).handleGetProjectTask()(w, req)

	require.Equal(t, http.StatusOK, w.Code)
	var got task.Task
	require.NoError(t, json.Unmarshal(w.Body.Bytes(), &got))
	assert.Equal(t, "Do it", got.Title)
}

func TestHandleGetProjectTask_NotFound(t *testing.T) {
	projects := new(mockProjectStore)
	projects.On("Get", "demo-project").Return(project.Project{ID: "demo-project"}, nil)
	projects.On("TasksRoot", "demo-project").Return("/data/projects/demo-project/tasks", nil)

	tasks := new(mockTaskStore)
	tasks.On("Get", "TASK-9999").Return(nil, fs.ErrNotExist)

	req := newTaskRequest(t, http.MethodGet, "/api/v1/projects/demo-project/tasks/TASK-9999", nil)
	req.SetPathValue("projectId", "demo-project")
	req.SetPathValue("taskId", "TASK-9999")
	w := httptest.NewRecorder()
	(&Server{Projects: projects, TaskStores: fixedTaskStoreFactory(tasks)}).handleGetProjectTask()(w, req)

	assert.Equal(t, http.StatusNotFound, w.Code)
}

func TestHandleCreateProjectTask_OK(t *testing.T) {
	projects := new(mockProjectStore)
	projects.On("Get", "demo-project").Return(project.Project{ID: "demo-project"}, nil)
	projects.On("TasksRoot", "demo-project").Return("/data/projects/demo-project/tasks", nil)

	tasks := new(mockTaskStore)
	tasks.On("Create", task.Task{ID: "fix-login-bug", Title: "Fix it", Project: "demo-project"}).
		Return(task.Task{ID: "fix-login-bug", Title: "Fix it", Project: "demo-project"}, nil)

	req := newTaskRequest(t, http.MethodPost, "/api/v1/projects/demo-project/tasks", task.Task{ID: "fix-login-bug", Title: "Fix it"})
	req.SetPathValue("projectId", "demo-project")
	w := httptest.NewRecorder()
	(&Server{Projects: projects, TaskStores: fixedTaskStoreFactory(tasks)}).handleCreateProjectTask()(w, req)

	require.Equal(t, http.StatusCreated, w.Code)
	var got task.Task
	require.NoError(t, json.Unmarshal(w.Body.Bytes(), &got))
	assert.Equal(t, "fix-login-bug", got.ID)
	assert.Equal(t, "demo-project", got.Project)
}

func TestHandleCreateProjectTask_ProjectNotFound(t *testing.T) {
	projects := new(mockProjectStore)
	projects.On("Get", "nonexistent").Return(nil, fs.ErrNotExist)

	tasks := new(mockTaskStore)

	req := newTaskRequest(t, http.MethodPost, "/api/v1/projects/nonexistent/tasks", task.Task{ID: "a", Title: "A"})
	req.SetPathValue("projectId", "nonexistent")
	w := httptest.NewRecorder()
	// tasks has no "Create" expectation registered, so if the handler
	// wrongly reached the task store despite the missing project, the mock
	// would panic on the unexpected call and fail this test.
	(&Server{Projects: projects, TaskStores: fixedTaskStoreFactory(tasks)}).handleCreateProjectTask()(w, req)

	assert.Equal(t, http.StatusNotFound, w.Code)
}

func TestHandleCreateProjectTask_InvalidBody(t *testing.T) {
	projects := new(mockProjectStore)
	projects.On("Get", "demo-project").Return(project.Project{ID: "demo-project"}, nil)
	projects.On("TasksRoot", "demo-project").Return("/data/projects/demo-project/tasks", nil)

	tasks := new(mockTaskStore)

	req := httptest.NewRequest(http.MethodPost, "/api/v1/projects/demo-project/tasks", bytes.NewReader([]byte("not json")))
	req.SetPathValue("projectId", "demo-project")
	w := httptest.NewRecorder()
	(&Server{Projects: projects, TaskStores: fixedTaskStoreFactory(tasks)}).handleCreateProjectTask()(w, req)

	assert.Equal(t, http.StatusBadRequest, w.Code)
}

func TestHandleCreateProjectTask_AlreadyExists(t *testing.T) {
	projects := new(mockProjectStore)
	projects.On("Get", "demo-project").Return(project.Project{ID: "demo-project"}, nil)
	projects.On("TasksRoot", "demo-project").Return("/data/projects/demo-project/tasks", nil)

	tasks := new(mockTaskStore)
	tasks.On("Create", task.Task{ID: "dup", Title: "Dup", Project: "demo-project"}).
		Return(nil, task.ErrAlreadyExists)

	req := newTaskRequest(t, http.MethodPost, "/api/v1/projects/demo-project/tasks", task.Task{ID: "dup", Title: "Dup"})
	req.SetPathValue("projectId", "demo-project")
	w := httptest.NewRecorder()
	(&Server{Projects: projects, TaskStores: fixedTaskStoreFactory(tasks)}).handleCreateProjectTask()(w, req)

	assert.Equal(t, http.StatusConflict, w.Code)
}

func TestHandleUpdateProjectTask_OK(t *testing.T) {
	projects := new(mockProjectStore)
	projects.On("Get", "demo-project").Return(project.Project{ID: "demo-project"}, nil)
	projects.On("TasksRoot", "demo-project").Return("/data/projects/demo-project/tasks", nil)

	tasks := new(mockTaskStore)
	tasks.On("Update", "TASK-0001", task.Task{ID: "TASK-0001", Title: "Updated", Project: "demo-project"}).
		Return(task.Task{ID: "TASK-0001", Title: "Updated", Project: "demo-project"}, nil)

	req := newTaskRequest(t, http.MethodPut, "/api/v1/projects/demo-project/tasks/TASK-0001", task.Task{ID: "TASK-0001", Title: "Updated"})
	req.SetPathValue("projectId", "demo-project")
	req.SetPathValue("taskId", "TASK-0001")
	w := httptest.NewRecorder()
	(&Server{Projects: projects, TaskStores: fixedTaskStoreFactory(tasks)}).handleUpdateProjectTask()(w, req)

	require.Equal(t, http.StatusOK, w.Code)
	var got task.Task
	require.NoError(t, json.Unmarshal(w.Body.Bytes(), &got))
	assert.Equal(t, "Updated", got.Title)
}

func TestHandleUpdateProjectTask_IDMismatch(t *testing.T) {
	projects := new(mockProjectStore)
	projects.On("Get", "demo-project").Return(project.Project{ID: "demo-project"}, nil)
	projects.On("TasksRoot", "demo-project").Return("/data/projects/demo-project/tasks", nil)

	tasks := new(mockTaskStore)
	tasks.On("Update", "TASK-0001", task.Task{ID: "TASK-9999", Title: "Updated", Project: "demo-project"}).
		Return(nil, task.ErrIDMismatch)

	req := newTaskRequest(t, http.MethodPut, "/api/v1/projects/demo-project/tasks/TASK-0001", task.Task{ID: "TASK-9999", Title: "Updated"})
	req.SetPathValue("projectId", "demo-project")
	req.SetPathValue("taskId", "TASK-0001")
	w := httptest.NewRecorder()
	(&Server{Projects: projects, TaskStores: fixedTaskStoreFactory(tasks)}).handleUpdateProjectTask()(w, req)

	assert.Equal(t, http.StatusBadRequest, w.Code)
}

func TestHandleUpdateProjectTask_NotFound(t *testing.T) {
	projects := new(mockProjectStore)
	projects.On("Get", "demo-project").Return(project.Project{ID: "demo-project"}, nil)
	projects.On("TasksRoot", "demo-project").Return("/data/projects/demo-project/tasks", nil)

	tasks := new(mockTaskStore)
	tasks.On("Update", "TASK-9999", task.Task{ID: "TASK-9999", Title: "Updated", Project: "demo-project"}).
		Return(nil, fs.ErrNotExist)

	req := newTaskRequest(t, http.MethodPut, "/api/v1/projects/demo-project/tasks/TASK-9999", task.Task{ID: "TASK-9999", Title: "Updated"})
	req.SetPathValue("projectId", "demo-project")
	req.SetPathValue("taskId", "TASK-9999")
	w := httptest.NewRecorder()
	(&Server{Projects: projects, TaskStores: fixedTaskStoreFactory(tasks)}).handleUpdateProjectTask()(w, req)

	assert.Equal(t, http.StatusNotFound, w.Code)
}

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

	"github.com/timmersuk/llm-workbench/internal/agentrunner"
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

	tasks := new(mockTaskStore)
	tasks.On("List", "demo-project").Return(task.ListResult{Tasks: []task.Task{{ID: "TASK-0001"}}}, nil)

	req := newTaskRequest(t, http.MethodGet, "/api/v1/projects/demo-project/tasks", nil)
	req.SetPathValue("projectId", "demo-project")
	w := httptest.NewRecorder()
	(&Server{Projects: projects, Tasks: tasks}).handleListProjectTasks()(w, req)

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
	(&Server{Projects: projects, Tasks: tasks}).handleListProjectTasks()(w, req)

	assert.Equal(t, http.StatusNotFound, w.Code)
}

func TestHandleGetProjectTask_OK(t *testing.T) {
	projects := new(mockProjectStore)
	projects.On("Get", "demo-project").Return(project.Project{ID: "demo-project"}, nil)

	tasks := new(mockTaskStore)
	tasks.On("Get", "demo-project", "TASK-0001").Return(task.Task{ID: "TASK-0001", Title: "Do it"}, nil)

	req := newTaskRequest(t, http.MethodGet, "/api/v1/projects/demo-project/tasks/TASK-0001", nil)
	req.SetPathValue("projectId", "demo-project")
	req.SetPathValue("taskId", "TASK-0001")
	w := httptest.NewRecorder()
	(&Server{Projects: projects, Tasks: tasks}).handleGetProjectTask()(w, req)

	require.Equal(t, http.StatusOK, w.Code)
	var got task.Task
	require.NoError(t, json.Unmarshal(w.Body.Bytes(), &got))
	assert.Equal(t, "Do it", got.Title)
}

func TestHandleGetProjectTask_NotFound(t *testing.T) {
	projects := new(mockProjectStore)
	projects.On("Get", "demo-project").Return(project.Project{ID: "demo-project"}, nil)

	tasks := new(mockTaskStore)
	tasks.On("Get", "demo-project", "TASK-9999").Return(nil, fs.ErrNotExist)

	req := newTaskRequest(t, http.MethodGet, "/api/v1/projects/demo-project/tasks/TASK-9999", nil)
	req.SetPathValue("projectId", "demo-project")
	req.SetPathValue("taskId", "TASK-9999")
	w := httptest.NewRecorder()
	(&Server{Projects: projects, Tasks: tasks}).handleGetProjectTask()(w, req)

	assert.Equal(t, http.StatusNotFound, w.Code)
}

func TestHandleCreateProjectTask_OK(t *testing.T) {
	projects := new(mockProjectStore)
	projects.On("Get", "demo-project").Return(project.Project{ID: "demo-project"}, nil)

	tasks := new(mockTaskStore)
	tasks.On("Create", "demo-project", task.Task{ID: "fix-login-bug", Title: "Fix it", Project: "demo-project"}).
		Return(task.Task{ID: "fix-login-bug", Title: "Fix it", Project: "demo-project"}, nil)

	req := newTaskRequest(t, http.MethodPost, "/api/v1/projects/demo-project/tasks", task.Task{ID: "fix-login-bug", Title: "Fix it"})
	req.SetPathValue("projectId", "demo-project")
	w := httptest.NewRecorder()
	(&Server{Projects: projects, Tasks: tasks}).handleCreateProjectTask()(w, req)

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
	(&Server{Projects: projects, Tasks: tasks}).handleCreateProjectTask()(w, req)

	assert.Equal(t, http.StatusNotFound, w.Code)
}

func TestHandleCreateProjectTask_InvalidBody(t *testing.T) {
	projects := new(mockProjectStore)
	projects.On("Get", "demo-project").Return(project.Project{ID: "demo-project"}, nil)

	tasks := new(mockTaskStore)

	req := httptest.NewRequest(http.MethodPost, "/api/v1/projects/demo-project/tasks", bytes.NewReader([]byte("not json")))
	req.SetPathValue("projectId", "demo-project")
	w := httptest.NewRecorder()
	(&Server{Projects: projects, Tasks: tasks}).handleCreateProjectTask()(w, req)

	assert.Equal(t, http.StatusBadRequest, w.Code)
}

func TestHandleCreateProjectTask_AlreadyExists(t *testing.T) {
	projects := new(mockProjectStore)
	projects.On("Get", "demo-project").Return(project.Project{ID: "demo-project"}, nil)

	tasks := new(mockTaskStore)
	tasks.On("Create", "demo-project", task.Task{ID: "dup", Title: "Dup", Project: "demo-project"}).
		Return(nil, task.ErrAlreadyExists)

	req := newTaskRequest(t, http.MethodPost, "/api/v1/projects/demo-project/tasks", task.Task{ID: "dup", Title: "Dup"})
	req.SetPathValue("projectId", "demo-project")
	w := httptest.NewRecorder()
	(&Server{Projects: projects, Tasks: tasks}).handleCreateProjectTask()(w, req)

	assert.Equal(t, http.StatusConflict, w.Code)
}

func TestHandleUpdateProjectTask_OK(t *testing.T) {
	projects := new(mockProjectStore)
	projects.On("Get", "demo-project").Return(project.Project{ID: "demo-project"}, nil)

	tasks := new(mockTaskStore)
	tasks.On("Update", "demo-project", "TASK-0001", task.Task{ID: "TASK-0001", Title: "Updated", Project: "demo-project"}).
		Return(task.Task{ID: "TASK-0001", Title: "Updated", Project: "demo-project"}, nil)

	req := newTaskRequest(t, http.MethodPut, "/api/v1/projects/demo-project/tasks/TASK-0001", task.Task{ID: "TASK-0001", Title: "Updated"})
	req.SetPathValue("projectId", "demo-project")
	req.SetPathValue("taskId", "TASK-0001")
	w := httptest.NewRecorder()
	(&Server{Projects: projects, Tasks: tasks}).handleUpdateProjectTask()(w, req)

	require.Equal(t, http.StatusOK, w.Code)
	var got task.Task
	require.NoError(t, json.Unmarshal(w.Body.Bytes(), &got))
	assert.Equal(t, "Updated", got.Title)
}

func TestHandleUpdateProjectTask_IDMismatch(t *testing.T) {
	projects := new(mockProjectStore)
	projects.On("Get", "demo-project").Return(project.Project{ID: "demo-project"}, nil)

	tasks := new(mockTaskStore)
	tasks.On("Update", "demo-project", "TASK-0001", task.Task{ID: "TASK-9999", Title: "Updated", Project: "demo-project"}).
		Return(nil, task.ErrIDMismatch)

	req := newTaskRequest(t, http.MethodPut, "/api/v1/projects/demo-project/tasks/TASK-0001", task.Task{ID: "TASK-9999", Title: "Updated"})
	req.SetPathValue("projectId", "demo-project")
	req.SetPathValue("taskId", "TASK-0001")
	w := httptest.NewRecorder()
	(&Server{Projects: projects, Tasks: tasks}).handleUpdateProjectTask()(w, req)

	assert.Equal(t, http.StatusBadRequest, w.Code)
}

func TestHandleUpdateProjectTask_StageChangeRejected(t *testing.T) {
	projects := new(mockProjectStore)
	projects.On("Get", "demo-project").Return(project.Project{ID: "demo-project"}, nil)

	tasks := new(mockTaskStore)
	tasks.On("Update", "demo-project", "TASK-0001", task.Task{ID: "TASK-0001", Title: "Updated", Project: "demo-project", Stage: task.StageMerged}).
		Return(nil, task.ErrStageImmutable)

	req := newTaskRequest(t, http.MethodPut, "/api/v1/projects/demo-project/tasks/TASK-0001", task.Task{ID: "TASK-0001", Title: "Updated", Stage: task.StageMerged})
	req.SetPathValue("projectId", "demo-project")
	req.SetPathValue("taskId", "TASK-0001")
	w := httptest.NewRecorder()
	(&Server{Projects: projects, Tasks: tasks}).handleUpdateProjectTask()(w, req)

	assert.Equal(t, http.StatusBadRequest, w.Code)
}

func TestHandleUpdateProjectTask_NotFound(t *testing.T) {
	projects := new(mockProjectStore)
	projects.On("Get", "demo-project").Return(project.Project{ID: "demo-project"}, nil)

	tasks := new(mockTaskStore)
	tasks.On("Update", "demo-project", "TASK-9999", task.Task{ID: "TASK-9999", Title: "Updated", Project: "demo-project"}).
		Return(nil, fs.ErrNotExist)

	req := newTaskRequest(t, http.MethodPut, "/api/v1/projects/demo-project/tasks/TASK-9999", task.Task{ID: "TASK-9999", Title: "Updated"})
	req.SetPathValue("projectId", "demo-project")
	req.SetPathValue("taskId", "TASK-9999")
	w := httptest.NewRecorder()
	(&Server{Projects: projects, Tasks: tasks}).handleUpdateProjectTask()(w, req)

	assert.Equal(t, http.StatusNotFound, w.Code)
}

// TestHandleCreateProjectTask_SeedsAgentDefaultsFromConfiguredSeeds covers
// the "create-time initialization" success criterion at the HTTP-handler
// level (resolveSelection/newTaskAgentDefaults themselves are covered by
// selection_test.go, a different code path from the handler that's
// supposed to call them): a request that omits agent_defaults entirely
// still gets a complete, valid pair persisted, resolved from the server's
// configured stage/execution seed executors (here left at their
// zero-value defaults — "local"/"claude-code", the same fallback
// newTaskAgentDefaults applies) — never silently created with no
// agent_defaults at all.
func TestHandleCreateProjectTask_SeedsAgentDefaultsFromConfiguredSeeds(t *testing.T) {
	projects := new(mockProjectStore)
	projects.On("Get", "demo-project").Return(project.Project{ID: "demo-project"}, nil)

	local := new(mockAgentRunner)
	claudeCode := new(mockAgentRunner)
	expectedDefaults := task.AgentDefaults{
		StageConversation: task.AgentSelection{Executor: "local", Effort: "medium"},
		Execution:         task.AgentSelection{Executor: "claude-code", Effort: "medium"},
	}

	tasks := new(mockTaskStore)
	tasks.On("Create", "demo-project", task.Task{ID: "new-task", Title: "New task", Project: "demo-project", AgentDefaults: &expectedDefaults}).
		Return(task.Task{ID: "new-task", Title: "New task", Project: "demo-project", AgentDefaults: &expectedDefaults}, nil)

	req := newTaskRequest(t, http.MethodPost, "/api/v1/projects/demo-project/tasks", task.Task{ID: "new-task", Title: "New task"})
	req.SetPathValue("projectId", "demo-project")
	w := httptest.NewRecorder()
	server := &Server{Projects: projects, Tasks: tasks, AgentRunners: map[string]agentrunner.AgentRunner{"local": local, "claude-code": claudeCode}}
	server.handleCreateProjectTask()(w, req)

	require.Equal(t, http.StatusCreated, w.Code)
	var got task.Task
	require.NoError(t, json.Unmarshal(w.Body.Bytes(), &got))
	require.NotNil(t, got.AgentDefaults)
	assert.Equal(t, expectedDefaults, *got.AgentDefaults)
}

// TestHandleCreateProjectTask_SeedFailureReturns500 asserts that when a
// configured seed executor can't supply a valid default triple (here: the
// execution seed, "claude-code", simply isn't registered), task creation
// fails clearly with an internal error rather than silently persisting an
// incomplete/invalid agent_defaults or falling back to some other executor.
// tasks has no "Create" expectation registered, so if the handler wrongly
// reached the task store despite the seed failure, the mock would panic on
// the unexpected call and fail this test.
func TestHandleCreateProjectTask_SeedFailureReturns500(t *testing.T) {
	projects := new(mockProjectStore)
	projects.On("Get", "demo-project").Return(project.Project{ID: "demo-project"}, nil)

	local := new(mockAgentRunner)
	tasks := new(mockTaskStore)

	req := newTaskRequest(t, http.MethodPost, "/api/v1/projects/demo-project/tasks", task.Task{ID: "new-task", Title: "New task"})
	req.SetPathValue("projectId", "demo-project")
	w := httptest.NewRecorder()
	server := &Server{Projects: projects, Tasks: tasks, AgentRunners: map[string]agentrunner.AgentRunner{"local": local}}
	server.handleCreateProjectTask()(w, req)

	assert.Equal(t, http.StatusInternalServerError, w.Code)
}

// TestHandleUpdateProjectTask_PreservesStaleAgentDefaultsForUnrelatedEdit
// covers the "stale defaults are preserved, not silently rewritten or
// rejected" success criterion at the HTTP-handler level: the persisted
// agent_defaults reference an executor no longer registered server-side,
// but a request that only edits an unrelated field (Title) and omits
// agent_defaults entirely must still succeed, carrying the exact same
// stale triple forward untouched — never re-validated (which would 400 an
// otherwise-legitimate edit) and never quietly swapped for something else.
func TestHandleUpdateProjectTask_PreservesStaleAgentDefaultsForUnrelatedEdit(t *testing.T) {
	projects := new(mockProjectStore)
	projects.On("Get", "demo-project").Return(project.Project{ID: "demo-project"}, nil)

	staleDefaults := &task.AgentDefaults{
		StageConversation: task.AgentSelection{Executor: "removed-executor", Effort: "high"},
		Execution:         task.AgentSelection{Executor: "removed-executor", Effort: "high"},
	}
	current := task.Task{ID: "TASK-0001", Title: "Original", Project: "demo-project", AgentDefaults: staleDefaults}
	updated := task.Task{ID: "TASK-0001", Title: "Updated", Project: "demo-project", AgentDefaults: staleDefaults}

	tasks := new(mockTaskStore)
	tasks.On("Get", "demo-project", "TASK-0001").Return(current, nil)
	tasks.On("Update", "demo-project", "TASK-0001", updated).Return(updated, nil)

	req := newTaskRequest(t, http.MethodPut, "/api/v1/projects/demo-project/tasks/TASK-0001", task.Task{ID: "TASK-0001", Title: "Updated"})
	req.SetPathValue("projectId", "demo-project")
	req.SetPathValue("taskId", "TASK-0001")
	w := httptest.NewRecorder()
	// No AgentRunners at all — "removed-executor" couldn't be validated even
	// if the handler tried to, proving validation is genuinely skipped for
	// this unrelated edit rather than happening to pass.
	server := &Server{Projects: projects, Tasks: tasks}
	server.handleUpdateProjectTask()(w, req)

	require.Equal(t, http.StatusOK, w.Code)
	var got task.Task
	require.NoError(t, json.Unmarshal(w.Body.Bytes(), &got))
	assert.Equal(t, "Updated", got.Title)
	require.NotNil(t, got.AgentDefaults)
	assert.Equal(t, *staleDefaults, *got.AgentDefaults)
}

// TestHandleUpdateProjectTask_RejectsInvalidAgentDefaultsChange covers HTTP
// 400 mapping for a submitted agent_defaults change that doesn't validate
// against the selected executor's advertised capabilities — the Defaults
// UI's one explicit Save going through this same whole-task PUT. tasks has
// no "Update" expectation registered, so if the handler wrongly proceeded
// to persist despite the invalid combination, the mock would panic on the
// unexpected call and fail this test.
func TestHandleUpdateProjectTask_RejectsInvalidAgentDefaultsChange(t *testing.T) {
	projects := new(mockProjectStore)
	projects.On("Get", "demo-project").Return(project.Project{ID: "demo-project"}, nil)

	current := task.Task{
		ID: "TASK-0001", Title: "Original", Project: "demo-project",
		AgentDefaults: &task.AgentDefaults{
			StageConversation: task.AgentSelection{Executor: "local", Effort: "medium"},
			Execution:         task.AgentSelection{Executor: "claude-code", Effort: "medium"},
		},
	}

	tasks := new(mockTaskStore)
	tasks.On("Get", "demo-project", "TASK-0001").Return(current, nil)

	submitted := task.Task{
		ID: "TASK-0001", Title: "Original",
		AgentDefaults: &task.AgentDefaults{
			// "unknown-executor" is not registered below — an invalid
			// combination the submitted change actually alters, so it must
			// be re-validated (unlike the unrelated-edit case above) and
			// rejected before ever reaching the store.
			StageConversation: task.AgentSelection{Executor: "unknown-executor", Effort: "medium"},
			Execution:         task.AgentSelection{Executor: "claude-code", Effort: "medium"},
		},
	}
	req := newTaskRequest(t, http.MethodPut, "/api/v1/projects/demo-project/tasks/TASK-0001", submitted)
	req.SetPathValue("projectId", "demo-project")
	req.SetPathValue("taskId", "TASK-0001")
	w := httptest.NewRecorder()
	local := new(mockAgentRunner)
	claudeCode := new(mockAgentRunner)
	server := &Server{Projects: projects, Tasks: tasks, AgentRunners: map[string]agentrunner.AgentRunner{"local": local, "claude-code": claudeCode}}
	server.handleUpdateProjectTask()(w, req)

	assert.Equal(t, http.StatusBadRequest, w.Code)
}

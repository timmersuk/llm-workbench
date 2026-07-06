package api

import (
	"errors"
	"io/fs"
	"net/http"
	"net/http/httptest"
	"testing"
	"testing/fstest"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/mock"
	"github.com/stretchr/testify/require"

	"github.com/timmersuk/llm-workbench/internal/project"
	"github.com/timmersuk/llm-workbench/internal/task"
)

func testFrontendFS() fstest.MapFS {
	return fstest.MapFS{
		"index.html":    {Data: []byte("<html>dashboard</html>")},
		"assets/app.js": {Data: []byte("console.log('hi')")},
	}
}

func TestRouter_Healthcheck(t *testing.T) {
	chatCompleter := new(mockChatCompleter)
	chatCompleter.On("CheckHealth", mock.Anything).Return(nil)

	router := NewRouter(new(mockProjectStore), fixedTaskStoreFactory(new(mockTaskStore)), chatCompleter, testFrontendFS(), "test-build")

	req := httptest.NewRequest(http.MethodGet, "/healthcheck", nil)
	w := httptest.NewRecorder()
	router.ServeHTTP(w, req)

	assert.Equal(t, http.StatusOK, w.Code)
	assert.Contains(t, w.Body.String(), "\"status\":\"ok\"")
	assert.Contains(t, w.Body.String(), "\"build_id\":\"test-build\"")
}

func TestRouter_Healthcheck_WhenLLMProbeFails(t *testing.T) {
	chatCompleter := new(mockChatCompleter)
	chatCompleter.On("CheckHealth", mock.Anything).Return(errors.New("llm unavailable"))

	router := NewRouter(new(mockProjectStore), fixedTaskStoreFactory(new(mockTaskStore)), chatCompleter, testFrontendFS(), "test-build")

	req := httptest.NewRequest(http.MethodGet, "/healthcheck", nil)
	w := httptest.NewRecorder()
	router.ServeHTTP(w, req)

	assert.Equal(t, http.StatusServiceUnavailable, w.Code)
	assert.Contains(t, w.Body.String(), "llm")
}

func TestRouter_Version(t *testing.T) {
	router := NewRouter(new(mockProjectStore), fixedTaskStoreFactory(new(mockTaskStore)), new(mockChatCompleter), testFrontendFS(), "test-build")

	req := httptest.NewRequest(http.MethodGet, "/api/v1/version", nil)
	w := httptest.NewRecorder()
	router.ServeHTTP(w, req)

	assert.Equal(t, http.StatusOK, w.Code)
	assert.Contains(t, w.Body.String(), "version")
}

func TestRouter_FrontendServesRealFile(t *testing.T) {
	router := NewRouter(new(mockProjectStore), fixedTaskStoreFactory(new(mockTaskStore)), new(mockChatCompleter), testFrontendFS(), "test-build")

	req := httptest.NewRequest(http.MethodGet, "/assets/app.js", nil)
	w := httptest.NewRecorder()
	router.ServeHTTP(w, req)

	require.Equal(t, http.StatusOK, w.Code)
	assert.Equal(t, "console.log('hi')", w.Body.String())
}

func TestRouter_FrontendFallsBackToIndexForUnknownPath(t *testing.T) {
	router := NewRouter(new(mockProjectStore), fixedTaskStoreFactory(new(mockTaskStore)), new(mockChatCompleter), testFrontendFS(), "test-build")

	req := httptest.NewRequest(http.MethodGet, "/tasks/some-client-route", nil)
	w := httptest.NewRecorder()
	router.ServeHTTP(w, req)

	require.Equal(t, http.StatusOK, w.Code)
	assert.Contains(t, w.Body.String(), "dashboard")
}

func TestRouter_ProjectTasksEndToEnd(t *testing.T) {
	projects := new(mockProjectStore)
	projects.On("Get", "demo-project").Return(project.Project{ID: "demo-project"}, nil)
	projects.On("TasksRoot", "demo-project").Return("/data/projects/demo-project/tasks", nil)

	tasks := new(mockTaskStore)
	tasks.On("List").Return(task.ListResult{Tasks: []task.Task{{ID: "TASK-0001"}}}, nil)

	router := NewRouter(projects, fixedTaskStoreFactory(tasks), new(mockChatCompleter), testFrontendFS(), "test-build")

	req := httptest.NewRequest(http.MethodGet, "/api/v1/projects/demo-project/tasks", nil)
	w := httptest.NewRecorder()
	router.ServeHTTP(w, req)

	require.Equal(t, http.StatusOK, w.Code)
	assert.Contains(t, w.Body.String(), "TASK-0001")
}

func TestRouter_ProjectTasks_ProjectNotFoundBeforeTaskStoreTouched(t *testing.T) {
	projects := new(mockProjectStore)
	projects.On("Get", "nonexistent").Return(nil, fs.ErrNotExist)

	// tasks has no expectations registered at all — if the router reached
	// the task store despite the missing project, the mock would panic.
	tasks := new(mockTaskStore)

	router := NewRouter(projects, fixedTaskStoreFactory(tasks), new(mockChatCompleter), testFrontendFS(), "test-build")

	req := httptest.NewRequest(http.MethodGet, "/api/v1/projects/nonexistent/tasks", nil)
	w := httptest.NewRecorder()
	router.ServeHTTP(w, req)

	assert.Equal(t, http.StatusNotFound, w.Code)
}

func TestRouter_CreateAndUpdateProjectEndToEnd(t *testing.T) {
	projects := new(mockProjectStore)
	projects.On("Create", project.CreateInput{Name: "Demo"}).Return(project.Project{ID: "demo", Name: "Demo"}, nil)
	projects.On("Update", "demo", project.UpdateInput{Name: "Demo Updated"}).Return(project.Project{ID: "demo", Name: "Demo Updated"}, nil)

	router := NewRouter(projects, fixedTaskStoreFactory(new(mockTaskStore)), new(mockChatCompleter), testFrontendFS(), "test-build")

	createReq := newProjectRequest(t, http.MethodPost, "/api/v1/projects", project.CreateInput{Name: "Demo"})
	createW := httptest.NewRecorder()
	router.ServeHTTP(createW, createReq)
	require.Equal(t, http.StatusCreated, createW.Code)

	updateReq := newProjectRequest(t, http.MethodPut, "/api/v1/projects/demo", project.UpdateInput{Name: "Demo Updated"})
	updateW := httptest.NewRecorder()
	router.ServeHTTP(updateW, updateReq)
	require.Equal(t, http.StatusOK, updateW.Code)
}

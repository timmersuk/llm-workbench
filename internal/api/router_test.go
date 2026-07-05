package api

import (
	"net/http"
	"net/http/httptest"
	"testing"
	"testing/fstest"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/timmersuk/llm-workbench/internal/task"
)

func testFrontendFS() fstest.MapFS {
	return fstest.MapFS{
		"index.html": {Data: []byte("<html>dashboard</html>")},
		"assets/app.js": {Data: []byte("console.log('hi')")},
	}
}

func TestRouter_Healthcheck(t *testing.T) {
	router := NewRouter(new(mockTaskLister), new(mockProjectLister), new(mockChatCompleter), testFrontendFS())

	req := httptest.NewRequest(http.MethodGet, "/healthcheck", nil)
	w := httptest.NewRecorder()
	router.ServeHTTP(w, req)

	assert.Equal(t, http.StatusOK, w.Code)
	assert.Equal(t, "ok", w.Body.String())
}

func TestRouter_Version(t *testing.T) {
	router := NewRouter(new(mockTaskLister), new(mockProjectLister), new(mockChatCompleter), testFrontendFS())

	req := httptest.NewRequest(http.MethodGet, "/api/v1/version", nil)
	w := httptest.NewRecorder()
	router.ServeHTTP(w, req)

	assert.Equal(t, http.StatusOK, w.Code)
	assert.Contains(t, w.Body.String(), "version")
}

func TestRouter_FrontendServesRealFile(t *testing.T) {
	router := NewRouter(new(mockTaskLister), new(mockProjectLister), new(mockChatCompleter), testFrontendFS())

	req := httptest.NewRequest(http.MethodGet, "/assets/app.js", nil)
	w := httptest.NewRecorder()
	router.ServeHTTP(w, req)

	require.Equal(t, http.StatusOK, w.Code)
	assert.Equal(t, "console.log('hi')", w.Body.String())
}

func TestRouter_FrontendFallsBackToIndexForUnknownPath(t *testing.T) {
	router := NewRouter(new(mockTaskLister), new(mockProjectLister), new(mockChatCompleter), testFrontendFS())

	req := httptest.NewRequest(http.MethodGet, "/tasks/some-client-route", nil)
	w := httptest.NewRecorder()
	router.ServeHTTP(w, req)

	require.Equal(t, http.StatusOK, w.Code)
	assert.Contains(t, w.Body.String(), "dashboard")
}

func TestRouter_TasksEndToEnd(t *testing.T) {
	tasks := new(mockTaskLister)
	tasks.On("List").Return([]task.Task{{ID: "TASK-0001"}}, nil)

	router := NewRouter(tasks, new(mockProjectLister), new(mockChatCompleter), testFrontendFS())

	req := httptest.NewRequest(http.MethodGet, "/api/v1/tasks", nil)
	w := httptest.NewRecorder()
	router.ServeHTTP(w, req)

	require.Equal(t, http.StatusOK, w.Code)
	assert.Contains(t, w.Body.String(), "TASK-0001")
}

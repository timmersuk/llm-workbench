package api

import (
	"errors"
	"net/http"
	"net/http/httptest"
	"testing"
	"testing/fstest"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/mock"
	"github.com/stretchr/testify/require"

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

	router := NewRouter(new(mockTaskLister), new(mockProjectLister), chatCompleter, testFrontendFS(), "test-build")

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

	router := NewRouter(new(mockTaskLister), new(mockProjectLister), chatCompleter, testFrontendFS(), "test-build")

	req := httptest.NewRequest(http.MethodGet, "/healthcheck", nil)
	w := httptest.NewRecorder()
	router.ServeHTTP(w, req)

	assert.Equal(t, http.StatusServiceUnavailable, w.Code)
	assert.Contains(t, w.Body.String(), "llm")
}

func TestRouter_Version(t *testing.T) {
	router := NewRouter(new(mockTaskLister), new(mockProjectLister), new(mockChatCompleter), testFrontendFS(), "test-build")

	req := httptest.NewRequest(http.MethodGet, "/api/v1/version", nil)
	w := httptest.NewRecorder()
	router.ServeHTTP(w, req)

	assert.Equal(t, http.StatusOK, w.Code)
	assert.Contains(t, w.Body.String(), "version")
}

func TestRouter_FrontendServesRealFile(t *testing.T) {
	router := NewRouter(new(mockTaskLister), new(mockProjectLister), new(mockChatCompleter), testFrontendFS(), "test-build")

	req := httptest.NewRequest(http.MethodGet, "/assets/app.js", nil)
	w := httptest.NewRecorder()
	router.ServeHTTP(w, req)

	require.Equal(t, http.StatusOK, w.Code)
	assert.Equal(t, "console.log('hi')", w.Body.String())
}

func TestRouter_FrontendFallsBackToIndexForUnknownPath(t *testing.T) {
	router := NewRouter(new(mockTaskLister), new(mockProjectLister), new(mockChatCompleter), testFrontendFS(), "test-build")

	req := httptest.NewRequest(http.MethodGet, "/tasks/some-client-route", nil)
	w := httptest.NewRecorder()
	router.ServeHTTP(w, req)

	require.Equal(t, http.StatusOK, w.Code)
	assert.Contains(t, w.Body.String(), "dashboard")
}

func TestRouter_TasksEndToEnd(t *testing.T) {
	tasks := new(mockTaskLister)
	tasks.On("List").Return(task.ListResult{Tasks: []task.Task{{ID: "TASK-0001"}}}, nil)

	router := NewRouter(tasks, new(mockProjectLister), new(mockChatCompleter), testFrontendFS(), "test-build")

	req := httptest.NewRequest(http.MethodGet, "/api/v1/tasks", nil)
	w := httptest.NewRecorder()
	router.ServeHTTP(w, req)

	require.Equal(t, http.StatusOK, w.Code)
	assert.Contains(t, w.Body.String(), "TASK-0001")
}

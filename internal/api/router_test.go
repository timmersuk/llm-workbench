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

	"github.com/timmersuk/llm-workbench/internal/agentrunner"
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
	runner := new(mockAgentRunner)
	runner.On("CheckHealth", mock.Anything).Return(nil)

	router := NewRouter(new(mockProjectStore), fixedTaskStoreFactory(new(mockTaskStore)), new(mockKnowledgeReader),
		map[string]agentrunner.AgentRunner{"local": runner}, "", nil, nil, testFrontendFS(), "test-build")

	req := httptest.NewRequest(http.MethodGet, "/healthcheck", nil)
	w := httptest.NewRecorder()
	router.ServeHTTP(w, req)

	assert.Equal(t, http.StatusOK, w.Code)
	assert.Contains(t, w.Body.String(), "\"status\":\"ok\"")
	assert.Contains(t, w.Body.String(), "\"build_id\":\"test-build\"")
	assert.Contains(t, w.Body.String(), "\"subsystems\":{\"agent:local\":{\"status\":\"ok\"}}")
}

func TestRouter_Healthcheck_WhenLLMProbeFails(t *testing.T) {
	runner := new(mockAgentRunner)
	runner.On("CheckHealth", mock.Anything).Return(errors.New("llm unavailable"))

	router := NewRouter(new(mockProjectStore), fixedTaskStoreFactory(new(mockTaskStore)), new(mockKnowledgeReader),
		map[string]agentrunner.AgentRunner{"local": runner}, "", nil, nil, testFrontendFS(), "test-build")

	req := httptest.NewRequest(http.MethodGet, "/healthcheck", nil)
	w := httptest.NewRecorder()
	router.ServeHTTP(w, req)

	assert.Equal(t, http.StatusServiceUnavailable, w.Code)
	assert.Contains(t, w.Body.String(), "llm")
	assert.Contains(t, w.Body.String(), "\"agent:local\":{\"status\":\"error\",\"error\":\"llm unavailable\"}")
}

func TestRouter_Healthcheck_ReportsPerSubsystemStatusForAgentRunners(t *testing.T) {
	runner := new(mockAgentRunner)
	runner.On("CheckHealth", mock.Anything).Return(nil)

	router := NewRouter(new(mockProjectStore), fixedTaskStoreFactory(new(mockTaskStore)), new(mockKnowledgeReader),
		map[string]agentrunner.AgentRunner{"claude-code": runner}, "", nil, nil, testFrontendFS(), "test-build")

	req := httptest.NewRequest(http.MethodGet, "/healthcheck", nil)
	w := httptest.NewRecorder()
	router.ServeHTTP(w, req)

	assert.Equal(t, http.StatusOK, w.Code)
	assert.Contains(t, w.Body.String(), "\"agent:claude-code\":{\"status\":\"ok\"}")
}

func TestRouter_Healthcheck_WhenOneAgentRunnerFailsAnotherStaysIndependentlyOK(t *testing.T) {
	localRunner := new(mockAgentRunner)
	localRunner.On("CheckHealth", mock.Anything).Return(nil)
	claudeRunner := new(mockAgentRunner)
	claudeRunner.On("CheckHealth", mock.Anything).Return(errors.New("claude CLI not found on PATH"))

	router := NewRouter(new(mockProjectStore), fixedTaskStoreFactory(new(mockTaskStore)), new(mockKnowledgeReader),
		map[string]agentrunner.AgentRunner{"claude-code": claudeRunner, "local": localRunner}, "", nil, nil, testFrontendFS(), "test-build")

	req := httptest.NewRequest(http.MethodGet, "/healthcheck", nil)
	w := httptest.NewRecorder()
	router.ServeHTTP(w, req)

	assert.Equal(t, http.StatusServiceUnavailable, w.Code)
	assert.Contains(t, w.Body.String(), "\"agent:claude-code\":{\"status\":\"error\",\"error\":\"claude CLI not found on PATH\"}")
	assert.Contains(t, w.Body.String(), "\"agent:local\":{\"status\":\"ok\"}")
}

func TestRouter_Version(t *testing.T) {
	router := NewRouter(new(mockProjectStore), fixedTaskStoreFactory(new(mockTaskStore)), new(mockKnowledgeReader), nil, "", nil, nil, testFrontendFS(), "test-build")

	req := httptest.NewRequest(http.MethodGet, "/api/v1/version", nil)
	w := httptest.NewRecorder()
	router.ServeHTTP(w, req)

	assert.Equal(t, http.StatusOK, w.Code)
	assert.Contains(t, w.Body.String(), "version")
}

func TestRouter_FrontendServesRealFile(t *testing.T) {
	router := NewRouter(new(mockProjectStore), fixedTaskStoreFactory(new(mockTaskStore)), new(mockKnowledgeReader), nil, "", nil, nil, testFrontendFS(), "test-build")

	req := httptest.NewRequest(http.MethodGet, "/assets/app.js", nil)
	w := httptest.NewRecorder()
	router.ServeHTTP(w, req)

	require.Equal(t, http.StatusOK, w.Code)
	assert.Equal(t, "console.log('hi')", w.Body.String())
}

func TestRouter_FrontendFallsBackToIndexForUnknownPath(t *testing.T) {
	router := NewRouter(new(mockProjectStore), fixedTaskStoreFactory(new(mockTaskStore)), new(mockKnowledgeReader), nil, "", nil, nil, testFrontendFS(), "test-build")

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

	router := NewRouter(projects, fixedTaskStoreFactory(tasks), new(mockKnowledgeReader), nil, "", nil, nil, testFrontendFS(), "test-build")

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

	router := NewRouter(projects, fixedTaskStoreFactory(tasks), new(mockKnowledgeReader), nil, "", nil, nil, testFrontendFS(), "test-build")

	req := httptest.NewRequest(http.MethodGet, "/api/v1/projects/nonexistent/tasks", nil)
	w := httptest.NewRecorder()
	router.ServeHTTP(w, req)

	assert.Equal(t, http.StatusNotFound, w.Code)
}

func TestRouter_CreateAndUpdateProjectEndToEnd(t *testing.T) {
	projects := new(mockProjectStore)
	projects.On("Create", project.CreateInput{Name: "Demo"}).Return(project.Project{ID: "demo", Name: "Demo"}, nil)
	projects.On("Update", "demo", project.UpdateInput{Name: "Demo Updated"}).Return(project.Project{ID: "demo", Name: "Demo Updated"}, nil)

	router := NewRouter(projects, fixedTaskStoreFactory(new(mockTaskStore)), new(mockKnowledgeReader), nil, "", nil, nil, testFrontendFS(), "test-build")

	createReq := newProjectRequest(t, http.MethodPost, "/api/v1/projects", project.CreateInput{Name: "Demo"})
	createW := httptest.NewRecorder()
	router.ServeHTTP(createW, createReq)
	require.Equal(t, http.StatusCreated, createW.Code)

	updateReq := newProjectRequest(t, http.MethodPut, "/api/v1/projects/demo", project.UpdateInput{Name: "Demo Updated"})
	updateW := httptest.NewRecorder()
	router.ServeHTTP(updateW, updateReq)
	require.Equal(t, http.StatusOK, updateW.Code)
}

func TestRouter_GrillMeAndPlanningModeRoutesRegistered(t *testing.T) {
	projects := new(mockProjectStore)
	projects.On("Get", "demo-project").Return(project.Project{ID: "demo-project"}, nil)
	projects.On("TasksRoot", "demo-project").Return("/data/projects/demo-project/tasks", nil)

	tasks := new(mockTaskStore)
	tasks.On("Get", "TASK-0001").Return(task.Task{ID: "TASK-0001", Stage: task.StageRequirements}, nil)
	tasks.On("GetContext", "TASK-0001").Return(task.Context{Summary: "s"}, nil)
	tasks.On("GetPlan", "TASK-0001").Return(task.Plan{Approach: "a"}, nil)
	tasks.On("GetConversation", "TASK-0001", task.StageRequirements).Return(task.Conversation{Stage: task.StageRequirements}, nil)
	tasks.On("FinalizeRequirements", "TASK-0001", task.RequirementsDraft{}).Return(task.Task{ID: "TASK-0001", Stage: task.StagePlanning}, nil)
	tasks.On("FinalizePlan", "TASK-0001", task.Plan{}).Return(task.Task{ID: "TASK-0001", Stage: task.StageImplementation}, nil)
	tasks.On("ReviseToRequirements", "TASK-0001").Return(task.Task{ID: "TASK-0001", Stage: task.StageRequirements}, nil)
	tasks.On("ReviseToPlanning", "TASK-0001").Return(task.Task{ID: "TASK-0001", Stage: task.StagePlanning}, nil)

	router := NewRouter(projects, fixedTaskStoreFactory(tasks), new(mockKnowledgeReader), nil, "", nil, nil, testFrontendFS(), "test-build")

	cases := []struct {
		method, path string
		body         any
	}{
		{http.MethodGet, "/api/v1/projects/demo-project/tasks/TASK-0001/context", nil},
		{http.MethodGet, "/api/v1/projects/demo-project/tasks/TASK-0001/plan", nil},
		{http.MethodGet, "/api/v1/projects/demo-project/tasks/TASK-0001/stages/requirements/conversation", nil},
		{http.MethodPost, "/api/v1/projects/demo-project/tasks/TASK-0001/requirements/finalize", task.RequirementsDraft{}},
		{http.MethodPost, "/api/v1/projects/demo-project/tasks/TASK-0001/plan/finalize", task.Plan{}},
		{http.MethodPost, "/api/v1/projects/demo-project/tasks/TASK-0001/requirements/revise", nil},
		{http.MethodPost, "/api/v1/projects/demo-project/tasks/TASK-0001/plan/revise", nil},
	}

	for _, c := range cases {
		req := newProjectRequest(t, c.method, c.path, c.body)
		w := httptest.NewRecorder()
		router.ServeHTTP(w, req)
		assert.Equal(t, http.StatusOK, w.Code, "%s %s", c.method, c.path)
	}
}

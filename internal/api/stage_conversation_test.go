package api

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/mock"
	"github.com/stretchr/testify/require"

	"github.com/timmersuk/llm-workbench/internal/agentrunner"
	"github.com/timmersuk/llm-workbench/internal/chat"
	"github.com/timmersuk/llm-workbench/internal/knowledge"
	"github.com/timmersuk/llm-workbench/internal/project"
	"github.com/timmersuk/llm-workbench/internal/task"
)

func TestHandleGetStageConversation_InvalidStage(t *testing.T) {
	projects := new(mockProjectStore)
	projects.On("Get", "demo-project").Return(project.Project{ID: "demo-project"}, nil)
	projects.On("TasksRoot", "demo-project").Return("/data/projects/demo-project/tasks", nil)

	req := httptest.NewRequest(http.MethodGet, "/api/v1/projects/demo-project/tasks/TASK-0001/stages/implementation/conversation", nil)
	req.SetPathValue("projectId", "demo-project")
	req.SetPathValue("taskId", "TASK-0001")
	req.SetPathValue("stage", "implementation")
	w := httptest.NewRecorder()
	handleGetStageConversation(projects, fixedTaskStoreFactory(new(mockTaskStore)))(w, req)

	assert.Equal(t, http.StatusBadRequest, w.Code)
}

func TestHandleGetStageConversation_OK(t *testing.T) {
	projects := new(mockProjectStore)
	projects.On("Get", "demo-project").Return(project.Project{ID: "demo-project"}, nil)
	projects.On("TasksRoot", "demo-project").Return("/data/projects/demo-project/tasks", nil)

	tasks := new(mockTaskStore)
	tasks.On("GetConversation", "TASK-0001", task.StageRequirements).
		Return(task.Conversation{Stage: task.StageRequirements, Messages: []task.ConversationMessage{{Role: "user", Content: "hi"}}}, nil)

	req := httptest.NewRequest(http.MethodGet, "/api/v1/projects/demo-project/tasks/TASK-0001/stages/requirements/conversation", nil)
	req.SetPathValue("projectId", "demo-project")
	req.SetPathValue("taskId", "TASK-0001")
	req.SetPathValue("stage", "requirements")
	w := httptest.NewRecorder()
	handleGetStageConversation(projects, fixedTaskStoreFactory(tasks))(w, req)

	require.Equal(t, http.StatusOK, w.Code)
	var got task.Conversation
	require.NoError(t, json.Unmarshal(w.Body.Bytes(), &got))
	require.Len(t, got.Messages, 1)
	assert.Equal(t, "hi", got.Messages[0].Content)
}

// newStageMessageWorkspace creates a temp reposRoot containing a single
// resolvable repo checkout (matching agentrunner.ResolveWorkspace's
// last-path-segment convention), returning the reposRoot and the
// project.Repositories value that resolves into it. Every stage-message
// executor now requires a resolvable workspace, including "local".
func newStageMessageWorkspace(t *testing.T) (reposRoot string, repositories []string) {
	t.Helper()
	reposRoot = t.TempDir()
	require.NoError(t, os.Mkdir(filepath.Join(reposRoot, "demo-repo"), 0o755))
	return reposRoot, []string{"github.com/timmersuk/demo-repo"}
}

func newStageMessageServer(t *testing.T, tasks *mockTaskStore, repositories []string) (ProjectStore, TaskStoreFactory) {
	t.Helper()
	projects := new(mockProjectStore)
	projects.On("Get", "demo-project").Return(project.Project{
		ID: "demo-project", Name: "Demo", Description: "A demo project",
		Constraints: []string{"no new deps"}, Repositories: repositories,
	}, nil)
	projects.On("TasksRoot", "demo-project").Return("/data/projects/demo-project/tasks", nil)
	return projects, fixedTaskStoreFactory(tasks)
}

func TestHandlePostStageMessage_InvalidStage(t *testing.T) {
	req := newProjectRequest(t, http.MethodPost, "/api/v1/projects/demo-project/tasks/TASK-0001/stages/review/messages", stageMessageRequest{Content: "hi"})
	req.SetPathValue("projectId", "demo-project")
	req.SetPathValue("taskId", "TASK-0001")
	req.SetPathValue("stage", "review")
	w := httptest.NewRecorder()
	handlePostStageMessage(new(mockProjectStore), fixedTaskStoreFactory(new(mockTaskStore)), new(mockKnowledgeReader), nil, "")(w, req)

	assert.Equal(t, http.StatusBadRequest, w.Code)
}

func TestHandlePostStageMessage_ProjectNotFound(t *testing.T) {
	projects := new(mockProjectStore)
	projects.On("Get", "nonexistent").Return(nil, task.ErrInvalidID)

	req := newProjectRequest(t, http.MethodPost, "/api/v1/projects/nonexistent/tasks/TASK-0001/stages/requirements/messages", stageMessageRequest{Content: "hi"})
	req.SetPathValue("projectId", "nonexistent")
	req.SetPathValue("taskId", "TASK-0001")
	req.SetPathValue("stage", "requirements")
	w := httptest.NewRecorder()
	handlePostStageMessage(projects, fixedTaskStoreFactory(new(mockTaskStore)), new(mockKnowledgeReader),
		map[string]agentrunner.AgentRunner{"local": new(mockAgentRunner)}, "")(w, req)

	assert.Equal(t, http.StatusBadRequest, w.Code)
}

func TestHandlePostStageMessage_SeedsSystemPromptAndToolSchema(t *testing.T) {
	tasks := new(mockTaskStore)
	tasks.On("Get", "TASK-0001").Return(task.Task{
		ID: "TASK-0001", Objective: "ship login", Constraints: []string{"must use existing auth service"},
		References: task.References{Knowledge: []string{"team/style"}},
	}, nil)
	tasks.On("AppendConversationMessages", "TASK-0001", task.StageRequirements, mock.Anything).Return(task.Conversation{}, nil)

	knowledgeReader := new(mockKnowledgeReader)
	knowledgeReader.On("Get", "standards/logging").Return(knowledge.Concept{Type: "Coding Standard", Body: "use logrus"}, nil)
	knowledgeReader.On("Get", "team/style").Return(knowledge.Concept{}, assert.AnError)

	reposRoot, repositories := newStageMessageWorkspace(t)

	var gotIn agentrunner.RunInput
	runner := new(mockAgentRunner)
	runner.On("Run", mock.Anything, mock.MatchedBy(func(in agentrunner.RunInput) bool {
		gotIn = in
		return true
	}), mock.Anything).Return(nil, agentrunner.RunOutput{Content: "Sure, tell me more."}, nil)

	projects := new(mockProjectStore)
	projects.On("Get", "demo-project").Return(project.Project{
		ID: "demo-project", Name: "Demo", Description: "A demo project",
		Constraints: []string{"no new deps"}, Knowledge: []string{"standards/logging"}, Repositories: repositories,
	}, nil)
	projects.On("TasksRoot", "demo-project").Return("/data/projects/demo-project/tasks", nil)

	req := newProjectRequest(t, http.MethodPost, "/api/v1/projects/demo-project/tasks/TASK-0001/stages/requirements/messages", stageMessageRequest{Content: "let's start"})
	req.SetPathValue("projectId", "demo-project")
	req.SetPathValue("taskId", "TASK-0001")
	req.SetPathValue("stage", "requirements")
	w := httptest.NewRecorder()
	handlePostStageMessage(projects, fixedTaskStoreFactory(tasks), knowledgeReader,
		map[string]agentrunner.AgentRunner{"local": runner}, reposRoot)(w, req)

	require.Equal(t, http.StatusOK, w.Code)

	assert.Equal(t, proposeContextToolName, gotIn.Tool.Function.Name)
	assert.Equal(t, "TASK-0001:"+task.StageRequirements, gotIn.SessionKey)
	assert.Equal(t, "let's start", gotIn.UserMessage)

	assert.Contains(t, gotIn.SystemPrompt, "ship login")
	assert.Contains(t, gotIn.SystemPrompt, "must use existing auth service")
	assert.Contains(t, gotIn.SystemPrompt, "Demo")
	assert.Contains(t, gotIn.SystemPrompt, "no new deps")
	assert.Contains(t, gotIn.SystemPrompt, "use logrus") // resolved knowledge concept
}

func TestHandlePostStageMessage_SelectsPlanToolForPlanningStage(t *testing.T) {
	tasks := new(mockTaskStore)
	tasks.On("Get", "TASK-0001").Return(task.Task{ID: "TASK-0001", Stage: task.StagePlanning}, nil)
	tasks.On("AppendConversationMessages", "TASK-0001", task.StagePlanning, mock.Anything).Return(task.Conversation{}, nil)

	knowledgeReader := new(mockKnowledgeReader)

	var gotIn agentrunner.RunInput
	runner := new(mockAgentRunner)
	runner.On("Run", mock.Anything, mock.MatchedBy(func(in agentrunner.RunInput) bool {
		gotIn = in
		return true
	}), mock.Anything).Return(nil, agentrunner.RunOutput{}, nil)

	reposRoot, repositories := newStageMessageWorkspace(t)
	projects, factory := newStageMessageServer(t, tasks, repositories)

	req := newProjectRequest(t, http.MethodPost, "/api/v1/projects/demo-project/tasks/TASK-0001/stages/planning/messages", stageMessageRequest{Content: "go ahead"})
	req.SetPathValue("projectId", "demo-project")
	req.SetPathValue("taskId", "TASK-0001")
	req.SetPathValue("stage", "planning")
	w := httptest.NewRecorder()
	handlePostStageMessage(projects, factory, knowledgeReader,
		map[string]agentrunner.AgentRunner{"local": runner}, reposRoot)(w, req)

	require.Equal(t, http.StatusOK, w.Code)
	assert.Equal(t, proposePlanToolName, gotIn.Tool.Function.Name)
}

// TestHandlePostStageMessage_StreamsToolCallAsSSEEventAndPersists exercises
// the default ("" -> "local") executor path — the analogous
// TestHandlePostStageMessage_AgentExecutorStreamsToolCallAsSSEEventAndPersists
// below covers an explicitly-selected executor via the identical code path.
func TestHandlePostStageMessage_StreamsToolCallAsSSEEventAndPersists(t *testing.T) {
	tasks := new(mockTaskStore)
	tasks.On("Get", "TASK-0001").Return(task.Task{ID: "TASK-0001"}, nil)

	var persistedMsgs []task.ConversationMessage
	tasks.On("AppendConversationMessages", "TASK-0001", task.StageRequirements, mock.MatchedBy(func(msgs []task.ConversationMessage) bool {
		persistedMsgs = msgs
		return true
	})).Return(task.Conversation{}, nil)

	knowledgeReader := new(mockKnowledgeReader)

	runner := new(mockAgentRunner)
	runner.On("Run", mock.Anything, mock.Anything, mock.Anything).Return([]chat.Delta{
		{Content: "Here's my proposal: "},
	}, agentrunner.RunOutput{
		Content: "Here's my proposal: ",
		ToolCall: &chat.ToolCall{ID: "call-1", Type: "function", Function: chat.ToolCallFunction{
			Name: proposeContextToolName, Arguments: `{"objective":"ship login"}`,
		}},
	}, nil)

	reposRoot, repositories := newStageMessageWorkspace(t)
	projects, factory := newStageMessageServer(t, tasks, repositories)

	req := newProjectRequest(t, http.MethodPost, "/api/v1/projects/demo-project/tasks/TASK-0001/stages/requirements/messages", stageMessageRequest{Content: "let's start"})
	req.SetPathValue("projectId", "demo-project")
	req.SetPathValue("taskId", "TASK-0001")
	req.SetPathValue("stage", "requirements")
	w := httptest.NewRecorder()
	handlePostStageMessage(projects, factory, knowledgeReader,
		map[string]agentrunner.AgentRunner{"local": runner}, reposRoot)(w, req)

	require.Equal(t, http.StatusOK, w.Code)

	events := parseSSEEvents(t, w.Body.String())
	require.Len(t, events, 2)
	assert.Equal(t, "Here's my proposal: ", events[0].Content)
	require.NotNil(t, events[1].ToolCall)
	assert.Equal(t, proposeContextToolName, events[1].ToolCall.Name)
	assert.Equal(t, `{"objective":"ship login"}`, events[1].ToolCall.Arguments)

	require.Len(t, persistedMsgs, 2)
	assert.Equal(t, "user", persistedMsgs[0].Role)
	assert.Equal(t, "let's start", persistedMsgs[0].Content)
	assert.Equal(t, "assistant", persistedMsgs[1].Role)
	assert.Equal(t, "Here's my proposal: ", persistedMsgs[1].Content)
	require.NotNil(t, persistedMsgs[1].ToolCall)
	assert.Equal(t, "call-1", persistedMsgs[1].ToolCall.ID)
	assert.Equal(t, `{"objective":"ship login"}`, persistedMsgs[1].ToolCall.Arguments)
}

func TestHandlePostStageMessage_UnknownExecutor(t *testing.T) {
	req := newProjectRequest(t, http.MethodPost, "/api/v1/projects/demo-project/tasks/TASK-0001/stages/requirements/messages", stageMessageRequest{Content: "hi", Executor: "does-not-exist"})
	req.SetPathValue("projectId", "demo-project")
	req.SetPathValue("taskId", "TASK-0001")
	req.SetPathValue("stage", "requirements")
	w := httptest.NewRecorder()
	handlePostStageMessage(new(mockProjectStore), fixedTaskStoreFactory(new(mockTaskStore)), new(mockKnowledgeReader), nil, "")(w, req)

	assert.Equal(t, http.StatusBadRequest, w.Code)
}

func TestHandlePostStageMessage_AgentExecutorStreamsToolCallAsSSEEventAndPersists(t *testing.T) {
	reposRoot := t.TempDir()
	require.NoError(t, os.Mkdir(filepath.Join(reposRoot, "logthing"), 0o755))

	tasks := new(mockTaskStore)
	tasks.On("Get", "TASK-0001").Return(task.Task{ID: "TASK-0001"}, nil)

	var persistedMsgs []task.ConversationMessage
	tasks.On("AppendConversationMessages", "TASK-0001", task.StagePlanning, mock.MatchedBy(func(msgs []task.ConversationMessage) bool {
		persistedMsgs = msgs
		return true
	})).Return(task.Conversation{}, nil)

	knowledgeReader := new(mockKnowledgeReader)

	projects := new(mockProjectStore)
	projects.On("Get", "demo-project").Return(project.Project{
		ID: "demo-project", Name: "Demo", Repositories: []string{"github.com/timmersuk/logthing"},
	}, nil)
	projects.On("TasksRoot", "demo-project").Return("/data/projects/demo-project/tasks", nil)

	runner := new(mockAgentRunner)
	runner.On("Run", mock.Anything, mock.MatchedBy(func(in agentrunner.RunInput) bool {
		return in.SessionKey == "TASK-0001:"+task.StagePlanning &&
			in.Workspace == filepath.Join(reposRoot, "logthing") && in.UserMessage == "go ahead" &&
			in.Tool.Function.Name == proposePlanToolName
	}), mock.Anything).Return([]chat.Delta{{Content: "thinking..."}, {Content: "here's the plan"}}, agentrunner.RunOutput{
		Content: "here's the plan",
		ToolCall: &chat.ToolCall{ID: "call-1", Type: "function", Function: chat.ToolCallFunction{
			Name: proposePlanToolName, Arguments: `{"approach":"port logthing"}`,
		}},
	}, nil)

	req := newProjectRequest(t, http.MethodPost, "/api/v1/projects/demo-project/tasks/TASK-0001/stages/planning/messages", stageMessageRequest{Content: "go ahead", Executor: "claude-code"})
	req.SetPathValue("projectId", "demo-project")
	req.SetPathValue("taskId", "TASK-0001")
	req.SetPathValue("stage", "planning")
	w := httptest.NewRecorder()
	handlePostStageMessage(projects, fixedTaskStoreFactory(tasks), knowledgeReader,
		map[string]agentrunner.AgentRunner{"claude-code": runner}, reposRoot)(w, req)

	require.Equal(t, http.StatusOK, w.Code)

	events := parseSSEEvents(t, w.Body.String())
	require.Len(t, events, 3)
	assert.Equal(t, "thinking...", events[0].Content)
	assert.Equal(t, "here's the plan", events[1].Content)
	require.NotNil(t, events[2].ToolCall)
	assert.Equal(t, proposePlanToolName, events[2].ToolCall.Name)

	require.Len(t, persistedMsgs, 2)
	assert.Equal(t, "assistant", persistedMsgs[1].Role)
	assert.Equal(t, "here's the plan", persistedMsgs[1].Content)
	require.NotNil(t, persistedMsgs[1].ToolCall)
	assert.Equal(t, "call-1", persistedMsgs[1].ToolCall.ID)
	assert.Equal(t, `{"approach":"port logthing"}`, persistedMsgs[1].ToolCall.Arguments)
}

func TestHandlePostStageMessage_AgentExecutorWorkspaceResolutionFailureSurfacesAsSSEError(t *testing.T) {
	tasks := new(mockTaskStore)
	tasks.On("Get", "TASK-0001").Return(task.Task{ID: "TASK-0001"}, nil)
	tasks.On("AppendConversationMessages", "TASK-0001", task.StagePlanning, mock.Anything).Return(task.Conversation{}, nil)

	knowledgeReader := new(mockKnowledgeReader)

	projects := new(mockProjectStore)
	// No Repositories configured at all -> ResolveWorkspace fails before
	// the runner is ever invoked.
	projects.On("Get", "demo-project").Return(project.Project{ID: "demo-project", Name: "Demo"}, nil)
	projects.On("TasksRoot", "demo-project").Return("/data/projects/demo-project/tasks", nil)

	runner := new(mockAgentRunner)

	req := newProjectRequest(t, http.MethodPost, "/api/v1/projects/demo-project/tasks/TASK-0001/stages/planning/messages", stageMessageRequest{Content: "go ahead", Executor: "claude-code"})
	req.SetPathValue("projectId", "demo-project")
	req.SetPathValue("taskId", "TASK-0001")
	req.SetPathValue("stage", "planning")
	w := httptest.NewRecorder()
	handlePostStageMessage(projects, fixedTaskStoreFactory(tasks), knowledgeReader,
		map[string]agentrunner.AgentRunner{"claude-code": runner}, t.TempDir())(w, req)

	require.Equal(t, http.StatusOK, w.Code)

	events := parseSSEEvents(t, w.Body.String())
	require.Len(t, events, 1)
	assert.Contains(t, events[0].Error, "resolving workspace")
	runner.AssertNotCalled(t, "Run", mock.Anything, mock.Anything, mock.Anything)
}

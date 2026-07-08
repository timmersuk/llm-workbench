package api

import (
	"encoding/json"
	"errors"
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

func TestHandleStartStageConversation_InvalidStage(t *testing.T) {
	req := newProjectRequest(t, http.MethodPost, "/api/v1/projects/demo-project/tasks/TASK-0001/stages/review/start", stageStartRequest{})
	req.SetPathValue("projectId", "demo-project")
	req.SetPathValue("taskId", "TASK-0001")
	req.SetPathValue("stage", "review")
	w := httptest.NewRecorder()
	handleStartStageConversation(new(mockProjectStore), fixedTaskStoreFactory(new(mockTaskStore)), new(mockKnowledgeReader), nil, "")(w, req)

	assert.Equal(t, http.StatusBadRequest, w.Code)
}

func TestHandleStartStageConversation_UnknownExecutor(t *testing.T) {
	req := newProjectRequest(t, http.MethodPost, "/api/v1/projects/demo-project/tasks/TASK-0001/stages/requirements/start", stageStartRequest{Executor: "does-not-exist"})
	req.SetPathValue("projectId", "demo-project")
	req.SetPathValue("taskId", "TASK-0001")
	req.SetPathValue("stage", "requirements")
	w := httptest.NewRecorder()
	handleStartStageConversation(new(mockProjectStore), fixedTaskStoreFactory(new(mockTaskStore)), new(mockKnowledgeReader), nil, "")(w, req)

	assert.Equal(t, http.StatusBadRequest, w.Code)
}

// TestHandleStartStageConversation_RejectsWhenAlreadyStarted locks in the
// 409 guard: starting is only meaningful once, before any real exchange —
// a conversation that already has messages must continue via
// handlePostStageMessage instead.
func TestHandleStartStageConversation_RejectsWhenAlreadyStarted(t *testing.T) {
	tasks := new(mockTaskStore)
	tasks.On("Get", "TASK-0001").Return(task.Task{ID: "TASK-0001"}, nil)
	tasks.On("GetConversation", "TASK-0001", task.StageRequirements).Return(task.Conversation{
		Stage:    task.StageRequirements,
		Messages: []task.ConversationMessage{{Role: "user", Content: "already talking"}},
	}, nil)

	runner := new(mockAgentRunner)
	reposRoot, repositories := newStageMessageWorkspace(t)
	projects, factory := newStageMessageServer(t, tasks, repositories)

	req := newProjectRequest(t, http.MethodPost, "/api/v1/projects/demo-project/tasks/TASK-0001/stages/requirements/start", stageStartRequest{})
	req.SetPathValue("projectId", "demo-project")
	req.SetPathValue("taskId", "TASK-0001")
	req.SetPathValue("stage", "requirements")
	w := httptest.NewRecorder()
	handleStartStageConversation(projects, factory, new(mockKnowledgeReader),
		map[string]agentrunner.AgentRunner{"local": runner}, reposRoot)(w, req)

	assert.Equal(t, http.StatusConflict, w.Code)
	runner.AssertNotCalled(t, "Run", mock.Anything, mock.Anything, mock.Anything)
}

// TestHandleStartStageConversation_RunsKickoffTurnAndPersistsOnlyAssistant
// locks in the core behavior: an empty conversation gets one agent turn
// seeded with kickoffUserMessage (not the empty stageStartRequest content —
// there is none), and only the resulting assistant message is persisted,
// with no synthetic "user" turn alongside it.
func TestHandleStartStageConversation_RunsKickoffTurnAndPersistsOnlyAssistant(t *testing.T) {
	tasks := new(mockTaskStore)
	tasks.On("Get", "TASK-0001").Return(task.Task{ID: "TASK-0001", Objective: "ship login"}, nil)
	tasks.On("GetConversation", "TASK-0001", task.StageRequirements).Return(task.Conversation{Stage: task.StageRequirements}, nil)

	var persistedMsgs []task.ConversationMessage
	tasks.On("AppendConversationMessages", "TASK-0001", task.StageRequirements, mock.MatchedBy(func(msgs []task.ConversationMessage) bool {
		persistedMsgs = msgs
		return true
	})).Return(task.Conversation{}, nil)

	var gotIn agentrunner.RunInput
	runner := new(mockAgentRunner)
	runner.On("Run", mock.Anything, mock.MatchedBy(func(in agentrunner.RunInput) bool {
		gotIn = in
		return true
	}), mock.Anything).Return([]chat.Delta{{Content: "What's the objective?"}}, agentrunner.RunOutput{
		Content: "What's the objective?",
	}, nil)

	reposRoot, repositories := newStageMessageWorkspace(t)
	projects, factory := newStageMessageServer(t, tasks, repositories)

	req := newProjectRequest(t, http.MethodPost, "/api/v1/projects/demo-project/tasks/TASK-0001/stages/requirements/start", stageStartRequest{})
	req.SetPathValue("projectId", "demo-project")
	req.SetPathValue("taskId", "TASK-0001")
	req.SetPathValue("stage", "requirements")
	w := httptest.NewRecorder()
	handleStartStageConversation(projects, factory, new(mockKnowledgeReader),
		map[string]agentrunner.AgentRunner{"local": runner}, reposRoot)(w, req)

	require.Equal(t, http.StatusOK, w.Code)

	assert.Equal(t, kickoffUserMessage, gotIn.UserMessage)
	assert.Equal(t, proposeContextToolName, gotIn.Tool.Function.Name)
	assert.Contains(t, gotIn.SystemPrompt, "ship login")

	events := parseSSEEvents(t, w.Body.String())
	require.Len(t, events, 1)
	assert.Equal(t, "What's the objective?", events[0].Content)

	require.Len(t, persistedMsgs, 1, "only the assistant's opening question is persisted, no synthetic user turn")
	assert.Equal(t, "assistant", persistedMsgs[0].Role)
	assert.Equal(t, "What's the objective?", persistedMsgs[0].Content)
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
	tasks.On("GetConversation", "TASK-0001", task.StageRequirements).Return(task.Conversation{}, nil)
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

// TestHandlePostStageMessage_PassesPersistedConversationAsHistory locks in
// session rehydration (plan item 3): the persisted conversation is loaded
// and mapped into RunInput.History on every turn, so an AgentRunner that
// lost its in-memory session (e.g. after a server restart) can replay it
// into a fresh one — a tool-call proposal is flattened into the assistant
// message's content rather than reconstructed structurally.
func TestHandlePostStageMessage_PassesPersistedConversationAsHistory(t *testing.T) {
	tasks := new(mockTaskStore)
	tasks.On("Get", "TASK-0001").Return(task.Task{ID: "TASK-0001"}, nil)
	tasks.On("GetConversation", "TASK-0001", task.StageRequirements).Return(task.Conversation{
		Stage: task.StageRequirements,
		Messages: []task.ConversationMessage{
			{Role: "user", Content: "let's start"},
			{Role: "assistant", Content: "sure", ToolCall: &task.ConversationToolCall{
				ID: "call-0", Name: proposeContextToolName, Arguments: `{"objective":"draft one"}`,
			}},
		},
	}, nil)
	tasks.On("AppendConversationMessages", "TASK-0001", task.StageRequirements, mock.Anything).Return(task.Conversation{}, nil)

	knowledgeReader := new(mockKnowledgeReader)

	var gotIn agentrunner.RunInput
	runner := new(mockAgentRunner)
	runner.On("Run", mock.Anything, mock.MatchedBy(func(in agentrunner.RunInput) bool {
		gotIn = in
		return true
	}), mock.Anything).Return(nil, agentrunner.RunOutput{}, nil)

	reposRoot, repositories := newStageMessageWorkspace(t)
	projects, factory := newStageMessageServer(t, tasks, repositories)

	req := newProjectRequest(t, http.MethodPost, "/api/v1/projects/demo-project/tasks/TASK-0001/stages/requirements/messages", stageMessageRequest{Content: "keep going"})
	req.SetPathValue("projectId", "demo-project")
	req.SetPathValue("taskId", "TASK-0001")
	req.SetPathValue("stage", "requirements")
	w := httptest.NewRecorder()
	handlePostStageMessage(projects, factory, knowledgeReader,
		map[string]agentrunner.AgentRunner{"local": runner}, reposRoot)(w, req)

	require.Equal(t, http.StatusOK, w.Code)
	require.Len(t, gotIn.History, 2)
	assert.Equal(t, "user", gotIn.History[0].Role)
	assert.Equal(t, "let's start", gotIn.History[0].Content)
	assert.Equal(t, "assistant", gotIn.History[1].Role)
	assert.Contains(t, gotIn.History[1].Content, "sure")
	assert.Contains(t, gotIn.History[1].Content, proposeContextToolName)
}

func TestHandlePostStageMessage_SelectsPlanToolForPlanningStage(t *testing.T) {
	tasks := new(mockTaskStore)
	tasks.On("Get", "TASK-0001").Return(task.Task{ID: "TASK-0001", Stage: task.StagePlanning}, nil)
	tasks.On("GetConversation", "TASK-0001", task.StagePlanning).Return(task.Conversation{}, nil)
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
	tasks.On("GetConversation", "TASK-0001", task.StageRequirements).Return(task.Conversation{}, nil)

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

// TestHandlePostStageMessage_IgnoresMismatchedToolCallName covers a model
// hallucinating a tool call for a tool that was never offered (e.g. a local
// OpenAI-compatible model emitting a "Glob" tool_calls delta when only
// propose_context was in the request's Tools array) — it must not be
// surfaced as a Draft proposal or persisted.
func TestHandlePostStageMessage_IgnoresMismatchedToolCallName(t *testing.T) {
	tasks := new(mockTaskStore)
	tasks.On("Get", "TASK-0001").Return(task.Task{ID: "TASK-0001"}, nil)
	tasks.On("GetConversation", "TASK-0001", task.StageRequirements).Return(task.Conversation{}, nil)

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
			Name: "Glob", Arguments: `{"pattern":"**/*.go"}`,
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
	require.Len(t, events, 1)
	assert.Equal(t, "Here's my proposal: ", events[0].Content)
	assert.Nil(t, events[0].ToolCall)

	require.Len(t, persistedMsgs, 2)
	assert.Equal(t, "assistant", persistedMsgs[1].Role)
	assert.Equal(t, "Here's my proposal: ", persistedMsgs[1].Content)
	assert.Nil(t, persistedMsgs[1].ToolCall)
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
	tasks.On("GetConversation", "TASK-0001", task.StagePlanning).Return(task.Conversation{}, nil)

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
	// An unresolvable repository identifier (no matching checkout under
	// reposRoot) is real misconfiguration, unlike an absent-by-design
	// repository (ErrNoRepository, covered by the no-repository test
	// below) -> ResolveWorkspace still fails fatally, before the runner is
	// ever invoked.
	projects.On("Get", "demo-project").Return(project.Project{
		ID: "demo-project", Name: "Demo", Repositories: []string{"github.com/timmersuk/does-not-exist"},
	}, nil)
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

// TestHandlePostStageMessage_NoRepositoryProceedsWithEmptyWorkspace locks in
// the no-repo fix: a project with no configured repository at all
// (agentrunner.ErrNoRepository) is a normal state — e.g. a pure-planning
// project with nothing to check out — so the turn proceeds with an empty
// Workspace instead of aborting like a genuine misconfiguration would.
func TestHandlePostStageMessage_NoRepositoryProceedsWithEmptyWorkspace(t *testing.T) {
	tasks := new(mockTaskStore)
	tasks.On("Get", "TASK-0001").Return(task.Task{ID: "TASK-0001"}, nil)
	tasks.On("GetConversation", "TASK-0001", task.StagePlanning).Return(task.Conversation{}, nil)
	tasks.On("AppendConversationMessages", "TASK-0001", task.StagePlanning, mock.Anything).Return(task.Conversation{}, nil)

	knowledgeReader := new(mockKnowledgeReader)

	projects := new(mockProjectStore)
	// No Repositories configured at all -> ResolveWorkspace returns
	// ErrNoRepository, which is now tolerated.
	projects.On("Get", "demo-project").Return(project.Project{ID: "demo-project", Name: "Demo"}, nil)
	projects.On("TasksRoot", "demo-project").Return("/data/projects/demo-project/tasks", nil)

	var gotIn agentrunner.RunInput
	runner := new(mockAgentRunner)
	runner.On("Run", mock.Anything, mock.MatchedBy(func(in agentrunner.RunInput) bool {
		gotIn = in
		return true
	}), mock.Anything).Return(nil, agentrunner.RunOutput{Content: "ok"}, nil)

	req := newProjectRequest(t, http.MethodPost, "/api/v1/projects/demo-project/tasks/TASK-0001/stages/planning/messages", stageMessageRequest{Content: "go ahead", Executor: "local"})
	req.SetPathValue("projectId", "demo-project")
	req.SetPathValue("taskId", "TASK-0001")
	req.SetPathValue("stage", "planning")
	w := httptest.NewRecorder()
	handlePostStageMessage(projects, fixedTaskStoreFactory(tasks), knowledgeReader,
		map[string]agentrunner.AgentRunner{"local": runner}, t.TempDir())(w, req)

	require.Equal(t, http.StatusOK, w.Code)

	events := parseSSEEvents(t, w.Body.String())
	assert.Empty(t, events, "no error event should be emitted when ErrNoRepository is tolerated")
	assert.Equal(t, "", gotIn.Workspace)
}

// TestHandlePostStageMessage_PersistsErrorOnFailedTurn locks in the fix for
// "history recorded my attempt but not the error": a failed turn must
// persist its error onto the assistant message, not silently save an empty
// reply with no record anything went wrong.
func TestHandlePostStageMessage_PersistsErrorOnFailedTurn(t *testing.T) {
	tasks := new(mockTaskStore)
	tasks.On("Get", "TASK-0001").Return(task.Task{ID: "TASK-0001"}, nil)
	tasks.On("GetConversation", "TASK-0001", task.StageRequirements).Return(task.Conversation{}, nil)

	var persistedMsgs []task.ConversationMessage
	tasks.On("AppendConversationMessages", "TASK-0001", task.StageRequirements, mock.MatchedBy(func(msgs []task.ConversationMessage) bool {
		persistedMsgs = msgs
		return true
	})).Return(task.Conversation{}, nil)

	runner := new(mockAgentRunner)
	runner.On("Run", mock.Anything, mock.Anything, mock.Anything).
		Return(nil, agentrunner.RunOutput{}, errors.New("write |1: The pipe is being closed."))

	reposRoot, repositories := newStageMessageWorkspace(t)
	projects, factory := newStageMessageServer(t, tasks, repositories)

	req := newProjectRequest(t, http.MethodPost, "/api/v1/projects/demo-project/tasks/TASK-0001/stages/requirements/messages", stageMessageRequest{Content: "hi"})
	req.SetPathValue("projectId", "demo-project")
	req.SetPathValue("taskId", "TASK-0001")
	req.SetPathValue("stage", "requirements")
	w := httptest.NewRecorder()
	handlePostStageMessage(projects, factory, new(mockKnowledgeReader),
		map[string]agentrunner.AgentRunner{"local": runner}, reposRoot)(w, req)

	require.Equal(t, http.StatusOK, w.Code)
	require.Len(t, persistedMsgs, 2)
	assert.Equal(t, "assistant", persistedMsgs[1].Role)
	assert.Contains(t, persistedMsgs[1].Error, "pipe is being closed")
}

func TestHandleDeleteStageMessage_InvalidStage(t *testing.T) {
	req := httptest.NewRequest(http.MethodDelete, "/api/v1/projects/demo-project/tasks/TASK-0001/stages/review/messages/0", nil)
	req.SetPathValue("projectId", "demo-project")
	req.SetPathValue("taskId", "TASK-0001")
	req.SetPathValue("stage", "review")
	req.SetPathValue("index", "0")
	w := httptest.NewRecorder()
	handleDeleteStageMessage(new(mockProjectStore), fixedTaskStoreFactory(new(mockTaskStore)), nil)(w, req)

	assert.Equal(t, http.StatusBadRequest, w.Code)
}

func TestHandleDeleteStageMessage_InvalidIndex(t *testing.T) {
	projects := new(mockProjectStore)
	projects.On("Get", "demo-project").Return(project.Project{ID: "demo-project"}, nil)
	projects.On("TasksRoot", "demo-project").Return("/data/projects/demo-project/tasks", nil)

	req := httptest.NewRequest(http.MethodDelete, "/api/v1/projects/demo-project/tasks/TASK-0001/stages/requirements/messages/notanumber", nil)
	req.SetPathValue("projectId", "demo-project")
	req.SetPathValue("taskId", "TASK-0001")
	req.SetPathValue("stage", "requirements")
	req.SetPathValue("index", "notanumber")
	w := httptest.NewRecorder()
	handleDeleteStageMessage(projects, fixedTaskStoreFactory(new(mockTaskStore)), nil)(w, req)

	assert.Equal(t, http.StatusBadRequest, w.Code)
}

func TestHandleDeleteStageMessage_OutOfRangeIndex(t *testing.T) {
	projects := new(mockProjectStore)
	projects.On("Get", "demo-project").Return(project.Project{ID: "demo-project"}, nil)
	projects.On("TasksRoot", "demo-project").Return("/data/projects/demo-project/tasks", nil)

	tasks := new(mockTaskStore)
	tasks.On("GetConversation", "TASK-0001", task.StageRequirements).Return(task.Conversation{
		Stage: task.StageRequirements,
		Messages: []task.ConversationMessage{
			{Role: "user", Content: "hi"},
		},
	}, nil)

	req := httptest.NewRequest(http.MethodDelete, "/api/v1/projects/demo-project/tasks/TASK-0001/stages/requirements/messages/5", nil)
	req.SetPathValue("projectId", "demo-project")
	req.SetPathValue("taskId", "TASK-0001")
	req.SetPathValue("stage", "requirements")
	req.SetPathValue("index", "5")
	w := httptest.NewRecorder()
	handleDeleteStageMessage(projects, fixedTaskStoreFactory(tasks), nil)(w, req)

	assert.Equal(t, http.StatusBadRequest, w.Code)
}

// TestHandleDeleteStageMessage_RemovesOnlyThatMessageAndEvictsSessions
// locks in delete's core contract: exactly one message is removed (not a
// truncation of everything after it), the result is persisted via
// ReplaceConversationMessages, and every registered runner's session for
// this task+stage is evicted so the deletion actually reaches what the
// model sees on its next turn.
func TestHandleDeleteStageMessage_RemovesOnlyThatMessageAndEvictsSessions(t *testing.T) {
	projects := new(mockProjectStore)
	projects.On("Get", "demo-project").Return(project.Project{ID: "demo-project"}, nil)
	projects.On("TasksRoot", "demo-project").Return("/data/projects/demo-project/tasks", nil)

	tasks := new(mockTaskStore)
	tasks.On("GetConversation", "TASK-0001", task.StageRequirements).Return(task.Conversation{
		Stage: task.StageRequirements,
		Messages: []task.ConversationMessage{
			{Role: "user", Content: "first"},
			{Role: "assistant", Content: "reply"},
			{Role: "user", Content: "third"},
		},
	}, nil)

	var replaced []task.ConversationMessage
	tasks.On("ReplaceConversationMessages", "TASK-0001", task.StageRequirements, mock.MatchedBy(func(msgs []task.ConversationMessage) bool {
		replaced = msgs
		return true
	})).Return(task.Conversation{Stage: task.StageRequirements, Messages: []task.ConversationMessage{
		{Role: "user", Content: "first"}, {Role: "user", Content: "third"},
	}}, nil)

	runner := new(mockAgentRunner)
	runner.On("CloseSession", "TASK-0001:"+task.StageRequirements).Return()

	req := httptest.NewRequest(http.MethodDelete, "/api/v1/projects/demo-project/tasks/TASK-0001/stages/requirements/messages/1", nil)
	req.SetPathValue("projectId", "demo-project")
	req.SetPathValue("taskId", "TASK-0001")
	req.SetPathValue("stage", "requirements")
	req.SetPathValue("index", "1")
	w := httptest.NewRecorder()
	handleDeleteStageMessage(projects, fixedTaskStoreFactory(tasks), map[string]agentrunner.AgentRunner{"local": runner})(w, req)

	require.Equal(t, http.StatusOK, w.Code)
	require.Len(t, replaced, 2)
	assert.Equal(t, "first", replaced[0].Content)
	assert.Equal(t, "third", replaced[1].Content)
	runner.AssertCalled(t, "CloseSession", "TASK-0001:"+task.StageRequirements)

	var got task.Conversation
	require.NoError(t, json.Unmarshal(w.Body.Bytes(), &got))
	assert.Len(t, got.Messages, 2)
}

func TestHandleRegenerateStageMessage_InvalidStage(t *testing.T) {
	req := newProjectRequest(t, http.MethodPost, "/api/v1/projects/demo-project/tasks/TASK-0001/stages/review/messages/0/regenerate", stageRegenerateRequest{Content: "hi"})
	req.SetPathValue("projectId", "demo-project")
	req.SetPathValue("taskId", "TASK-0001")
	req.SetPathValue("stage", "review")
	req.SetPathValue("index", "0")
	w := httptest.NewRecorder()
	handleRegenerateStageMessage(new(mockProjectStore), fixedTaskStoreFactory(new(mockTaskStore)), new(mockKnowledgeReader), nil, "")(w, req)

	assert.Equal(t, http.StatusBadRequest, w.Code)
}

func TestHandleRegenerateStageMessage_RejectsNonUserIndex(t *testing.T) {
	projects := new(mockProjectStore)
	projects.On("Get", "demo-project").Return(project.Project{ID: "demo-project"}, nil)
	projects.On("TasksRoot", "demo-project").Return("/data/projects/demo-project/tasks", nil)

	tasks := new(mockTaskStore)
	tasks.On("GetConversation", "TASK-0001", task.StageRequirements).Return(task.Conversation{
		Stage: task.StageRequirements,
		Messages: []task.ConversationMessage{
			{Role: "user", Content: "hi"},
			{Role: "assistant", Content: "hello"},
		},
	}, nil)

	req := newProjectRequest(t, http.MethodPost, "/api/v1/projects/demo-project/tasks/TASK-0001/stages/requirements/messages/1/regenerate", stageRegenerateRequest{Content: "hi"})
	req.SetPathValue("projectId", "demo-project")
	req.SetPathValue("taskId", "TASK-0001")
	req.SetPathValue("stage", "requirements")
	req.SetPathValue("index", "1")
	w := httptest.NewRecorder()
	handleRegenerateStageMessage(projects, fixedTaskStoreFactory(tasks), new(mockKnowledgeReader), nil, "")(w, req)

	assert.Equal(t, http.StatusBadRequest, w.Code)
}

// TestHandleRegenerateStageMessage_TruncatesHistoryEvictsSessionAndPersists
// locks in the core contract shared by Regenerate and Edit: the runner is
// invoked with History built from everything strictly before the targeted
// index (not the stale trailing messages), sessions are evicted first so
// that History is actually consulted, and the persisted result replaces
// everything from index onward with a fresh [user, assistant] pair.
func TestHandleRegenerateStageMessage_TruncatesHistoryEvictsSessionAndPersists(t *testing.T) {
	tasks := new(mockTaskStore)
	tasks.On("Get", "TASK-0001").Return(task.Task{ID: "TASK-0001"}, nil)
	tasks.On("GetConversation", "TASK-0001", task.StageRequirements).Return(task.Conversation{
		Stage: task.StageRequirements,
		Messages: []task.ConversationMessage{
			{Role: "user", Content: "earlier"},
			{Role: "assistant", Content: "earlier reply"},
			{Role: "user", Content: "original question"},
			{Role: "assistant", Content: "stale reply"},
		},
	}, nil)

	var replaced []task.ConversationMessage
	tasks.On("ReplaceConversationMessages", "TASK-0001", task.StageRequirements, mock.MatchedBy(func(msgs []task.ConversationMessage) bool {
		replaced = msgs
		return true
	})).Return(task.Conversation{}, nil)

	var gotIn agentrunner.RunInput
	runner := new(mockAgentRunner)
	runner.On("CloseSession", "TASK-0001:"+task.StageRequirements).Return()
	runner.On("Run", mock.Anything, mock.MatchedBy(func(in agentrunner.RunInput) bool {
		gotIn = in
		return true
	}), mock.Anything).Return(nil, agentrunner.RunOutput{Content: "fresh reply"}, nil)

	reposRoot, repositories := newStageMessageWorkspace(t)
	projects, factory := newStageMessageServer(t, tasks, repositories)

	req := newProjectRequest(t, http.MethodPost, "/api/v1/projects/demo-project/tasks/TASK-0001/stages/requirements/messages/2/regenerate", stageRegenerateRequest{Content: "original question"})
	req.SetPathValue("projectId", "demo-project")
	req.SetPathValue("taskId", "TASK-0001")
	req.SetPathValue("stage", "requirements")
	req.SetPathValue("index", "2")
	w := httptest.NewRecorder()
	handleRegenerateStageMessage(projects, factory, new(mockKnowledgeReader),
		map[string]agentrunner.AgentRunner{"local": runner}, reposRoot)(w, req)

	require.Equal(t, http.StatusOK, w.Code)
	runner.AssertCalled(t, "CloseSession", "TASK-0001:"+task.StageRequirements)

	require.Len(t, gotIn.History, 2)
	assert.Equal(t, "earlier", gotIn.History[0].Content)
	assert.Equal(t, "earlier reply", gotIn.History[1].Content)
	assert.Equal(t, "original question", gotIn.UserMessage)

	require.Len(t, replaced, 4)
	assert.Equal(t, "earlier", replaced[0].Content)
	assert.Equal(t, "earlier reply", replaced[1].Content)
	assert.Equal(t, "original question", replaced[2].Content)
	assert.Equal(t, "fresh reply", replaced[3].Content)
	assert.False(t, replaced[2].CreatedAt.IsZero(), "the new user turn must be stamped, not left at its zero value")
	assert.False(t, replaced[3].CreatedAt.IsZero(), "the new assistant turn must be stamped, not left at its zero value")
}

// TestHandleRegenerateStageMessage_EditUsesNewContent confirms Edit's
// variant of the same endpoint: targeting the user message's own index
// (not the following assistant's) with new content produces
// [..., user(newContent), assistant] — the old user text is gone, not kept
// alongside the edit.
func TestHandleRegenerateStageMessage_EditUsesNewContent(t *testing.T) {
	tasks := new(mockTaskStore)
	tasks.On("Get", "TASK-0001").Return(task.Task{ID: "TASK-0001"}, nil)
	tasks.On("GetConversation", "TASK-0001", task.StageRequirements).Return(task.Conversation{
		Stage: task.StageRequirements,
		Messages: []task.ConversationMessage{
			{Role: "user", Content: "original question"},
			{Role: "assistant", Content: "stale reply"},
		},
	}, nil)

	var replaced []task.ConversationMessage
	tasks.On("ReplaceConversationMessages", "TASK-0001", task.StageRequirements, mock.MatchedBy(func(msgs []task.ConversationMessage) bool {
		replaced = msgs
		return true
	})).Return(task.Conversation{}, nil)

	runner := new(mockAgentRunner)
	runner.On("CloseSession", "TASK-0001:"+task.StageRequirements).Return()
	runner.On("Run", mock.Anything, mock.Anything, mock.Anything).Return(nil, agentrunner.RunOutput{Content: "new reply"}, nil)

	reposRoot, repositories := newStageMessageWorkspace(t)
	projects, factory := newStageMessageServer(t, tasks, repositories)

	req := newProjectRequest(t, http.MethodPost, "/api/v1/projects/demo-project/tasks/TASK-0001/stages/requirements/messages/0/regenerate", stageRegenerateRequest{Content: "edited question"})
	req.SetPathValue("projectId", "demo-project")
	req.SetPathValue("taskId", "TASK-0001")
	req.SetPathValue("stage", "requirements")
	req.SetPathValue("index", "0")
	w := httptest.NewRecorder()
	handleRegenerateStageMessage(projects, factory, new(mockKnowledgeReader),
		map[string]agentrunner.AgentRunner{"local": runner}, reposRoot)(w, req)

	require.Equal(t, http.StatusOK, w.Code)
	require.Len(t, replaced, 2)
	assert.Equal(t, "edited question", replaced[0].Content)
	assert.Equal(t, "new reply", replaced[1].Content)
}

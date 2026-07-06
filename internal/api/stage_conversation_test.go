package api

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/mock"
	"github.com/stretchr/testify/require"

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

func newStageMessageServer(t *testing.T, tasks *mockTaskStore, chatCompleter *mockChatCompleter, knowledgeReader *mockKnowledgeReader) (ProjectStore, TaskStoreFactory) {
	t.Helper()
	projects := new(mockProjectStore)
	projects.On("Get", "demo-project").Return(project.Project{
		ID: "demo-project", Name: "Demo", Description: "A demo project",
		Constraints: []string{"no new deps"},
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
	handlePostStageMessage(new(mockProjectStore), fixedTaskStoreFactory(new(mockTaskStore)), new(mockChatCompleter), new(mockKnowledgeReader))(w, req)

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
	handlePostStageMessage(projects, fixedTaskStoreFactory(new(mockTaskStore)), new(mockChatCompleter), new(mockKnowledgeReader))(w, req)

	assert.Equal(t, http.StatusBadRequest, w.Code)
}

func TestHandlePostStageMessage_SeedsSystemPromptAndToolSchema(t *testing.T) {
	tasks := new(mockTaskStore)
	tasks.On("Get", "TASK-0001").Return(task.Task{
		ID: "TASK-0001", Objective: "ship login", Constraints: []string{"must use existing auth service"},
		References: task.References{Knowledge: []string{"team/style"}},
	}, nil)
	tasks.On("GetConversation", "TASK-0001", task.StageRequirements).Return(task.Conversation{Stage: task.StageRequirements}, nil)
	tasks.On("AppendConversationMessages", "TASK-0001", task.StageRequirements, mock.Anything).Return(task.Conversation{}, nil)

	knowledgeReader := new(mockKnowledgeReader)
	knowledgeReader.On("Get", "standards/logging").Return(knowledge.Concept{Type: "Coding Standard", Body: "use logrus"}, nil)
	knowledgeReader.On("Get", "team/style").Return(knowledge.Concept{}, assert.AnError)

	chatCompleter := new(mockChatCompleter)
	var gotReq chat.CompletionRequest
	chatCompleter.On("StreamChatCompletion", mock.Anything, mock.MatchedBy(func(r chat.CompletionRequest) bool {
		gotReq = r
		return true
	}), mock.Anything).Return([]chat.Delta{{Content: "Sure, tell me more."}}, nil)

	projects := new(mockProjectStore)
	projects.On("Get", "demo-project").Return(project.Project{
		ID: "demo-project", Name: "Demo", Description: "A demo project",
		Constraints: []string{"no new deps"}, Knowledge: []string{"standards/logging"},
	}, nil)
	projects.On("TasksRoot", "demo-project").Return("/data/projects/demo-project/tasks", nil)

	req := newProjectRequest(t, http.MethodPost, "/api/v1/projects/demo-project/tasks/TASK-0001/stages/requirements/messages", stageMessageRequest{Content: "let's start"})
	req.SetPathValue("projectId", "demo-project")
	req.SetPathValue("taskId", "TASK-0001")
	req.SetPathValue("stage", "requirements")
	w := httptest.NewRecorder()
	handlePostStageMessage(projects, fixedTaskStoreFactory(tasks), chatCompleter, knowledgeReader)(w, req)

	require.Equal(t, http.StatusOK, w.Code)

	require.Len(t, gotReq.Tools, 1)
	assert.Equal(t, proposeContextToolName, gotReq.Tools[0].Function.Name)

	require.NotEmpty(t, gotReq.Messages)
	systemMsg := gotReq.Messages[0]
	assert.Equal(t, "system", systemMsg.Role)
	assert.Contains(t, systemMsg.Content, "ship login")
	assert.Contains(t, systemMsg.Content, "must use existing auth service")
	assert.Contains(t, systemMsg.Content, "Demo")
	assert.Contains(t, systemMsg.Content, "no new deps")
	assert.Contains(t, systemMsg.Content, "use logrus") // resolved knowledge concept

	// The last message is the new user turn.
	lastMsg := gotReq.Messages[len(gotReq.Messages)-1]
	assert.Equal(t, "user", lastMsg.Role)
	assert.Equal(t, "let's start", lastMsg.Content)
}

func TestHandlePostStageMessage_SelectsPlanToolForPlanningStage(t *testing.T) {
	tasks := new(mockTaskStore)
	tasks.On("Get", "TASK-0001").Return(task.Task{ID: "TASK-0001", Stage: task.StagePlanning}, nil)
	tasks.On("GetConversation", "TASK-0001", task.StagePlanning).Return(task.Conversation{Stage: task.StagePlanning}, nil)
	tasks.On("AppendConversationMessages", "TASK-0001", task.StagePlanning, mock.Anything).Return(task.Conversation{}, nil)

	knowledgeReader := new(mockKnowledgeReader)
	chatCompleter := new(mockChatCompleter)
	var gotReq chat.CompletionRequest
	chatCompleter.On("StreamChatCompletion", mock.Anything, mock.MatchedBy(func(r chat.CompletionRequest) bool {
		gotReq = r
		return true
	}), mock.Anything).Return([]chat.Delta{}, nil)

	projects, factory := newStageMessageServer(t, tasks, chatCompleter, knowledgeReader)

	req := newProjectRequest(t, http.MethodPost, "/api/v1/projects/demo-project/tasks/TASK-0001/stages/planning/messages", stageMessageRequest{Content: "go ahead"})
	req.SetPathValue("projectId", "demo-project")
	req.SetPathValue("taskId", "TASK-0001")
	req.SetPathValue("stage", "planning")
	w := httptest.NewRecorder()
	handlePostStageMessage(projects, factory, chatCompleter, knowledgeReader)(w, req)

	require.Equal(t, http.StatusOK, w.Code)
	require.Len(t, gotReq.Tools, 1)
	assert.Equal(t, proposePlanToolName, gotReq.Tools[0].Function.Name)
}

func TestHandlePostStageMessage_StreamsToolCallAsSSEEventAndPersists(t *testing.T) {
	tasks := new(mockTaskStore)
	tasks.On("Get", "TASK-0001").Return(task.Task{ID: "TASK-0001"}, nil)
	tasks.On("GetConversation", "TASK-0001", task.StageRequirements).Return(task.Conversation{Stage: task.StageRequirements}, nil)

	var persistedMsgs []task.ConversationMessage
	tasks.On("AppendConversationMessages", "TASK-0001", task.StageRequirements, mock.MatchedBy(func(msgs []task.ConversationMessage) bool {
		persistedMsgs = msgs
		return true
	})).Return(task.Conversation{}, nil)

	knowledgeReader := new(mockKnowledgeReader)
	chatCompleter := new(mockChatCompleter)
	chatCompleter.On("StreamChatCompletion", mock.Anything, mock.Anything, mock.Anything).Return([]chat.Delta{
		{Content: "Here's my proposal: "},
		{ToolCall: &chat.ToolCall{ID: "call-1", Type: "function", Function: chat.ToolCallFunction{
			Name: proposeContextToolName, Arguments: `{"objective":"ship login"}`,
		}}},
	}, nil)

	projects, factory := newStageMessageServer(t, tasks, chatCompleter, knowledgeReader)

	req := newProjectRequest(t, http.MethodPost, "/api/v1/projects/demo-project/tasks/TASK-0001/stages/requirements/messages", stageMessageRequest{Content: "let's start"})
	req.SetPathValue("projectId", "demo-project")
	req.SetPathValue("taskId", "TASK-0001")
	req.SetPathValue("stage", "requirements")
	w := httptest.NewRecorder()
	handlePostStageMessage(projects, factory, chatCompleter, knowledgeReader)(w, req)

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

func TestHandlePostStageMessage_ReplaysPriorToolCallAsAssistantAndSyntheticToolMessage(t *testing.T) {
	tasks := new(mockTaskStore)
	tasks.On("Get", "TASK-0001").Return(task.Task{ID: "TASK-0001"}, nil)
	tasks.On("GetConversation", "TASK-0001", task.StageRequirements).Return(task.Conversation{
		Stage: task.StageRequirements,
		Messages: []task.ConversationMessage{
			{Role: "user", Content: "let's start"},
			{Role: "assistant", ToolCall: &task.ConversationToolCall{ID: "call-1", Name: proposeContextToolName, Arguments: `{"objective":"x"}`}},
		},
	}, nil)
	tasks.On("AppendConversationMessages", "TASK-0001", task.StageRequirements, mock.Anything).Return(task.Conversation{}, nil)

	knowledgeReader := new(mockKnowledgeReader)
	chatCompleter := new(mockChatCompleter)
	var gotReq chat.CompletionRequest
	chatCompleter.On("StreamChatCompletion", mock.Anything, mock.MatchedBy(func(r chat.CompletionRequest) bool {
		gotReq = r
		return true
	}), mock.Anything).Return([]chat.Delta{}, nil)

	projects, factory := newStageMessageServer(t, tasks, chatCompleter, knowledgeReader)

	req := newProjectRequest(t, http.MethodPost, "/api/v1/projects/demo-project/tasks/TASK-0001/stages/requirements/messages", stageMessageRequest{Content: "actually, one more thing"})
	req.SetPathValue("projectId", "demo-project")
	req.SetPathValue("taskId", "TASK-0001")
	req.SetPathValue("stage", "requirements")
	w := httptest.NewRecorder()
	handlePostStageMessage(projects, factory, chatCompleter, knowledgeReader)(w, req)

	require.Equal(t, http.StatusOK, w.Code)

	// system, user, assistant(tool_calls), tool(synthetic reply), user(new)
	require.Len(t, gotReq.Messages, 5)
	assert.Equal(t, "assistant", gotReq.Messages[2].Role)
	require.Len(t, gotReq.Messages[2].ToolCalls, 1)
	assert.Equal(t, "call-1", gotReq.Messages[2].ToolCalls[0].ID)
	assert.Equal(t, "tool", gotReq.Messages[3].Role)
	assert.Equal(t, "call-1", gotReq.Messages[3].ToolCallID)
}

package api

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/mock"
	"github.com/stretchr/testify/require"

	"github.com/timmersuk/llm-workbench/internal/agentrunner"
	"github.com/timmersuk/llm-workbench/internal/chat"
	"github.com/timmersuk/llm-workbench/internal/project"
	"github.com/timmersuk/llm-workbench/internal/task"
)

func TestHandleGetTaskDraftConversation_ProjectNotFound(t *testing.T) {
	projects := new(mockProjectStore)
	projects.On("Get", "nonexistent").Return(nil, task.ErrInvalidID)

	req := httptest.NewRequest(http.MethodGet, "/api/v1/projects/nonexistent/task-drafts/session-1/conversation", nil)
	req.SetPathValue("projectId", "nonexistent")
	req.SetPathValue("sessionId", "session-1")
	w := httptest.NewRecorder()
	(&Server{Projects: projects, Tasks: new(mockTaskStore)}).handleGetTaskDraftConversation()(w, req)

	assert.Equal(t, http.StatusBadRequest, w.Code)
}

func TestHandleGetTaskDraftConversation_OK(t *testing.T) {
	projects := new(mockProjectStore)
	projects.On("Get", "demo-project").Return(project.Project{ID: "demo-project"}, nil)

	tasks := new(mockTaskStore)
	stubTaskDraftSessionIDCalls(tasks)
	tasks.On("GetTaskDraftConversation", "demo-project", "session-1").
		Return(task.Conversation{Messages: []task.ConversationMessage{{Role: "user", Content: "hi"}}}, nil)

	req := httptest.NewRequest(http.MethodGet, "/api/v1/projects/demo-project/task-drafts/session-1/conversation", nil)
	req.SetPathValue("projectId", "demo-project")
	req.SetPathValue("sessionId", "session-1")
	w := httptest.NewRecorder()
	(&Server{Projects: projects, Tasks: tasks}).handleGetTaskDraftConversation()(w, req)

	require.Equal(t, http.StatusOK, w.Code)
	var got task.Conversation
	require.NoError(t, json.Unmarshal(w.Body.Bytes(), &got))
	require.Len(t, got.Messages, 1)
	assert.Equal(t, "hi", got.Messages[0].Content)
}

func TestHandleStartTaskDraftConversation_UnknownExecutor(t *testing.T) {
	req := newProjectRequest(t, http.MethodPost, "/api/v1/projects/demo-project/task-drafts/session-1/start", stageStartRequest{Executor: "nope"})
	req.SetPathValue("projectId", "demo-project")
	req.SetPathValue("sessionId", "session-1")
	w := httptest.NewRecorder()
	(&Server{Projects: new(mockProjectStore), Tasks: new(mockTaskStore), AgentRunners: map[string]agentrunner.AgentRunner{}}).handleStartTaskDraftConversation()(w, req)

	assert.Equal(t, http.StatusBadRequest, w.Code)
}

func TestHandleStartTaskDraftConversation_RejectsWhenAlreadyStarted(t *testing.T) {
	projects := new(mockProjectStore)
	projects.On("Get", "demo-project").Return(project.Project{ID: "demo-project"}, nil)

	tasks := new(mockTaskStore)
	stubTaskDraftSessionIDCalls(tasks)
	tasks.On("GetTaskDraftConversation", "demo-project", "session-1").
		Return(task.Conversation{Messages: []task.ConversationMessage{{Role: "assistant", Content: "already asked"}}}, nil)

	runner := new(mockAgentRunner)
	req := newProjectRequest(t, http.MethodPost, "/api/v1/projects/demo-project/task-drafts/session-1/start", stageStartRequest{})
	req.SetPathValue("projectId", "demo-project")
	req.SetPathValue("sessionId", "session-1")
	w := httptest.NewRecorder()
	(&Server{Projects: projects, Tasks: tasks, AgentRunners: map[string]agentrunner.AgentRunner{"local": runner}}).handleStartTaskDraftConversation()(w, req)

	assert.Equal(t, http.StatusConflict, w.Code)
	runner.AssertNotCalled(t, "Run", mock.Anything, mock.Anything, mock.Anything)
}

func TestHandleStartTaskDraftConversation_RunsKickoffTurnAndPersistsOnlyAssistant(t *testing.T) {
	projects := new(mockProjectStore)
	projects.On("Get", "demo-project").Return(project.Project{ID: "demo-project", Name: "Demo", Description: "A demo project"}, nil)

	tasks := new(mockTaskStore)
	stubTaskDraftSessionIDCalls(tasks)
	tasks.On("GetTaskDraftConversation", "demo-project", "session-1").Return(task.Conversation{}, nil)

	var persistedMsgs []task.ConversationMessage
	tasks.On("AppendTaskDraftConversationMessages", "demo-project", "session-1", mock.MatchedBy(func(msgs []task.ConversationMessage) bool {
		persistedMsgs = msgs
		return true
	})).Return(task.Conversation{}, nil)

	var gotIn agentrunner.RunInput
	runner := new(mockAgentRunner)
	runner.On("Run", mock.Anything, mock.MatchedBy(func(in agentrunner.RunInput) bool {
		gotIn = in
		return true
	}), mock.Anything).Return([]chat.Delta{{Content: "What should we call this task?"}}, agentrunner.RunOutput{
		Content: "What should we call this task?",
	}, nil)

	req := newProjectRequest(t, http.MethodPost, "/api/v1/projects/demo-project/task-drafts/session-1/start", stageStartRequest{})
	req.SetPathValue("projectId", "demo-project")
	req.SetPathValue("sessionId", "session-1")
	w := httptest.NewRecorder()
	(&Server{Projects: projects, Tasks: tasks, KnowledgeStore: new(mockKnowledgeStore),
		AgentRunners: map[string]agentrunner.AgentRunner{"local": runner}}).handleStartTaskDraftConversation()(w, req)

	require.Equal(t, http.StatusOK, w.Code)

	assert.Equal(t, taskDraftKickoffMessage, gotIn.UserMessage)
	assert.Equal(t, taskDraftSessionKey("demo-project", "session-1"), gotIn.SessionKey)
	require.Len(t, gotIn.Tools, 2)
	assert.Equal(t, "propose_task", gotIn.Tools[0].Function.Name)
	assert.Contains(t, gotIn.SystemPrompt, "Demo")

	require.Len(t, persistedMsgs, 1, "only the assistant's opening question is persisted, no synthetic user turn")
	assert.Equal(t, "assistant", persistedMsgs[0].Role)
	assert.Equal(t, "What should we call this task?", persistedMsgs[0].Content)
}

func TestHandlePostTaskDraftMessage_SeedsSystemPromptAndToolSchema(t *testing.T) {
	projects := new(mockProjectStore)
	projects.On("Get", "demo-project").Return(project.Project{
		ID: "demo-project", Name: "Demo", Description: "A demo project", Constraints: []string{"no new deps"},
	}, nil)

	tasks := new(mockTaskStore)
	stubTaskDraftSessionIDCalls(tasks)
	tasks.On("GetTaskDraftConversation", "demo-project", "session-1").Return(task.Conversation{}, nil)
	tasks.On("AppendTaskDraftConversationMessages", "demo-project", "session-1", mock.Anything).Return(task.Conversation{}, nil)

	var gotIn agentrunner.RunInput
	runner := new(mockAgentRunner)
	runner.On("Run", mock.Anything, mock.MatchedBy(func(in agentrunner.RunInput) bool {
		gotIn = in
		return true
	}), mock.Anything).Return(nil, agentrunner.RunOutput{Content: "Sure — what should this task accomplish?"}, nil)

	req := newProjectRequest(t, http.MethodPost, "/api/v1/projects/demo-project/task-drafts/session-1/messages", stageMessageRequest{Content: "I want a login page"})
	req.SetPathValue("projectId", "demo-project")
	req.SetPathValue("sessionId", "session-1")
	w := httptest.NewRecorder()
	(&Server{Projects: projects, Tasks: tasks, KnowledgeStore: new(mockKnowledgeStore),
		AgentRunners: map[string]agentrunner.AgentRunner{"local": runner}}).handlePostTaskDraftMessage()(w, req)

	require.Equal(t, http.StatusOK, w.Code)
	assert.Equal(t, "I want a login page", gotIn.UserMessage)
	assert.Contains(t, gotIn.SystemPrompt, "Demo")
	assert.Contains(t, gotIn.SystemPrompt, "no new deps")
	require.Len(t, gotIn.Tools, 2)
	names := []string{gotIn.Tools[0].Function.Name, gotIn.Tools[1].Function.Name}
	assert.Contains(t, names, "propose_task")
	assert.Contains(t, names, "ask_question")
}

func TestHandlePostTaskDraftMessage_StreamsToolCallAsSSEEventAndPersists(t *testing.T) {
	projects := new(mockProjectStore)
	projects.On("Get", "demo-project").Return(project.Project{ID: "demo-project"}, nil)

	tasks := new(mockTaskStore)
	stubTaskDraftSessionIDCalls(tasks)
	tasks.On("GetTaskDraftConversation", "demo-project", "session-1").Return(task.Conversation{}, nil)

	var persistedMsgs []task.ConversationMessage
	tasks.On("AppendTaskDraftConversationMessages", "demo-project", "session-1", mock.MatchedBy(func(msgs []task.ConversationMessage) bool {
		persistedMsgs = msgs
		return true
	})).Return(task.Conversation{}, nil)

	proposeArgs := `{"id":"fix-login-bug","title":"Fix login bug","objective":"ship a fix"}`
	runner := new(mockAgentRunner)
	runner.On("Run", mock.Anything, mock.Anything, mock.Anything).Return(nil, agentrunner.RunOutput{
		Content: "Here's a draft.",
		ToolCall: &chat.ToolCall{ID: "call-1", Function: chat.ToolCallFunction{Name: "propose_task", Arguments: proposeArgs}},
	}, nil)

	req := newProjectRequest(t, http.MethodPost, "/api/v1/projects/demo-project/task-drafts/session-1/messages", stageMessageRequest{Content: "that's everything"})
	req.SetPathValue("projectId", "demo-project")
	req.SetPathValue("sessionId", "session-1")
	w := httptest.NewRecorder()
	(&Server{Projects: projects, Tasks: tasks, KnowledgeStore: new(mockKnowledgeStore),
		AgentRunners: map[string]agentrunner.AgentRunner{"local": runner}}).handlePostTaskDraftMessage()(w, req)

	require.Equal(t, http.StatusOK, w.Code)

	events := parseSSEEvents(t, w.Body.String())
	require.NotEmpty(t, events)
	last := events[len(events)-1]
	require.NotNil(t, last.ToolCall)
	assert.Equal(t, "propose_task", last.ToolCall.Name)

	require.Len(t, persistedMsgs, 2)
	require.NotNil(t, persistedMsgs[1].ToolCall)
	assert.Equal(t, "propose_task", persistedMsgs[1].ToolCall.Name)
	assert.Equal(t, proposeArgs, persistedMsgs[1].ToolCall.Arguments)
}

func TestHandleDeleteTaskDraftMessage_RemovesOnlyThatMessageAndEvictsSessions(t *testing.T) {
	projects := new(mockProjectStore)
	projects.On("Get", "demo-project").Return(project.Project{ID: "demo-project"}, nil)

	tasks := new(mockTaskStore)
	stubTaskDraftSessionIDCalls(tasks)
	tasks.On("GetTaskDraftConversation", "demo-project", "session-1").Return(task.Conversation{
		Messages: []task.ConversationMessage{
			{Role: "user", Content: "first"},
			{Role: "assistant", Content: "reply"},
			{Role: "user", Content: "third"},
		},
	}, nil)

	var replaced []task.ConversationMessage
	tasks.On("ReplaceTaskDraftConversationMessages", "demo-project", "session-1", mock.MatchedBy(func(msgs []task.ConversationMessage) bool {
		replaced = msgs
		return true
	})).Return(task.Conversation{Messages: []task.ConversationMessage{
		{Role: "user", Content: "first"}, {Role: "user", Content: "third"},
	}}, nil)

	runner := new(mockAgentRunner)
	sessionKey := taskDraftSessionKey("demo-project", "session-1")
	runner.On("CloseSession", sessionKey).Return()

	req := httptest.NewRequest(http.MethodDelete, "/api/v1/projects/demo-project/task-drafts/session-1/messages/1", nil)
	req.SetPathValue("projectId", "demo-project")
	req.SetPathValue("sessionId", "session-1")
	req.SetPathValue("index", "1")
	w := httptest.NewRecorder()
	(&Server{Projects: projects, Tasks: tasks, AgentRunners: map[string]agentrunner.AgentRunner{"local": runner}}).handleDeleteTaskDraftMessage()(w, req)

	require.Equal(t, http.StatusOK, w.Code)
	require.Len(t, replaced, 2)
	assert.Equal(t, "first", replaced[0].Content)
	assert.Equal(t, "third", replaced[1].Content)
	runner.AssertCalled(t, "CloseSession", sessionKey)
}

func TestHandleRegenerateTaskDraftMessage_TruncatesHistoryEvictsSessionAndPersists(t *testing.T) {
	projects := new(mockProjectStore)
	projects.On("Get", "demo-project").Return(project.Project{ID: "demo-project"}, nil)

	tasks := new(mockTaskStore)
	stubTaskDraftSessionIDCalls(tasks)
	tasks.On("GetTaskDraftConversation", "demo-project", "session-1").Return(task.Conversation{
		Messages: []task.ConversationMessage{
			{Role: "user", Content: "earlier"},
			{Role: "assistant", Content: "earlier reply"},
			{Role: "user", Content: "original question"},
			{Role: "assistant", Content: "stale reply"},
		},
	}, nil)

	var replaced []task.ConversationMessage
	tasks.On("ReplaceTaskDraftConversationMessages", "demo-project", "session-1", mock.MatchedBy(func(msgs []task.ConversationMessage) bool {
		replaced = msgs
		return true
	})).Return(task.Conversation{}, nil)

	sessionKey := taskDraftSessionKey("demo-project", "session-1")
	var gotIn agentrunner.RunInput
	runner := new(mockAgentRunner)
	runner.On("CloseSession", sessionKey).Return()
	runner.On("Run", mock.Anything, mock.MatchedBy(func(in agentrunner.RunInput) bool {
		gotIn = in
		return true
	}), mock.Anything).Return([]chat.Delta{{Content: "fresh reply"}}, agentrunner.RunOutput{}, nil)

	req := newProjectRequest(t, http.MethodPost, "/api/v1/projects/demo-project/task-drafts/session-1/messages/2/regenerate", stageRegenerateRequest{Content: "original question"})
	req.SetPathValue("projectId", "demo-project")
	req.SetPathValue("sessionId", "session-1")
	req.SetPathValue("index", "2")
	w := httptest.NewRecorder()
	(&Server{Projects: projects, Tasks: tasks, KnowledgeStore: new(mockKnowledgeStore),
		AgentRunners: map[string]agentrunner.AgentRunner{"local": runner}}).handleRegenerateTaskDraftMessage()(w, req)

	require.Equal(t, http.StatusOK, w.Code)
	runner.AssertCalled(t, "CloseSession", sessionKey)

	require.Len(t, gotIn.History, 2)
	assert.Equal(t, "earlier", gotIn.History[0].Content)
	assert.Equal(t, "earlier reply", gotIn.History[1].Content)
	assert.Equal(t, "original question", gotIn.UserMessage)

	require.Len(t, replaced, 4)
	assert.Equal(t, "original question", replaced[2].Content)
	assert.Equal(t, "fresh reply", replaced[3].Content)
}

func TestHandleRegenerateTaskDraftMessage_RejectsNonUserIndex(t *testing.T) {
	projects := new(mockProjectStore)
	projects.On("Get", "demo-project").Return(project.Project{ID: "demo-project"}, nil)

	tasks := new(mockTaskStore)
	stubTaskDraftSessionIDCalls(tasks)
	tasks.On("GetTaskDraftConversation", "demo-project", "session-1").Return(task.Conversation{
		Messages: []task.ConversationMessage{
			{Role: "user", Content: "hi"},
			{Role: "assistant", Content: "hello"},
		},
	}, nil)

	req := newProjectRequest(t, http.MethodPost, "/api/v1/projects/demo-project/task-drafts/session-1/messages/1/regenerate", stageRegenerateRequest{Content: "hi"})
	req.SetPathValue("projectId", "demo-project")
	req.SetPathValue("sessionId", "session-1")
	req.SetPathValue("index", "1")
	w := httptest.NewRecorder()
	(&Server{Projects: projects, Tasks: tasks, KnowledgeStore: new(mockKnowledgeStore)}).handleRegenerateTaskDraftMessage()(w, req)

	assert.Equal(t, http.StatusBadRequest, w.Code)
}

// TestBuildTaskDraftContext_EmptyWhenNoDraftSessionID confirms a task
// created before this mechanism existed (or any other way with no
// DraftSessionID) gets no addendum at all — GrillMe's prompt is unchanged
// from before this feature existed.
func TestBuildTaskDraftContext_EmptyWhenNoDraftSessionID(t *testing.T) {
	s := &Server{Tasks: new(mockTaskStore)}
	addendum, err := s.buildTaskDraftContext(s.Tasks, "demo-project", task.Task{ID: "TASK-0001"})
	require.NoError(t, err)
	assert.Empty(t, addendum)
}

// TestBuildTaskDraftContext_IncludesConversationWhenSet confirms the
// pre-creation transcript is folded into the addendum when DraftSessionID
// is set and its session actually has messages.
func TestBuildTaskDraftContext_IncludesConversationWhenSet(t *testing.T) {
	tasks := new(mockTaskStore)
	stubTaskDraftSessionIDCalls(tasks)
	tasks.On("GetTaskDraftConversation", "demo-project", "session-1").Return(task.Conversation{
		Messages: []task.ConversationMessage{
			{Role: "user", Content: "I want to fix the login bug"},
			{Role: "assistant", Content: "Got it, what's the acceptance criteria?"},
		},
	}, nil)

	s := &Server{Tasks: tasks}
	addendum, err := s.buildTaskDraftContext(tasks, "demo-project", task.Task{ID: "TASK-0001", DraftSessionID: "session-1"})
	require.NoError(t, err)
	assert.Contains(t, addendum, "Pre-creation conversation")
	assert.Contains(t, addendum, "I want to fix the login bug")
	assert.Contains(t, addendum, "Got it, what's the acceptance criteria?")
}

// TestBuildTaskDraftContext_EmptyWhenSessionHasNoMessages covers a task
// whose DraftSessionID points at a session nothing was ever sent to (Create
// called before any message went out) — no addendum, not an error.
func TestBuildTaskDraftContext_EmptyWhenSessionHasNoMessages(t *testing.T) {
	tasks := new(mockTaskStore)
	stubTaskDraftSessionIDCalls(tasks)
	tasks.On("GetTaskDraftConversation", "demo-project", "session-1").Return(task.Conversation{}, nil)

	s := &Server{Tasks: tasks}
	addendum, err := s.buildTaskDraftContext(tasks, "demo-project", task.Task{ID: "TASK-0001", DraftSessionID: "session-1"})
	require.NoError(t, err)
	assert.Empty(t, addendum)
}

// TestResolveStageRun_TaskDraftAddendumSkippedWhenRejectedReviewPresent
// locks in resolveStageRun's mutual-exclusivity contract: when a rejected
// review's addendum applies, the task-draft addendum is never additionally
// appended, even if DraftSessionID happens to be set too — the risk noted
// in the implementation plan for this feature (a merge, not a branch, would
// double up on prior-context addenda).
func TestResolveStageRun_TaskDraftAddendumSkippedWhenRejectedReviewPresent(t *testing.T) {
	tasks := new(mockTaskStore)
	stubTaskDraftSessionIDCalls(tasks)
	tasks.On("ListReviews", "demo-project", "TASK-0001").Return([]task.Review{
		{ReviewID: "review-001", Decision: task.ReviewDecisionRejected, Notes: "not quite right"},
	}, nil)
	tasks.On("ListExecutions", "demo-project", "TASK-0001").Return(nil, nil)

	reposRoot, repositories := newStageMessageWorkspace(t)
	s := &Server{Tasks: tasks, ReposRoot: reposRoot, KnowledgeStore: new(mockKnowledgeStore)}

	proj := project.Project{ID: "demo-project", Repositories: repositories}
	tsk := task.Task{ID: "TASK-0001", Stage: task.StageRequirements, DraftSessionID: "session-1"}

	run, err := s.resolveStageRun(t.Context(), proj, tasks, "demo-project", tsk, task.StageRequirements, task.Conversation{})
	require.NoError(t, err)
	assert.Contains(t, run.SystemPrompt, "reopened after a rejected review")
	assert.NotContains(t, run.SystemPrompt, "Pre-creation conversation")
	tasks.AssertNotCalled(t, "GetTaskDraftConversation", mock.Anything, mock.Anything)
}

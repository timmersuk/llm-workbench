package api

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/mock"
	"github.com/stretchr/testify/require"

	"github.com/timmersuk/llm-workbench/internal/agentrunner"
	"github.com/timmersuk/llm-workbench/internal/chat"
	"github.com/timmersuk/llm-workbench/internal/gitutil"
	"github.com/timmersuk/llm-workbench/internal/knowledge"
	"github.com/timmersuk/llm-workbench/internal/project"
	"github.com/timmersuk/llm-workbench/internal/task"
)

// TestKickoffUserMessageFor_ReviewDiffersFromInterviewStages locks in that
// Review gets wording matching its own system prompt's "work through phases"
// framing rather than the Requirements/Planning "ask your first question"
// interview wording — the two were a single shared constant misapplied to
// Review until this test was added (see reviewSystemPrompt above).
func TestKickoffUserMessageFor_ReviewDiffersFromInterviewStages(t *testing.T) {
	reviewMsg := kickoffUserMessageFor(task.StageReview)
	assert.NotEqual(t, kickoffUserMessageFor(task.StageRequirements), reviewMsg)
	assert.Equal(t, kickoffUserMessageFor(task.StageRequirements), kickoffUserMessageFor(task.StagePlanning))
	assert.NotContains(t, reviewMsg, "interview")
	assert.NotContains(t, reviewMsg, "ask your first question")
}

func TestHandleGetStageConversation_InvalidStage(t *testing.T) {
	projects := new(mockProjectStore)
	projects.On("Get", "demo-project").Return(project.Project{ID: "demo-project"}, nil)

	req := httptest.NewRequest(http.MethodGet, "/api/v1/projects/demo-project/tasks/TASK-0001/stages/implementation/conversation", nil)
	req.SetPathValue("projectId", "demo-project")
	req.SetPathValue("taskId", "TASK-0001")
	req.SetPathValue("stage", "implementation")
	w := httptest.NewRecorder()
	(&Server{Projects: projects, Tasks: new(mockTaskStore)}).handleGetStageConversation()(w, req)

	assert.Equal(t, http.StatusBadRequest, w.Code)
}

func TestHandleGetStageConversation_OK(t *testing.T) {
	projects := new(mockProjectStore)
	projects.On("Get", "demo-project").Return(project.Project{ID: "demo-project"}, nil)

	tasks := new(mockTaskStore)
	stubSessionIDCalls(tasks)
	tasks.On("Get", "demo-project", "TASK-0001").Return(task.Task{ID: "TASK-0001", Stage: task.StageRequirements}, nil)
	tasks.On("GetConversation", "demo-project", "TASK-0001", task.StageRequirements).
		Return(task.Conversation{Stage: task.StageRequirements, Messages: []task.ConversationMessage{{Role: "user", Content: "hi"}}}, nil)

	req := httptest.NewRequest(http.MethodGet, "/api/v1/projects/demo-project/tasks/TASK-0001/stages/requirements/conversation", nil)
	req.SetPathValue("projectId", "demo-project")
	req.SetPathValue("taskId", "TASK-0001")
	req.SetPathValue("stage", "requirements")
	w := httptest.NewRecorder()
	(&Server{Projects: projects, Tasks: tasks}).handleGetStageConversation()(w, req)

	require.Equal(t, http.StatusOK, w.Code)
	var got task.Conversation
	require.NoError(t, json.Unmarshal(w.Body.Bytes(), &got))
	require.Len(t, got.Messages, 1)
	assert.Equal(t, "hi", got.Messages[0].Content)
}

// TestHandleGetStageConversation_AllowsNonCurrentStage locks in that this
// read no longer requires stage to match the task's current Stage (unlike
// the mutating stage-conversation handlers, which still enforce
// requireCurrentStage) — a task now at implementation must still be able to
// serve its past requirements conversation, e.g. for a timeline entry
// linking back to why an earlier stage was revisited.
func TestHandleGetStageConversation_AllowsNonCurrentStage(t *testing.T) {
	projects := new(mockProjectStore)
	projects.On("Get", "demo-project").Return(project.Project{ID: "demo-project"}, nil)

	tasks := new(mockTaskStore)
	stubSessionIDCalls(tasks)
	tasks.On("GetConversation", "demo-project", "TASK-0001", task.StageRequirements).
		Return(task.Conversation{Stage: task.StageRequirements, Messages: []task.ConversationMessage{{Role: "user", Content: "past turn"}}}, nil)

	req := httptest.NewRequest(http.MethodGet, "/api/v1/projects/demo-project/tasks/TASK-0001/stages/requirements/conversation", nil)
	req.SetPathValue("projectId", "demo-project")
	req.SetPathValue("taskId", "TASK-0001")
	req.SetPathValue("stage", "requirements")
	w := httptest.NewRecorder()
	(&Server{Projects: projects, Tasks: tasks}).handleGetStageConversation()(w, req)

	require.Equal(t, http.StatusOK, w.Code)
	tasks.AssertNotCalled(t, "Get", mock.Anything, mock.Anything)
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

func newStageMessageServer(t *testing.T, tasks *mockTaskStore, repositories []string) (ProjectStore, TaskStore) {
	t.Helper()
	projects := new(mockProjectStore)
	projects.On("Get", "demo-project").Return(project.Project{
		ID: "demo-project", Name: "Demo", Description: "A demo project",
		Constraints: []string{"no new deps"}, Repositories: repositories,
	}, nil)
	return projects, tasks
}

// newAdvisoryStageWorkspace mirrors newStageMessageWorkspace but backs
// "demo-repo" with a real git clone (proper upstream tracking, the same
// way gitutil.Clone's own tests rely on), so appendWorkspaceAdvisories'
// gitutil calls actually resolve instead of failing to "unknown" — used
// only by the tests in this file that assert on the injected advisory
// text itself. newStageMessageWorkspace's plain, non-git directory is
// deliberately left as-is for every other test in this file, so those
// keep seeing "unknown" (no injected text) and stay unaffected.
func newAdvisoryStageWorkspace(t *testing.T) (reposRoot string, repositories []string, sourceDir string) {
	t.Helper()
	sourceDir = filepath.Join(t.TempDir(), "source")
	require.NoError(t, os.MkdirAll(sourceDir, 0o755))
	gitRun(t, sourceDir, "init", "-q")
	gitRun(t, sourceDir, "config", "user.email", "t@example.com")
	gitRun(t, sourceDir, "config", "user.name", "T")
	require.NoError(t, os.WriteFile(filepath.Join(sourceDir, "README.md"), []byte("hi\n"), 0o644))
	gitRun(t, sourceDir, "add", ".")
	gitRun(t, sourceDir, "commit", "-q", "-m", "init")

	reposRoot = t.TempDir()
	require.NoError(t, gitutil.Clone(context.Background(), sourceDir, filepath.Join(reposRoot, "demo-repo")))
	return reposRoot, []string{"github.com/timmersuk/demo-repo"}, sourceDir
}

func newAdvisoryPostRequest(t *testing.T) *http.Request {
	t.Helper()
	req := newProjectRequest(t, http.MethodPost, "/api/v1/projects/demo-project/tasks/TASK-0001/stages/requirements/messages", stageMessageRequest{Content: "hi"})
	req.SetPathValue("projectId", "demo-project")
	req.SetPathValue("taskId", "TASK-0001")
	req.SetPathValue("stage", "requirements")
	return req
}

func TestHandlePostStageMessage_SystemPromptOmitsAdvisoryNotesOnCleanCheckout(t *testing.T) {
	tasks := new(mockTaskStore)
	stubSessionIDCalls(tasks)
	tasks.On("Get", "demo-project", "TASK-0001").Return(task.Task{ID: "TASK-0001", Stage: task.StageRequirements}, nil)
	tasks.On("GetConversation", "demo-project", "TASK-0001", task.StageRequirements).Return(task.Conversation{}, nil)
	tasks.On("AppendConversationMessages", "demo-project", "TASK-0001", task.StageRequirements, mock.Anything).Return(task.Conversation{}, nil)
	tasks.On("ListReviews", "demo-project", "TASK-0001").Return(nil, nil)

	reposRoot, repositories, _ := newAdvisoryStageWorkspace(t)
	projects, factory := newStageMessageServer(t, tasks, repositories)

	var gotIn agentrunner.RunInput
	runner := new(mockAgentRunner)
	runner.On("Run", mock.Anything, mock.MatchedBy(func(in agentrunner.RunInput) bool {
		gotIn = in
		return true
	}), mock.Anything).Return(nil, agentrunner.RunOutput{Content: "ok"}, nil)

	w := httptest.NewRecorder()
	(&Server{Projects: projects, Tasks: factory, KnowledgeStore: new(mockKnowledgeStore),
		AgentRunners: map[string]agentrunner.AgentRunner{"local": runner}, ReposRoot: reposRoot}).handlePostStageMessage()(w, newAdvisoryPostRequest(t))

	require.Equal(t, http.StatusOK, w.Code)
	assert.NotContains(t, gotIn.SystemPrompt, "commit(s) behind")
	assert.NotContains(t, gotIn.SystemPrompt, "uncommitted changes")
}

func TestHandlePostStageMessage_SystemPromptIncludesDirtyNote(t *testing.T) {
	tasks := new(mockTaskStore)
	stubSessionIDCalls(tasks)
	tasks.On("Get", "demo-project", "TASK-0001").Return(task.Task{ID: "TASK-0001", Stage: task.StageRequirements}, nil)
	tasks.On("GetConversation", "demo-project", "TASK-0001", task.StageRequirements).Return(task.Conversation{}, nil)
	tasks.On("AppendConversationMessages", "demo-project", "TASK-0001", task.StageRequirements, mock.Anything).Return(task.Conversation{}, nil)
	tasks.On("ListReviews", "demo-project", "TASK-0001").Return(nil, nil)

	reposRoot, repositories, _ := newAdvisoryStageWorkspace(t)
	require.NoError(t, os.WriteFile(filepath.Join(reposRoot, "demo-repo", "scratch.txt"), []byte("x\n"), 0o644))
	projects, factory := newStageMessageServer(t, tasks, repositories)

	var gotIn agentrunner.RunInput
	runner := new(mockAgentRunner)
	runner.On("Run", mock.Anything, mock.MatchedBy(func(in agentrunner.RunInput) bool {
		gotIn = in
		return true
	}), mock.Anything).Return(nil, agentrunner.RunOutput{Content: "ok"}, nil)

	w := httptest.NewRecorder()
	(&Server{Projects: projects, Tasks: factory, KnowledgeStore: new(mockKnowledgeStore),
		AgentRunners: map[string]agentrunner.AgentRunner{"local": runner}, ReposRoot: reposRoot}).handlePostStageMessage()(w, newAdvisoryPostRequest(t))

	require.Equal(t, http.StatusOK, w.Code)
	assert.Contains(t, gotIn.SystemPrompt, "uncommitted changes")
}

func TestHandlePostStageMessage_SystemPromptIncludesBehindOriginNote(t *testing.T) {
	tasks := new(mockTaskStore)
	stubSessionIDCalls(tasks)
	tasks.On("Get", "demo-project", "TASK-0001").Return(task.Task{ID: "TASK-0001", Stage: task.StageRequirements}, nil)
	tasks.On("GetConversation", "demo-project", "TASK-0001", task.StageRequirements).Return(task.Conversation{}, nil)
	tasks.On("AppendConversationMessages", "demo-project", "TASK-0001", task.StageRequirements, mock.Anything).Return(task.Conversation{}, nil)
	tasks.On("ListReviews", "demo-project", "TASK-0001").Return(nil, nil)

	reposRoot, repositories, sourceDir := newAdvisoryStageWorkspace(t)
	require.NoError(t, os.WriteFile(filepath.Join(sourceDir, "second.txt"), []byte("x\n"), 0o644))
	gitRun(t, sourceDir, "add", ".")
	gitRun(t, sourceDir, "commit", "-q", "-m", "second commit")
	projects, factory := newStageMessageServer(t, tasks, repositories)

	var gotIn agentrunner.RunInput
	runner := new(mockAgentRunner)
	runner.On("Run", mock.Anything, mock.MatchedBy(func(in agentrunner.RunInput) bool {
		gotIn = in
		return true
	}), mock.Anything).Return(nil, agentrunner.RunOutput{Content: "ok"}, nil)

	w := httptest.NewRecorder()
	(&Server{Projects: projects, Tasks: factory, KnowledgeStore: new(mockKnowledgeStore),
		AgentRunners: map[string]agentrunner.AgentRunner{"local": runner}, ReposRoot: reposRoot}).handlePostStageMessage()(w, newAdvisoryPostRequest(t))

	require.Equal(t, http.StatusOK, w.Code)
	assert.Contains(t, gotIn.SystemPrompt, "commit(s) behind")
}

// TestResolveStageRun_ReviewStageIncludesAdvisoryNoteFromSharedCheckout
// locks in appendWorkspaceAdvisories' design decision: the advisory check
// always runs against the project's shared checkout (proj.Repositories),
// not whatever workspace a given stage actually resolved to — a Review
// conversation's own run.Workspace is the isolated execution worktree
// (ResolveReviewWorkspace), which stays clean here, yet the note must
// still appear because the *shared* checkout is dirty.
func TestResolveStageRun_ReviewStageIncludesAdvisoryNoteFromSharedCheckout(t *testing.T) {
	reposRoot := t.TempDir()
	initReviewRepo(t, reposRoot) // shared checkout "myrepo" (branch "main") + exec-001 worktree

	require.NoError(t, os.WriteFile(filepath.Join(reposRoot, "myrepo", "scratch.txt"), []byte("x\n"), 0o644))

	store := task.NewFileStore(t.TempDir())
	seedReviewableTask(t, store, "task-a")
	tk, err := store.Get("demo-project", "task-a")
	require.NoError(t, err)

	proj := project.Project{Repositories: []string{"github.com/x/myrepo"}, DefaultBranch: "main"}
	run, err := (&Server{ReposRoot: reposRoot, KnowledgeStore: new(mockKnowledgeStore), Projects: new(mockProjectStore)}).resolveStageRun(context.Background(), proj, store, "demo-project", tk, task.StageReview, task.Conversation{})
	require.NoError(t, err)
	assert.Contains(t, run.SystemPrompt, "uncommitted changes")
}

func TestHandleStartStageConversation_InvalidStage(t *testing.T) {
	req := newProjectRequest(t, http.MethodPost, "/api/v1/projects/demo-project/tasks/TASK-0001/stages/implementation/start", stageStartRequest{})
	req.SetPathValue("projectId", "demo-project")
	req.SetPathValue("taskId", "TASK-0001")
	req.SetPathValue("stage", "implementation")
	w := httptest.NewRecorder()
	(&Server{Projects: new(mockProjectStore), Tasks: new(mockTaskStore), KnowledgeStore: new(mockKnowledgeStore)}).handleStartStageConversation()(w, req)

	assert.Equal(t, http.StatusBadRequest, w.Code)
}

func TestHandleStartStageConversation_UnknownExecutor(t *testing.T) {
	req := newProjectRequest(t, http.MethodPost, "/api/v1/projects/demo-project/tasks/TASK-0001/stages/requirements/start", stageStartRequest{Executor: "does-not-exist"})
	req.SetPathValue("projectId", "demo-project")
	req.SetPathValue("taskId", "TASK-0001")
	req.SetPathValue("stage", "requirements")
	w := httptest.NewRecorder()
	(&Server{Projects: new(mockProjectStore), Tasks: new(mockTaskStore), KnowledgeStore: new(mockKnowledgeStore)}).handleStartStageConversation()(w, req)

	assert.Equal(t, http.StatusBadRequest, w.Code)
}

// TestHandleStartStageConversation_StageMismatch locks in the URL/actual-stage
// guard (docs/milestones/done/milestone7.md PR 5): a task already at implementation
// must not start a fresh requirements conversation just because the URL
// names a valid Conversation stage.
func TestHandleStartStageConversation_StageMismatch(t *testing.T) {
	tasks := new(mockTaskStore)
	stubSessionIDCalls(tasks)
	tasks.On("Get", "demo-project", "TASK-0001").Return(task.Task{ID: "TASK-0001", Stage: task.StageImplementation}, nil)

	runner := new(mockAgentRunner)
	reposRoot, repositories := newStageMessageWorkspace(t)
	projects, factory := newStageMessageServer(t, tasks, repositories)

	req := newProjectRequest(t, http.MethodPost, "/api/v1/projects/demo-project/tasks/TASK-0001/stages/requirements/start", stageStartRequest{})
	req.SetPathValue("projectId", "demo-project")
	req.SetPathValue("taskId", "TASK-0001")
	req.SetPathValue("stage", "requirements")
	w := httptest.NewRecorder()
	(&Server{Projects: projects, Tasks: factory, KnowledgeStore: new(mockKnowledgeStore),
		AgentRunners: map[string]agentrunner.AgentRunner{"local": runner}, ReposRoot: reposRoot}).handleStartStageConversation()(w, req)

	assert.Equal(t, http.StatusConflict, w.Code)
	runner.AssertNotCalled(t, "Run", mock.Anything, mock.Anything, mock.Anything)
}

// TestHandleStartStageConversation_RejectsWhenAlreadyStarted locks in the
// 409 guard: starting is only meaningful once, before any real exchange —
// a conversation that already has messages must continue via
// handlePostStageMessage instead.
func TestHandleStartStageConversation_RejectsWhenAlreadyStarted(t *testing.T) {
	tasks := new(mockTaskStore)
	stubSessionIDCalls(tasks)
	tasks.On("Get", "demo-project", "TASK-0001").Return(task.Task{ID: "TASK-0001", Stage: task.StageRequirements}, nil)
	tasks.On("GetConversation", "demo-project", "TASK-0001", task.StageRequirements).Return(task.Conversation{
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
	(&Server{Projects: projects, Tasks: factory, KnowledgeStore: new(mockKnowledgeStore),
		AgentRunners: map[string]agentrunner.AgentRunner{"local": runner}, ReposRoot: reposRoot}).handleStartStageConversation()(w, req)

	assert.Equal(t, http.StatusConflict, w.Code)
	runner.AssertNotCalled(t, "Run", mock.Anything, mock.Anything, mock.Anything)
}

// TestHandleStartStageConversation_RunsKickoffTurnAndPersistsOnlyAssistant
// locks in the core behavior: an empty conversation gets one agent turn
// seeded with kickoffUserMessageFor(stage) (not the empty stageStartRequest
// content — there is none), and only the resulting assistant message is persisted,
// with no synthetic "user" turn alongside it.
func TestHandleStartStageConversation_RunsKickoffTurnAndPersistsOnlyAssistant(t *testing.T) {
	tasks := new(mockTaskStore)
	stubSessionIDCalls(tasks)
	tasks.On("Get", "demo-project", "TASK-0001").Return(task.Task{ID: "TASK-0001", Stage: task.StageRequirements, Objective: "ship login"}, nil)
	tasks.On("GetConversation", "demo-project", "TASK-0001", task.StageRequirements).Return(task.Conversation{Stage: task.StageRequirements}, nil)
	tasks.On("ListReviews", "demo-project", "TASK-0001").Return(nil, nil)

	var persistedMsgs []task.ConversationMessage
	tasks.On("AppendConversationMessages", "demo-project", "TASK-0001", task.StageRequirements, mock.MatchedBy(func(msgs []task.ConversationMessage) bool {
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
	(&Server{Projects: projects, Tasks: factory, KnowledgeStore: new(mockKnowledgeStore),
		AgentRunners: map[string]agentrunner.AgentRunner{"local": runner}, ReposRoot: reposRoot}).handleStartStageConversation()(w, req)

	require.Equal(t, http.StatusOK, w.Code)

	assert.Equal(t, kickoffUserMessageFor(task.StageRequirements), gotIn.UserMessage)
	assert.Equal(t, proposeContextToolName, gotIn.Tools[0].Function.Name)
	assert.Contains(t, gotIn.SystemPrompt, "ship login")

	events := parseSSEEvents(t, w.Body.String())
	require.Len(t, events, 1)
	assert.Equal(t, "What's the objective?", events[0].Content)

	require.Len(t, persistedMsgs, 1, "only the assistant's opening question is persisted, no synthetic user turn")
	assert.Equal(t, "assistant", persistedMsgs[0].Role)
	assert.Equal(t, "What's the objective?", persistedMsgs[0].Content)
}

func TestHandlePostStageMessage_InvalidStage(t *testing.T) {
	req := newProjectRequest(t, http.MethodPost, "/api/v1/projects/demo-project/tasks/TASK-0001/stages/implementation/messages", stageMessageRequest{Content: "hi"})
	req.SetPathValue("projectId", "demo-project")
	req.SetPathValue("taskId", "TASK-0001")
	req.SetPathValue("stage", "implementation")
	w := httptest.NewRecorder()
	(&Server{Projects: new(mockProjectStore), Tasks: new(mockTaskStore), KnowledgeStore: new(mockKnowledgeStore)}).handlePostStageMessage()(w, req)

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
	(&Server{Projects: projects, Tasks: new(mockTaskStore), KnowledgeStore: new(mockKnowledgeStore),
		AgentRunners: map[string]agentrunner.AgentRunner{"local": new(mockAgentRunner)}}).handlePostStageMessage()(w, req)

	assert.Equal(t, http.StatusBadRequest, w.Code)
}

// TestHandlePostStageMessage_StageMismatch locks in the URL/actual-stage
// guard (docs/milestones/done/milestone7.md PR 5): a task already at
// implementation must not accept a new requirements-stage turn just because
// the URL names a valid Conversation stage.
func TestHandlePostStageMessage_StageMismatch(t *testing.T) {
	tasks := new(mockTaskStore)
	stubSessionIDCalls(tasks)
	tasks.On("Get", "demo-project", "TASK-0001").Return(task.Task{ID: "TASK-0001", Stage: task.StageImplementation}, nil)

	runner := new(mockAgentRunner)
	reposRoot, repositories := newStageMessageWorkspace(t)
	projects, factory := newStageMessageServer(t, tasks, repositories)

	req := newProjectRequest(t, http.MethodPost, "/api/v1/projects/demo-project/tasks/TASK-0001/stages/requirements/messages", stageMessageRequest{Content: "hi"})
	req.SetPathValue("projectId", "demo-project")
	req.SetPathValue("taskId", "TASK-0001")
	req.SetPathValue("stage", "requirements")
	w := httptest.NewRecorder()
	(&Server{Projects: projects, Tasks: factory, KnowledgeStore: new(mockKnowledgeStore),
		AgentRunners: map[string]agentrunner.AgentRunner{"local": runner}, ReposRoot: reposRoot}).handlePostStageMessage()(w, req)

	assert.Equal(t, http.StatusConflict, w.Code)
	runner.AssertNotCalled(t, "Run", mock.Anything, mock.Anything, mock.Anything)
}

func TestHandlePostStageMessage_SeedsSystemPromptAndToolSchema(t *testing.T) {
	tasks := new(mockTaskStore)
	stubSessionIDCalls(tasks)
	tasks.On("Get", "demo-project", "TASK-0001").Return(task.Task{
		ID: "TASK-0001", Stage: task.StageRequirements, Objective: "ship login", Constraints: []string{"must use existing auth service"},
		References: task.References{Knowledge: []string{"team/style"}},
	}, nil)
	tasks.On("GetConversation", "demo-project", "TASK-0001", task.StageRequirements).Return(task.Conversation{}, nil)
	tasks.On("AppendConversationMessages", "demo-project", "TASK-0001", task.StageRequirements, mock.Anything).Return(task.Conversation{}, nil)
	tasks.On("ListReviews", "demo-project", "TASK-0001").Return(nil, nil)

	knowledgeReader := new(mockKnowledgeStore)
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

	req := newProjectRequest(t, http.MethodPost, "/api/v1/projects/demo-project/tasks/TASK-0001/stages/requirements/messages", stageMessageRequest{Content: "let's start"})
	req.SetPathValue("projectId", "demo-project")
	req.SetPathValue("taskId", "TASK-0001")
	req.SetPathValue("stage", "requirements")
	w := httptest.NewRecorder()
	(&Server{Projects: projects, Tasks: tasks, KnowledgeStore: knowledgeReader,
		AgentRunners: map[string]agentrunner.AgentRunner{"local": runner}, ReposRoot: reposRoot}).handlePostStageMessage()(w, req)

	require.Equal(t, http.StatusOK, w.Code)

	assert.Equal(t, proposeContextToolName, gotIn.Tools[0].Function.Name)
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
	stubSessionIDCalls(tasks)
	tasks.On("Get", "demo-project", "TASK-0001").Return(task.Task{ID: "TASK-0001", Stage: task.StageRequirements}, nil)
	tasks.On("GetConversation", "demo-project", "TASK-0001", task.StageRequirements).Return(task.Conversation{
		Stage: task.StageRequirements,
		Messages: []task.ConversationMessage{
			{Role: "user", Content: "let's start"},
			{Role: "assistant", Content: "sure", ToolCall: &task.ConversationToolCall{
				ID: "call-0", Name: proposeContextToolName, Arguments: `{"objective":"draft one"}`,
			}},
		},
	}, nil)
	tasks.On("AppendConversationMessages", "demo-project", "TASK-0001", task.StageRequirements, mock.Anything).Return(task.Conversation{}, nil)
	tasks.On("ListReviews", "demo-project", "TASK-0001").Return(nil, nil)

	knowledgeReader := new(mockKnowledgeStore)

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
	(&Server{Projects: projects, Tasks: factory, KnowledgeStore: knowledgeReader,
		AgentRunners: map[string]agentrunner.AgentRunner{"local": runner}, ReposRoot: reposRoot}).handlePostStageMessage()(w, req)

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
	stubSessionIDCalls(tasks)
	tasks.On("Get", "demo-project", "TASK-0001").Return(task.Task{ID: "TASK-0001", Stage: task.StagePlanning}, nil)
	tasks.On("GetConversation", "demo-project", "TASK-0001", task.StagePlanning).Return(task.Conversation{}, nil)
	tasks.On("AppendConversationMessages", "demo-project", "TASK-0001", task.StagePlanning, mock.Anything).Return(task.Conversation{}, nil)

	knowledgeReader := new(mockKnowledgeStore)

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
	(&Server{Projects: projects, Tasks: factory, KnowledgeStore: knowledgeReader,
		AgentRunners: map[string]agentrunner.AgentRunner{"local": runner}, ReposRoot: reposRoot}).handlePostStageMessage()(w, req)

	require.Equal(t, http.StatusOK, w.Code)
	assert.Equal(t, proposePlanToolName, gotIn.Tools[0].Function.Name)
}

// TestHandlePostStageMessage_StreamsToolCallAsSSEEventAndPersists exercises
// the default ("" -> "local") executor path — the analogous
// TestHandlePostStageMessage_AgentExecutorStreamsToolCallAsSSEEventAndPersists
// below covers an explicitly-selected executor via the identical code path.
func TestHandlePostStageMessage_StreamsToolCallAsSSEEventAndPersists(t *testing.T) {
	tasks := new(mockTaskStore)
	stubSessionIDCalls(tasks)
	tasks.On("Get", "demo-project", "TASK-0001").Return(task.Task{ID: "TASK-0001", Stage: task.StageRequirements}, nil)
	tasks.On("GetConversation", "demo-project", "TASK-0001", task.StageRequirements).Return(task.Conversation{}, nil)

	var persistedMsgs []task.ConversationMessage
	tasks.On("AppendConversationMessages", "demo-project", "TASK-0001", task.StageRequirements, mock.MatchedBy(func(msgs []task.ConversationMessage) bool {
		persistedMsgs = msgs
		return true
	})).Return(task.Conversation{}, nil)
	tasks.On("ListReviews", "demo-project", "TASK-0001").Return(nil, nil)

	knowledgeReader := new(mockKnowledgeStore)

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
	(&Server{Projects: projects, Tasks: factory, KnowledgeStore: knowledgeReader,
		AgentRunners: map[string]agentrunner.AgentRunner{"local": runner}, ReposRoot: reposRoot}).handlePostStageMessage()(w, req)

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

// TestHandlePostStageMessage_StreamsAndPersistsBothDraftsFromOneTurn locks
// in the fix for a real bug: a stage that offers more than one Draft-
// proposing tool at once (Requirements offers propose_context +
// ask_question; Review offers propose_review + propose_knowledge the same
// way) can have the model call more than one of them in the same turn —
// previously only the first ever survived past agentrunner into the
// persisted conversation, the other silently vanishing with no trace
// (confirmed against a real Review transcript where the assistant's own
// text said "I've proposed needs_changes" but no propose_review tool_call
// was ever recorded, because propose_knowledge had been resolved first).
// Exercised here against Requirements — same stageTool/runStageTurn
// machinery Review uses, without Review's git-worktree resolution — both
// calls must be streamed as separate SSE ToolCall events and both
// persisted on the one assistant message.
func TestHandlePostStageMessage_StreamsAndPersistsBothDraftsFromOneTurn(t *testing.T) {
	tasks := new(mockTaskStore)
	stubSessionIDCalls(tasks)
	tasks.On("Get", "demo-project", "TASK-0001").Return(task.Task{ID: "TASK-0001", Stage: task.StageRequirements}, nil)
	tasks.On("GetConversation", "demo-project", "TASK-0001", task.StageRequirements).Return(task.Conversation{}, nil)

	var persistedMsgs []task.ConversationMessage
	tasks.On("AppendConversationMessages", "demo-project", "TASK-0001", task.StageRequirements, mock.MatchedBy(func(msgs []task.ConversationMessage) bool {
		persistedMsgs = msgs
		return true
	})).Return(task.Conversation{}, nil)
	tasks.On("ListReviews", "demo-project", "TASK-0001").Return(nil, nil)

	knowledgeReader := new(mockKnowledgeStore)

	runner := new(mockAgentRunner)
	runner.On("Run", mock.Anything, mock.Anything, mock.Anything).Return([]chat.Delta{
		{Content: "Here's my proposal, and a quick check: "},
	}, agentrunner.RunOutput{
		Content: "Here's my proposal, and a quick check: ",
		ToolCall: &chat.ToolCall{ID: "call-1", Type: "function", Function: chat.ToolCallFunction{
			Name: proposeContextToolName, Arguments: `{"objective":"ship login"}`,
		}},
		ToolCalls: []chat.ToolCall{
			{ID: "call-1", Type: "function", Function: chat.ToolCallFunction{
				Name: proposeContextToolName, Arguments: `{"objective":"ship login"}`,
			}},
			{ID: "call-2", Type: "function", Function: chat.ToolCallFunction{
				Name: askQuestionToolName, Arguments: `{"options":["yes","no"]}`,
			}},
		},
	}, nil)

	reposRoot, repositories := newStageMessageWorkspace(t)
	projects, factory := newStageMessageServer(t, tasks, repositories)

	req := newProjectRequest(t, http.MethodPost, "/api/v1/projects/demo-project/tasks/TASK-0001/stages/requirements/messages", stageMessageRequest{Content: "let's start"})
	req.SetPathValue("projectId", "demo-project")
	req.SetPathValue("taskId", "TASK-0001")
	req.SetPathValue("stage", "requirements")
	w := httptest.NewRecorder()
	(&Server{Projects: projects, Tasks: factory, KnowledgeStore: knowledgeReader,
		AgentRunners: map[string]agentrunner.AgentRunner{"local": runner}, ReposRoot: reposRoot}).handlePostStageMessage()(w, req)

	require.Equal(t, http.StatusOK, w.Code)

	events := parseSSEEvents(t, w.Body.String())
	var toolCallEvents []chatToolCallEvent
	for _, ev := range events {
		if ev.ToolCall != nil {
			toolCallEvents = append(toolCallEvents, *ev.ToolCall)
		}
	}
	require.Len(t, toolCallEvents, 2, "both drafts must be streamed, not just the first")
	assert.Equal(t, proposeContextToolName, toolCallEvents[0].Name)
	assert.Equal(t, askQuestionToolName, toolCallEvents[1].Name)

	require.Len(t, persistedMsgs, 2)
	assistantMsg := persistedMsgs[1]
	require.Len(t, assistantMsg.ToolCalls, 2, "both drafts must be persisted, not just the first")
	assert.Equal(t, proposeContextToolName, assistantMsg.ToolCalls[0].Name)
	assert.Equal(t, askQuestionToolName, assistantMsg.ToolCalls[1].Name)
	require.NotNil(t, assistantMsg.ToolCall)
	assert.Equal(t, proposeContextToolName, assistantMsg.ToolCall.Name, "singular ToolCall stays the first, for back-compat")
}

// TestHandlePostStageMessage_PersistsToolActivityOnAssistantMessage locks in
// docs/adr/0018's persistence half: the turn's intermediate tool activity
// (surfaced live as SSE ToolActivity events, same as
// TestHandlePostStageMessage_StreamsToolCallAsSSEEventAndPersists's Draft
// ToolCall) must also land on the persisted assistant message's
// ToolActivity field, call paired with its result, so reopening the
// conversation later still shows what the agent did.
func TestHandlePostStageMessage_PersistsToolActivityOnAssistantMessage(t *testing.T) {
	tasks := new(mockTaskStore)
	stubSessionIDCalls(tasks)
	tasks.On("Get", "demo-project", "TASK-0001").Return(task.Task{ID: "TASK-0001", Stage: task.StageRequirements}, nil)
	tasks.On("GetConversation", "demo-project", "TASK-0001", task.StageRequirements).Return(task.Conversation{}, nil)

	var persistedMsgs []task.ConversationMessage
	tasks.On("AppendConversationMessages", "demo-project", "TASK-0001", task.StageRequirements, mock.MatchedBy(func(msgs []task.ConversationMessage) bool {
		persistedMsgs = msgs
		return true
	})).Return(task.Conversation{}, nil)
	tasks.On("ListReviews", "demo-project", "TASK-0001").Return(nil, nil)

	knowledgeReader := new(mockKnowledgeStore)

	runner := new(mockAgentRunner)
	runner.On("Run", mock.Anything, mock.MatchedBy(func(in agentrunner.RunInput) bool {
		in.OnToolCall("call-1", "Read", `{"path":"a.go"}`)
		in.OnToolResult("call-1", "Read", "package main", false)
		in.OnToolCall("call-2", "Grep", `{"pattern":"TODO"}`)
		in.OnToolResult("call-2", "Grep", "no matches", true)
		return true
	}), mock.Anything).Return([]chat.Delta{{Content: "done looking"}}, agentrunner.RunOutput{Content: "done looking"}, nil)

	reposRoot, repositories := newStageMessageWorkspace(t)
	projects, factory := newStageMessageServer(t, tasks, repositories)

	req := newProjectRequest(t, http.MethodPost, "/api/v1/projects/demo-project/tasks/TASK-0001/stages/requirements/messages", stageMessageRequest{Content: "what's here?"})
	req.SetPathValue("projectId", "demo-project")
	req.SetPathValue("taskId", "TASK-0001")
	req.SetPathValue("stage", "requirements")
	w := httptest.NewRecorder()
	(&Server{Projects: projects, Tasks: factory, KnowledgeStore: knowledgeReader,
		AgentRunners: map[string]agentrunner.AgentRunner{"local": runner}, ReposRoot: reposRoot}).handlePostStageMessage()(w, req)

	require.Equal(t, http.StatusOK, w.Code)

	require.Len(t, persistedMsgs, 2)
	assistant := persistedMsgs[1]
	require.Len(t, assistant.ToolActivity, 2)
	assert.Equal(t, "Read", assistant.ToolActivity[0].Name)
	assert.JSONEq(t, `{"path":"a.go"}`, assistant.ToolActivity[0].Arguments)
	assert.Equal(t, "package main", assistant.ToolActivity[0].Result)
	assert.False(t, assistant.ToolActivity[0].IsError)
	assert.Equal(t, "Grep", assistant.ToolActivity[1].Name)
	assert.Equal(t, "no matches", assistant.ToolActivity[1].Result)
	assert.True(t, assistant.ToolActivity[1].IsError)
}

// TestHandlePostStageMessage_PersistsSegmentsInRealInterleavedOrder locks in
// docs/adr/0023: a turn that narrates between tool calls must persist that
// real chronological order (text, tools, text, tools, text), not the old
// bundled "all tool activity, then all text" shape — Content/ToolActivity
// stay derived from Segments, in the same order.
func TestHandlePostStageMessage_PersistsSegmentsInRealInterleavedOrder(t *testing.T) {
	tasks := new(mockTaskStore)
	stubSessionIDCalls(tasks)
	tasks.On("Get", "demo-project", "TASK-0001").Return(task.Task{ID: "TASK-0001", Stage: task.StageRequirements}, nil)
	tasks.On("GetConversation", "demo-project", "TASK-0001", task.StageRequirements).Return(task.Conversation{}, nil)

	var persistedMsgs []task.ConversationMessage
	tasks.On("AppendConversationMessages", "demo-project", "TASK-0001", task.StageRequirements, mock.MatchedBy(func(msgs []task.ConversationMessage) bool {
		persistedMsgs = msgs
		return true
	})).Return(task.Conversation{}, nil)
	tasks.On("ListReviews", "demo-project", "TASK-0001").Return(nil, nil)

	knowledgeReader := new(mockKnowledgeStore)

	runner := new(mockAgentRunner)
	runner.On("Run", mock.Anything, mock.Anything, mock.Anything).
		Run(func(args mock.Arguments) {
			in := args.Get(1).(agentrunner.RunInput)
			onDelta := args.Get(2).(func(chat.Delta) error)
			// Drive text and tool calls in a real interleaved order — not
			// "all tools then all text" — to prove runStageTurn records
			// what actually happened, not a flattened summary.
			require.NoError(t, onDelta(chat.Delta{Content: "build passes, now testing"}))
			in.OnToolCall("call-1", "Bash", `{"command":"go test ./..."}`)
			in.OnToolResult("call-1", "Bash", "ok", false)
			require.NoError(t, onDelta(chat.Delta{Content: "tests pass, now checking frontend"}))
			in.OnToolCall("call-2", "Bash", `{"command":"npm test"}`)
			in.OnToolResult("call-2", "Bash", "ok", false)
			require.NoError(t, onDelta(chat.Delta{Content: "all green"}))
		}).
		Return(nil, agentrunner.RunOutput{}, nil)

	reposRoot, repositories := newStageMessageWorkspace(t)
	projects, factory := newStageMessageServer(t, tasks, repositories)

	req := newProjectRequest(t, http.MethodPost, "/api/v1/projects/demo-project/tasks/TASK-0001/stages/requirements/messages", stageMessageRequest{Content: "run the checks"})
	req.SetPathValue("projectId", "demo-project")
	req.SetPathValue("taskId", "TASK-0001")
	req.SetPathValue("stage", "requirements")
	w := httptest.NewRecorder()
	(&Server{Projects: projects, Tasks: factory, KnowledgeStore: knowledgeReader,
		AgentRunners: map[string]agentrunner.AgentRunner{"local": runner}, ReposRoot: reposRoot}).handlePostStageMessage()(w, req)

	require.Equal(t, http.StatusOK, w.Code)

	require.Len(t, persistedMsgs, 2)
	assistant := persistedMsgs[1]

	require.Len(t, assistant.Segments, 5)
	assert.Equal(t, task.SegmentKindText, assistant.Segments[0].Kind)
	assert.Equal(t, "build passes, now testing", assistant.Segments[0].Text)
	assert.Equal(t, task.SegmentKindTools, assistant.Segments[1].Kind)
	require.Len(t, assistant.Segments[1].ToolActivity, 1)
	assert.Equal(t, "Bash", assistant.Segments[1].ToolActivity[0].Name)
	assert.Equal(t, task.SegmentKindText, assistant.Segments[2].Kind)
	assert.Equal(t, "tests pass, now checking frontend", assistant.Segments[2].Text)
	assert.Equal(t, task.SegmentKindTools, assistant.Segments[3].Kind)
	require.Len(t, assistant.Segments[3].ToolActivity, 1)
	assert.Equal(t, `{"command":"npm test"}`, assistant.Segments[3].ToolActivity[0].Arguments)
	assert.Equal(t, task.SegmentKindText, assistant.Segments[4].Kind)
	assert.Equal(t, "all green", assistant.Segments[4].Text)

	// Content/ToolActivity are still populated, derived from Segments in
	// the same order — nothing reads them any differently than before.
	assert.Equal(t, "build passes, now testingtests pass, now checking frontendall green", assistant.Content)
	require.Len(t, assistant.ToolActivity, 2)
	assert.Equal(t, "Bash", assistant.ToolActivity[0].Name)
	assert.Equal(t, `{"command":"go test ./..."}`, assistant.ToolActivity[0].Arguments)
	assert.Equal(t, `{"command":"npm test"}`, assistant.ToolActivity[1].Arguments)
}

// TestHandlePostStageMessage_AttachesResultsByIDNotArrivalOrder proves
// runStageTurn no longer assumes results arrive in the same order their
// calls were declared — a provider (the claude CLI, for parallel read-only
// tool calls) can declare several calls before any of their results
// return. Two calls are declared back-to-back before either result
// arrives, and the results are delivered in REVERSE order — the old "fill
// in whichever entry is currently last" heuristic would attach both
// results to the wrong call; id-keyed correlation must attach each to its
// real one regardless.
func TestHandlePostStageMessage_AttachesResultsByIDNotArrivalOrder(t *testing.T) {
	tasks := new(mockTaskStore)
	stubSessionIDCalls(tasks)
	tasks.On("Get", "demo-project", "TASK-0001").Return(task.Task{ID: "TASK-0001", Stage: task.StageRequirements}, nil)
	tasks.On("GetConversation", "demo-project", "TASK-0001", task.StageRequirements).Return(task.Conversation{}, nil)

	var persistedMsgs []task.ConversationMessage
	tasks.On("AppendConversationMessages", "demo-project", "TASK-0001", task.StageRequirements, mock.MatchedBy(func(msgs []task.ConversationMessage) bool {
		persistedMsgs = msgs
		return true
	})).Return(task.Conversation{}, nil)
	tasks.On("ListReviews", "demo-project", "TASK-0001").Return(nil, nil)

	knowledgeReader := new(mockKnowledgeStore)

	runner := new(mockAgentRunner)
	runner.On("Run", mock.Anything, mock.Anything, mock.Anything).
		Run(func(args mock.Arguments) {
			in := args.Get(1).(agentrunner.RunInput)
			// Both calls declared before either result — the exact shape a
			// batching provider produces and a strictly-alternating mock
			// never exercises.
			in.OnToolCall("call-A", "Read", `{"path":"a.go"}`)
			in.OnToolCall("call-B", "Grep", `{"pattern":"TODO"}`)
			// Results arrive in REVERSE declaration order.
			in.OnToolResult("call-B", "Grep", "no matches", false)
			in.OnToolResult("call-A", "Read", "package main", false)
		}).
		Return(nil, agentrunner.RunOutput{}, nil)

	reposRoot, repositories := newStageMessageWorkspace(t)
	projects, factory := newStageMessageServer(t, tasks, repositories)

	req := newProjectRequest(t, http.MethodPost, "/api/v1/projects/demo-project/tasks/TASK-0001/stages/requirements/messages", stageMessageRequest{Content: "what's here?"})
	req.SetPathValue("projectId", "demo-project")
	req.SetPathValue("taskId", "TASK-0001")
	req.SetPathValue("stage", "requirements")
	w := httptest.NewRecorder()
	(&Server{Projects: projects, Tasks: factory, KnowledgeStore: knowledgeReader,
		AgentRunners: map[string]agentrunner.AgentRunner{"local": runner}, ReposRoot: reposRoot}).handlePostStageMessage()(w, req)

	require.Equal(t, http.StatusOK, w.Code)

	require.Len(t, persistedMsgs, 2)
	assistant := persistedMsgs[1]
	require.Len(t, assistant.ToolActivity, 2)
	assert.Equal(t, "Read", assistant.ToolActivity[0].Name)
	assert.Equal(t, "package main", assistant.ToolActivity[0].Result, "call-A's result must attach to call-A, not to whichever call was declared last")
	assert.Equal(t, "Grep", assistant.ToolActivity[1].Name)
	assert.Equal(t, "no matches", assistant.ToolActivity[1].Result, "call-B's result must attach to call-B")
}

// TestHandlePostStageMessage_IgnoresMismatchedToolCallName covers a model
// hallucinating a tool call for a tool that was never offered (e.g. a local
// OpenAI-compatible model emitting a "Glob" tool_calls delta when only
// propose_context was in the request's Tools array) — it must not be
// surfaced as a Draft proposal or persisted.
func TestHandlePostStageMessage_IgnoresMismatchedToolCallName(t *testing.T) {
	tasks := new(mockTaskStore)
	stubSessionIDCalls(tasks)
	tasks.On("Get", "demo-project", "TASK-0001").Return(task.Task{ID: "TASK-0001", Stage: task.StageRequirements}, nil)
	tasks.On("GetConversation", "demo-project", "TASK-0001", task.StageRequirements).Return(task.Conversation{}, nil)

	var persistedMsgs []task.ConversationMessage
	tasks.On("AppendConversationMessages", "demo-project", "TASK-0001", task.StageRequirements, mock.MatchedBy(func(msgs []task.ConversationMessage) bool {
		persistedMsgs = msgs
		return true
	})).Return(task.Conversation{}, nil)
	tasks.On("ListReviews", "demo-project", "TASK-0001").Return(nil, nil)

	knowledgeReader := new(mockKnowledgeStore)

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
	(&Server{Projects: projects, Tasks: factory, KnowledgeStore: knowledgeReader,
		AgentRunners: map[string]agentrunner.AgentRunner{"local": runner}, ReposRoot: reposRoot}).handlePostStageMessage()(w, req)

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
	(&Server{Projects: new(mockProjectStore), Tasks: new(mockTaskStore), KnowledgeStore: new(mockKnowledgeStore)}).handlePostStageMessage()(w, req)

	assert.Equal(t, http.StatusBadRequest, w.Code)
}

func TestHandlePostStageMessage_AgentExecutorStreamsToolCallAsSSEEventAndPersists(t *testing.T) {
	reposRoot := t.TempDir()
	require.NoError(t, os.Mkdir(filepath.Join(reposRoot, "logthing"), 0o755))

	tasks := new(mockTaskStore)
	stubSessionIDCalls(tasks)
	tasks.On("Get", "demo-project", "TASK-0001").Return(task.Task{ID: "TASK-0001", Stage: task.StagePlanning}, nil)
	tasks.On("GetConversation", "demo-project", "TASK-0001", task.StagePlanning).Return(task.Conversation{}, nil)

	var persistedMsgs []task.ConversationMessage
	tasks.On("AppendConversationMessages", "demo-project", "TASK-0001", task.StagePlanning, mock.MatchedBy(func(msgs []task.ConversationMessage) bool {
		persistedMsgs = msgs
		return true
	})).Return(task.Conversation{}, nil)

	knowledgeReader := new(mockKnowledgeStore)

	projects := new(mockProjectStore)
	projects.On("Get", "demo-project").Return(project.Project{
		ID: "demo-project", Name: "Demo", Repositories: []string{"github.com/timmersuk/logthing"},
	}, nil)

	runner := new(mockAgentRunner)
	runner.On("Run", mock.Anything, mock.MatchedBy(func(in agentrunner.RunInput) bool {
		return in.SessionKey == "TASK-0001:"+task.StagePlanning &&
			in.Workspace == filepath.Join(reposRoot, "logthing") && in.UserMessage == "go ahead" &&
			len(in.Tools) == 2 && in.Tools[0].Function.Name == proposePlanToolName && in.Tools[1].Function.Name == askQuestionToolName
	}), mock.Anything).Return([]chat.Delta{{Content: "thinking..."}, {Content: "here's the plan"}}, agentrunner.RunOutput{
		// Content is deliberately NOT "thinking...here's the plan" here:
		// runStageTurn no longer trusts RunOutput.Content (docs/adr/0023),
		// it derives persisted content from what was actually streamed via
		// onDelta above — this field is unused by the code under test, left
		// different on purpose so a future reader doesn't mistake it for
		// the source of truth.
		Content: "here's the plan (stale, unused)",
		ToolCall: &chat.ToolCall{ID: "call-1", Type: "function", Function: chat.ToolCallFunction{
			Name: proposePlanToolName, Arguments: `{"approach":"port logthing"}`,
		}},
	}, nil)

	req := newProjectRequest(t, http.MethodPost, "/api/v1/projects/demo-project/tasks/TASK-0001/stages/planning/messages", stageMessageRequest{Content: "go ahead", Executor: "claude-code", Effort: "medium"})
	req.SetPathValue("projectId", "demo-project")
	req.SetPathValue("taskId", "TASK-0001")
	req.SetPathValue("stage", "planning")
	w := httptest.NewRecorder()
	(&Server{Projects: projects, Tasks: tasks, KnowledgeStore: knowledgeReader,
		AgentRunners: map[string]agentrunner.AgentRunner{"claude-code": runner}, ReposRoot: reposRoot}).handlePostStageMessage()(w, req)

	require.Equal(t, http.StatusOK, w.Code)

	events := parseSSEEvents(t, w.Body.String())
	require.Len(t, events, 3)
	assert.Equal(t, "thinking...", events[0].Content)
	assert.Equal(t, "here's the plan", events[1].Content)
	require.NotNil(t, events[2].ToolCall)
	assert.Equal(t, proposePlanToolName, events[2].ToolCall.Name)

	require.Len(t, persistedMsgs, 2)
	assert.Equal(t, "assistant", persistedMsgs[1].Role)
	assert.Equal(t, "thinking...here's the plan", persistedMsgs[1].Content)
	require.NotNil(t, persistedMsgs[1].ToolCall)
	assert.Equal(t, "call-1", persistedMsgs[1].ToolCall.ID)
	assert.Equal(t, `{"approach":"port logthing"}`, persistedMsgs[1].ToolCall.Arguments)
}

func TestHandlePostStageMessage_AgentExecutorWorkspaceResolutionFailureSurfacesAsSSEError(t *testing.T) {
	tasks := new(mockTaskStore)
	stubSessionIDCalls(tasks)
	tasks.On("Get", "demo-project", "TASK-0001").Return(task.Task{ID: "TASK-0001", Stage: task.StagePlanning}, nil)
	tasks.On("GetConversation", "demo-project", "TASK-0001", task.StagePlanning).Return(task.Conversation{}, nil)
	tasks.On("AppendConversationMessages", "demo-project", "TASK-0001", task.StagePlanning, mock.Anything).Return(task.Conversation{}, nil)

	knowledgeReader := new(mockKnowledgeStore)

	projects := new(mockProjectStore)
	// An unresolvable repository identifier (no matching checkout under
	// reposRoot) is real misconfiguration, unlike an absent-by-design
	// repository (ErrNoRepository, covered by the no-repository test
	// below) -> ResolveWorkspace still fails fatally, before the runner is
	// ever invoked. Conversation history is now loaded before resolveStageRun
	// runs (resolvedDecisionsSummary needs it), so that call must be mocked
	// too even though this test's failure is about workspace resolution.
	projects.On("Get", "demo-project").Return(project.Project{
		ID: "demo-project", Name: "Demo", Repositories: []string{"github.com/timmersuk/does-not-exist"},
	}, nil)

	runner := new(mockAgentRunner)

	req := newProjectRequest(t, http.MethodPost, "/api/v1/projects/demo-project/tasks/TASK-0001/stages/planning/messages", stageMessageRequest{Content: "go ahead", Executor: "claude-code", Effort: "medium"})
	req.SetPathValue("projectId", "demo-project")
	req.SetPathValue("taskId", "TASK-0001")
	req.SetPathValue("stage", "planning")
	w := httptest.NewRecorder()
	(&Server{Projects: projects, Tasks: tasks, KnowledgeStore: knowledgeReader,
		AgentRunners: map[string]agentrunner.AgentRunner{"claude-code": runner}, ReposRoot: t.TempDir()}).handlePostStageMessage()(w, req)

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
	stubSessionIDCalls(tasks)
	tasks.On("Get", "demo-project", "TASK-0001").Return(task.Task{ID: "TASK-0001", Stage: task.StagePlanning}, nil)
	tasks.On("GetConversation", "demo-project", "TASK-0001", task.StagePlanning).Return(task.Conversation{}, nil)
	tasks.On("AppendConversationMessages", "demo-project", "TASK-0001", task.StagePlanning, mock.Anything).Return(task.Conversation{}, nil)

	knowledgeReader := new(mockKnowledgeStore)

	projects := new(mockProjectStore)
	// No Repositories configured at all -> ResolveWorkspace returns
	// ErrNoRepository, which is now tolerated.
	projects.On("Get", "demo-project").Return(project.Project{ID: "demo-project", Name: "Demo"}, nil)

	var gotIn agentrunner.RunInput
	runner := new(mockAgentRunner)
	runner.On("Run", mock.Anything, mock.MatchedBy(func(in agentrunner.RunInput) bool {
		gotIn = in
		return true
	}), mock.Anything).Return(nil, agentrunner.RunOutput{Content: "ok"}, nil)

	req := newProjectRequest(t, http.MethodPost, "/api/v1/projects/demo-project/tasks/TASK-0001/stages/planning/messages", stageMessageRequest{Content: "go ahead", Executor: "local", Effort: "medium"})
	req.SetPathValue("projectId", "demo-project")
	req.SetPathValue("taskId", "TASK-0001")
	req.SetPathValue("stage", "planning")
	w := httptest.NewRecorder()
	(&Server{Projects: projects, Tasks: tasks, KnowledgeStore: knowledgeReader,
		AgentRunners: map[string]agentrunner.AgentRunner{"local": runner}, ReposRoot: t.TempDir()}).handlePostStageMessage()(w, req)

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
	stubSessionIDCalls(tasks)
	tasks.On("Get", "demo-project", "TASK-0001").Return(task.Task{ID: "TASK-0001", Stage: task.StageRequirements}, nil)
	tasks.On("GetConversation", "demo-project", "TASK-0001", task.StageRequirements).Return(task.Conversation{}, nil)

	var persistedMsgs []task.ConversationMessage
	tasks.On("AppendConversationMessages", "demo-project", "TASK-0001", task.StageRequirements, mock.MatchedBy(func(msgs []task.ConversationMessage) bool {
		persistedMsgs = msgs
		return true
	})).Return(task.Conversation{}, nil)
	tasks.On("ListReviews", "demo-project", "TASK-0001").Return(nil, nil)

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
	(&Server{Projects: projects, Tasks: factory, KnowledgeStore: new(mockKnowledgeStore),
		AgentRunners: map[string]agentrunner.AgentRunner{"local": runner}, ReposRoot: reposRoot}).handlePostStageMessage()(w, req)

	require.Equal(t, http.StatusOK, w.Code)
	require.Len(t, persistedMsgs, 2)
	assert.Equal(t, "assistant", persistedMsgs[1].Role)
	assert.Contains(t, persistedMsgs[1].Error, "pipe is being closed")
}

func TestHandleDeleteStageMessage_InvalidStage(t *testing.T) {
	req := httptest.NewRequest(http.MethodDelete, "/api/v1/projects/demo-project/tasks/TASK-0001/stages/implementation/messages/0", nil)
	req.SetPathValue("projectId", "demo-project")
	req.SetPathValue("taskId", "TASK-0001")
	req.SetPathValue("stage", "implementation")
	req.SetPathValue("index", "0")
	w := httptest.NewRecorder()
	(&Server{Projects: new(mockProjectStore), Tasks: new(mockTaskStore)}).handleDeleteStageMessage()(w, req)

	assert.Equal(t, http.StatusBadRequest, w.Code)
}

func TestHandleDeleteStageMessage_InvalidIndex(t *testing.T) {
	projects := new(mockProjectStore)
	projects.On("Get", "demo-project").Return(project.Project{ID: "demo-project"}, nil)

	req := httptest.NewRequest(http.MethodDelete, "/api/v1/projects/demo-project/tasks/TASK-0001/stages/requirements/messages/notanumber", nil)
	req.SetPathValue("projectId", "demo-project")
	req.SetPathValue("taskId", "TASK-0001")
	req.SetPathValue("stage", "requirements")
	req.SetPathValue("index", "notanumber")
	w := httptest.NewRecorder()
	(&Server{Projects: projects, Tasks: new(mockTaskStore)}).handleDeleteStageMessage()(w, req)

	assert.Equal(t, http.StatusBadRequest, w.Code)
}

// TestHandleDeleteStageMessage_StageMismatch locks in the URL/actual-stage
// guard (docs/milestones/done/milestone7.md PR 5): a task already at
// implementation must not let a message be deleted from its now-stale
// requirements conversation just because the URL names a valid Conversation
// stage.
func TestHandleDeleteStageMessage_StageMismatch(t *testing.T) {
	projects := new(mockProjectStore)
	projects.On("Get", "demo-project").Return(project.Project{ID: "demo-project"}, nil)

	tasks := new(mockTaskStore)
	stubSessionIDCalls(tasks)
	tasks.On("Get", "demo-project", "TASK-0001").Return(task.Task{ID: "TASK-0001", Stage: task.StageImplementation}, nil)

	req := httptest.NewRequest(http.MethodDelete, "/api/v1/projects/demo-project/tasks/TASK-0001/stages/requirements/messages/0", nil)
	req.SetPathValue("projectId", "demo-project")
	req.SetPathValue("taskId", "TASK-0001")
	req.SetPathValue("stage", "requirements")
	req.SetPathValue("index", "0")
	w := httptest.NewRecorder()
	(&Server{Projects: projects, Tasks: tasks}).handleDeleteStageMessage()(w, req)

	assert.Equal(t, http.StatusConflict, w.Code)
	tasks.AssertNotCalled(t, "GetConversation", "demo-project", "demo-project", "demo-project", mock.Anything, mock.Anything)
}

func TestHandleDeleteStageMessage_OutOfRangeIndex(t *testing.T) {
	projects := new(mockProjectStore)
	projects.On("Get", "demo-project").Return(project.Project{ID: "demo-project"}, nil)

	tasks := new(mockTaskStore)
	stubSessionIDCalls(tasks)
	tasks.On("Get", "demo-project", "TASK-0001").Return(task.Task{ID: "TASK-0001", Stage: task.StageRequirements}, nil)
	tasks.On("GetConversation", "demo-project", "TASK-0001", task.StageRequirements).Return(task.Conversation{
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
	(&Server{Projects: projects, Tasks: tasks}).handleDeleteStageMessage()(w, req)

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

	tasks := new(mockTaskStore)
	stubSessionIDCalls(tasks)
	tasks.On("Get", "demo-project", "TASK-0001").Return(task.Task{ID: "TASK-0001", Stage: task.StageRequirements}, nil)
	tasks.On("GetConversation", "demo-project", "TASK-0001", task.StageRequirements).Return(task.Conversation{
		Stage: task.StageRequirements,
		Messages: []task.ConversationMessage{
			{Role: "user", Content: "first"},
			{Role: "assistant", Content: "reply"},
			{Role: "user", Content: "third"},
		},
	}, nil)

	var replaced []task.ConversationMessage
	tasks.On("ReplaceConversationMessages", "demo-project", "TASK-0001", task.StageRequirements, mock.MatchedBy(func(msgs []task.ConversationMessage) bool {
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
	(&Server{Projects: projects, Tasks: tasks, AgentRunners: map[string]agentrunner.AgentRunner{"local": runner}}).handleDeleteStageMessage()(w, req)

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
	req := newProjectRequest(t, http.MethodPost, "/api/v1/projects/demo-project/tasks/TASK-0001/stages/implementation/messages/0/regenerate", stageRegenerateRequest{Content: "hi"})
	req.SetPathValue("projectId", "demo-project")
	req.SetPathValue("taskId", "TASK-0001")
	req.SetPathValue("stage", "implementation")
	req.SetPathValue("index", "0")
	w := httptest.NewRecorder()
	(&Server{Projects: new(mockProjectStore), Tasks: new(mockTaskStore), KnowledgeStore: new(mockKnowledgeStore)}).handleRegenerateStageMessage()(w, req)

	assert.Equal(t, http.StatusBadRequest, w.Code)
}

// TestHandleRegenerateStageMessage_StageMismatch locks in the URL/actual-stage
// guard (docs/milestones/done/milestone7.md PR 5): a task already at
// implementation must not regenerate a turn in its now-stale requirements
// conversation just because the URL names a valid Conversation stage.
func TestHandleRegenerateStageMessage_StageMismatch(t *testing.T) {
	tasks := new(mockTaskStore)
	stubSessionIDCalls(tasks)
	tasks.On("Get", "demo-project", "TASK-0001").Return(task.Task{ID: "TASK-0001", Stage: task.StageImplementation}, nil)

	runner := new(mockAgentRunner)
	reposRoot, repositories := newStageMessageWorkspace(t)
	projects, factory := newStageMessageServer(t, tasks, repositories)

	req := newProjectRequest(t, http.MethodPost, "/api/v1/projects/demo-project/tasks/TASK-0001/stages/requirements/messages/0/regenerate", stageRegenerateRequest{Content: "hi"})
	req.SetPathValue("projectId", "demo-project")
	req.SetPathValue("taskId", "TASK-0001")
	req.SetPathValue("stage", "requirements")
	req.SetPathValue("index", "0")
	w := httptest.NewRecorder()
	(&Server{Projects: projects, Tasks: factory, KnowledgeStore: new(mockKnowledgeStore),
		AgentRunners: map[string]agentrunner.AgentRunner{"local": runner}, ReposRoot: reposRoot}).handleRegenerateStageMessage()(w, req)

	assert.Equal(t, http.StatusConflict, w.Code)
	runner.AssertNotCalled(t, "Run", mock.Anything, mock.Anything, mock.Anything)
}

func TestHandleRegenerateStageMessage_RejectsNonUserIndex(t *testing.T) {
	projects := new(mockProjectStore)
	projects.On("Get", "demo-project").Return(project.Project{ID: "demo-project"}, nil)

	tasks := new(mockTaskStore)
	stubSessionIDCalls(tasks)
	tasks.On("Get", "demo-project", "TASK-0001").Return(task.Task{ID: "TASK-0001", Stage: task.StageRequirements}, nil)
	tasks.On("GetConversation", "demo-project", "TASK-0001", task.StageRequirements).Return(task.Conversation{
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
	(&Server{Projects: projects, Tasks: tasks, KnowledgeStore: new(mockKnowledgeStore)}).handleRegenerateStageMessage()(w, req)

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
	stubSessionIDCalls(tasks)
	tasks.On("Get", "demo-project", "TASK-0001").Return(task.Task{ID: "TASK-0001", Stage: task.StageRequirements}, nil)
	tasks.On("GetConversation", "demo-project", "TASK-0001", task.StageRequirements).Return(task.Conversation{
		Stage: task.StageRequirements,
		Messages: []task.ConversationMessage{
			{Role: "user", Content: "earlier"},
			{Role: "assistant", Content: "earlier reply"},
			{Role: "user", Content: "original question"},
			{Role: "assistant", Content: "stale reply"},
		},
	}, nil)
	tasks.On("ListReviews", "demo-project", "TASK-0001").Return(nil, nil)

	var replaced []task.ConversationMessage
	tasks.On("ReplaceConversationMessages", "demo-project", "TASK-0001", task.StageRequirements, mock.MatchedBy(func(msgs []task.ConversationMessage) bool {
		replaced = msgs
		return true
	})).Return(task.Conversation{}, nil)

	var gotIn agentrunner.RunInput
	runner := new(mockAgentRunner)
	runner.On("CloseSession", "TASK-0001:"+task.StageRequirements).Return()
	runner.On("Run", mock.Anything, mock.MatchedBy(func(in agentrunner.RunInput) bool {
		gotIn = in
		return true
	}), mock.Anything).Return([]chat.Delta{{Content: "fresh reply"}}, agentrunner.RunOutput{}, nil)

	reposRoot, repositories := newStageMessageWorkspace(t)
	projects, factory := newStageMessageServer(t, tasks, repositories)

	req := newProjectRequest(t, http.MethodPost, "/api/v1/projects/demo-project/tasks/TASK-0001/stages/requirements/messages/2/regenerate", stageRegenerateRequest{Content: "original question"})
	req.SetPathValue("projectId", "demo-project")
	req.SetPathValue("taskId", "TASK-0001")
	req.SetPathValue("stage", "requirements")
	req.SetPathValue("index", "2")
	w := httptest.NewRecorder()
	(&Server{Projects: projects, Tasks: factory, KnowledgeStore: new(mockKnowledgeStore),
		AgentRunners: map[string]agentrunner.AgentRunner{"local": runner}, ReposRoot: reposRoot}).handleRegenerateStageMessage()(w, req)

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
	stubSessionIDCalls(tasks)
	tasks.On("Get", "demo-project", "TASK-0001").Return(task.Task{ID: "TASK-0001", Stage: task.StageRequirements}, nil)
	tasks.On("GetConversation", "demo-project", "TASK-0001", task.StageRequirements).Return(task.Conversation{
		Stage: task.StageRequirements,
		Messages: []task.ConversationMessage{
			{Role: "user", Content: "original question"},
			{Role: "assistant", Content: "stale reply"},
		},
	}, nil)
	tasks.On("ListReviews", "demo-project", "TASK-0001").Return(nil, nil)

	var replaced []task.ConversationMessage
	tasks.On("ReplaceConversationMessages", "demo-project", "TASK-0001", task.StageRequirements, mock.MatchedBy(func(msgs []task.ConversationMessage) bool {
		replaced = msgs
		return true
	})).Return(task.Conversation{}, nil)

	runner := new(mockAgentRunner)
	runner.On("CloseSession", "TASK-0001:"+task.StageRequirements).Return()
	runner.On("Run", mock.Anything, mock.Anything, mock.Anything).Return([]chat.Delta{{Content: "new reply"}}, agentrunner.RunOutput{}, nil)

	reposRoot, repositories := newStageMessageWorkspace(t)
	projects, factory := newStageMessageServer(t, tasks, repositories)

	req := newProjectRequest(t, http.MethodPost, "/api/v1/projects/demo-project/tasks/TASK-0001/stages/requirements/messages/0/regenerate", stageRegenerateRequest{Content: "edited question"})
	req.SetPathValue("projectId", "demo-project")
	req.SetPathValue("taskId", "TASK-0001")
	req.SetPathValue("stage", "requirements")
	req.SetPathValue("index", "0")
	w := httptest.NewRecorder()
	(&Server{Projects: projects, Tasks: factory, KnowledgeStore: new(mockKnowledgeStore),
		AgentRunners: map[string]agentrunner.AgentRunner{"local": runner}, ReposRoot: reposRoot}).handleRegenerateStageMessage()(w, req)

	require.Equal(t, http.StatusOK, w.Code)
	require.Len(t, replaced, 2)
	assert.Equal(t, "edited question", replaced[0].Content)
	assert.Equal(t, "new reply", replaced[1].Content)
}

// TestConversationHistoryToChatMessages_IncludesToolActivity locks in
// docs/adr/0022's rehydration fix: a message's ToolActivity must be folded
// into the flattened content, not dropped, so a session rehydrated after a
// restart still has evidence of its own prior tool calls instead of denying
// (or "confessing to fabricating") something it genuinely did.
func TestConversationHistoryToChatMessages_IncludesToolActivity(t *testing.T) {
	conv := task.Conversation{
		Stage: task.StageRequirements,
		Messages: []task.ConversationMessage{
			{
				Role:    "assistant",
				Content: "I've kicked off research into the storage layer.",
				ToolActivity: []task.ConversationToolActivity{
					{Name: "Agent", Arguments: `{"description":"Explore persistence layer"}`, Result: "Async agent launched successfully.", IsError: false},
					{Name: "ScheduleWakeup", Arguments: `{"stop":false}`, Result: "'prompt' is required when stop is not true", IsError: true},
				},
			},
		},
	}

	got := conversationHistoryToChatMessages(conv)

	require.Len(t, got, 1)
	assert.Contains(t, got[0].Content, "I've kicked off research into the storage layer.")
	assert.Contains(t, got[0].Content, "Agent (ok)")
	assert.Contains(t, got[0].Content, "ScheduleWakeup (error)")
	assert.Contains(t, got[0].Content, "'prompt' is required when stop is not true")
}

// TestConversationHistoryToChatMessages_CollapsesRepeatedToolNames locks in
// summarizeToolActivities: repeated successful calls to the same tool
// (a dozen Read/Grep calls exploring a codebase in one turn, the common
// shape once docs/adr/0022 closes off Agent/ScheduleWakeup) collapse to a
// single "Name ×N" line instead of one full preview per call, keeping a
// tool-heavy turn from eating the whole maxHistoryReplayBytes budget on
// repetition alone. A lone call keeps its full preview. Every erroring
// call — even a repeat of an already-collapsed name — keeps its own
// full-detail line, since the error itself is the signal worth replaying.
func TestConversationHistoryToChatMessages_CollapsesRepeatedToolNames(t *testing.T) {
	msg := task.ConversationMessage{Role: "assistant", Content: "explored the codebase"}
	for i := 0; i < 12; i++ {
		msg.ToolActivity = append(msg.ToolActivity, task.ConversationToolActivity{Name: "Read", Result: "file contents"})
	}
	msg.ToolActivity = append(msg.ToolActivity,
		task.ConversationToolActivity{Name: "Agent", Result: "Async agent launched successfully."},
		task.ConversationToolActivity{Name: "ScheduleWakeup", Result: "'prompt' is required when stop is not true", IsError: true},
		task.ConversationToolActivity{Name: "ScheduleWakeup", Result: "'prompt' is required when stop is not true", IsError: true},
	)

	got := conversationHistoryToChatMessages(task.Conversation{
		Stage:    task.StageRequirements,
		Messages: []task.ConversationMessage{msg},
	})

	require.Len(t, got, 1)
	content := got[0].Content
	assert.Contains(t, content, "Read ×12", "12 identical successful Read calls must collapse to one count line")
	assert.NotContains(t, content, "file contents", "a collapsed group must not carry a per-call preview")
	assert.Contains(t, content, "Agent (ok): Async agent launched successfully.", "a lone call keeps its full preview")
	assert.Equal(t, 2, strings.Count(content, "ScheduleWakeup (error)"), "every erroring call stays individually detailed, even repeated ones")
}

// TestConversationHistoryToChatMessages_TruncatesLongToolActivity ensures a
// single turn's tool activity can't dominate the replayed-history budget
// (maxHistoryReplayBytes in claude_runner.go, sized for Windows'
// CreateProcess ~32K argument-length ceiling) — each activity's preview is
// capped, not replayed at its full persisted size (up to 2KB per
// task.maxPersistedToolActivityBytes).
func TestConversationHistoryToChatMessages_TruncatesLongToolActivity(t *testing.T) {
	huge := make([]byte, maxRehydratedToolActivityPreviewBytes*4)
	for i := range huge {
		huge[i] = 'x'
	}
	conv := task.Conversation{
		Stage: task.StageRequirements,
		Messages: []task.ConversationMessage{
			{
				Role:    "assistant",
				Content: "turn with a huge tool result",
				ToolActivity: []task.ConversationToolActivity{
					{Name: "Grep", Result: string(huge)},
				},
			},
		},
	}

	got := conversationHistoryToChatMessages(conv)

	require.Len(t, got, 1)
	assert.Less(t, len(got[0].Content), len(huge))
}

func TestConversationHistoryToChatMessages_EmptyConversationReturnsNil(t *testing.T) {
	got := conversationHistoryToChatMessages(task.Conversation{})
	assert.Nil(t, got)
}

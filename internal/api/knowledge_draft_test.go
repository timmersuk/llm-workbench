package api

import (
	"bytes"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/mock"
	"github.com/stretchr/testify/require"

	"github.com/timmersuk/llm-workbench/internal/knowledge"
	"github.com/timmersuk/llm-workbench/internal/project"
	"github.com/timmersuk/llm-workbench/internal/task"
)

func newFinalizeKnowledgeRequest(t *testing.T, body finalizeKnowledgeRequest) *http.Request {
	t.Helper()
	req := newProjectRequest(t, http.MethodPost, "/api/v1/projects/demo-project/tasks/TASK-0001/knowledge/finalize", body)
	req.SetPathValue("projectId", "demo-project")
	req.SetPathValue("taskId", "TASK-0001")
	return req
}

func newReviewStageServer(t *testing.T, knowledgeStore *mockKnowledgeStore) (*mockProjectStore, *mockTaskStore, *Server) {
	t.Helper()
	projects := new(mockProjectStore)
	projects.On("Get", "demo-project").Return(project.Project{ID: "demo-project"}, nil)

	tasks := new(mockTaskStore)
	tasks.On("Get", "demo-project", "TASK-0001").Return(task.Task{ID: "TASK-0001", Stage: task.StageReview}, nil)
	tasks.On("AppendKnowledgeActivity", "demo-project", "TASK-0001", mock.Anything).Return(task.Task{}, nil)
	tasks.On("AppendConversationMessages", "demo-project", "TASK-0001", task.StageReview, mock.Anything).Return(task.Conversation{}, nil)

	return projects, tasks, &Server{Projects: projects, Tasks: tasks, KnowledgeStore: knowledgeStore}
}

func TestHandleFinalizeKnowledge_Accepted_WritesConcept(t *testing.T) {
	knowledgeStore := new(mockKnowledgeStore)
	concept := knowledge.Concept{Type: "Coding Standard", Frontmatter: map[string]any{"title": "Logging"}, Body: "Use structured logging.\n"}
	knowledgeStore.On("Get", "coding-standards/logging").Return(knowledge.Concept{}, os.ErrNotExist)
	knowledgeStore.On("Put", "coding-standards/logging", concept).Return(nil)

	_, tasks, server := newReviewStageServer(t, knowledgeStore)

	req := newFinalizeKnowledgeRequest(t, finalizeKnowledgeRequest{
		ConceptID: "coding-standards/logging", Type: "Coding Standard",
		Frontmatter: map[string]any{"title": "Logging"}, Body: "Use structured logging.\n",
		Decision: knowledgeDecisionAccepted,
	})
	w := httptest.NewRecorder()
	server.handleFinalizeKnowledge()(w, req)

	require.Equal(t, http.StatusOK, w.Code)
	var got finalizeKnowledgeResponse
	require.NoError(t, json.Unmarshal(w.Body.Bytes(), &got))
	assert.Equal(t, "coding-standards/logging", got.ConceptID)
	assert.Equal(t, knowledgeDecisionAccepted, got.Decision)
	require.NotNil(t, got.Concept)
	assert.Equal(t, "Coding Standard", got.Concept.Type)
	knowledgeStore.AssertCalled(t, "Put", "coding-standards/logging", concept)
	tasks.AssertCalled(t, "AppendKnowledgeActivity", "demo-project", "TASK-0001", task.KnowledgeActivityEntry{
		ConceptID: "coding-standards/logging", Type: "Coding Standard", Action: task.KnowledgeActivityCreated,
	})
	require.NotNil(t, got.Task, "the updated task (with the new log entry) is returned for the client to refresh from, no second GET required")
	assert.Equal(t, `Accepted the knowledge concept "coding-standards/logging" — created.`, got.Note)
	tasks.AssertCalled(t, "AppendConversationMessages", "demo-project", "TASK-0001", task.StageReview, []task.ConversationMessage{
		{Role: "user", Content: `Accepted the knowledge concept "coding-standards/logging" — created.`},
	})
}

// TestHandleFinalizeKnowledge_Accepted_ExistingConceptRecordsUpdated covers
// the "updated" half of KnowledgeActivityAction: a concept id that already
// resolves via KnowledgeStore.Get is recorded as updated, not created.
func TestHandleFinalizeKnowledge_Accepted_ExistingConceptRecordsUpdated(t *testing.T) {
	knowledgeStore := new(mockKnowledgeStore)
	concept := knowledge.Concept{Type: "Coding Standard", Body: "revised body"}
	knowledgeStore.On("Get", "coding-standards/logging").Return(knowledge.Concept{Type: "Coding Standard", Body: "old body"}, nil)
	knowledgeStore.On("Put", "coding-standards/logging", concept).Return(nil)

	_, tasks, server := newReviewStageServer(t, knowledgeStore)

	req := newFinalizeKnowledgeRequest(t, finalizeKnowledgeRequest{
		ConceptID: "coding-standards/logging", Type: "Coding Standard", Body: "revised body", Decision: knowledgeDecisionAccepted,
	})
	w := httptest.NewRecorder()
	server.handleFinalizeKnowledge()(w, req)

	require.Equal(t, http.StatusOK, w.Code)
	tasks.AssertCalled(t, "AppendKnowledgeActivity", "demo-project", "TASK-0001", task.KnowledgeActivityEntry{
		ConceptID: "coding-standards/logging", Type: "Coding Standard", Action: task.KnowledgeActivityUpdated,
	})
}

func TestHandleFinalizeKnowledge_Rejected_NeverWrites(t *testing.T) {
	knowledgeStore := new(mockKnowledgeStore)
	_, tasks, server := newReviewStageServer(t, knowledgeStore)

	req := newFinalizeKnowledgeRequest(t, finalizeKnowledgeRequest{
		ConceptID: "coding-standards/logging", Type: "Coding Standard", Decision: knowledgeDecisionRejected,
	})
	w := httptest.NewRecorder()
	server.handleFinalizeKnowledge()(w, req)

	require.Equal(t, http.StatusOK, w.Code)
	var got finalizeKnowledgeResponse
	require.NoError(t, json.Unmarshal(w.Body.Bytes(), &got))
	assert.Equal(t, knowledgeDecisionRejected, got.Decision)
	assert.Nil(t, got.Concept)
	knowledgeStore.AssertNotCalled(t, "Put", mock.Anything, mock.Anything)
	tasks.AssertCalled(t, "AppendKnowledgeActivity", "demo-project", "TASK-0001", task.KnowledgeActivityEntry{
		ConceptID: "coding-standards/logging", Type: "Coding Standard", Action: task.KnowledgeActivityRejected,
	})
	assert.Equal(t, `Rejected the proposed knowledge concept "coding-standards/logging".`, got.Note)
	tasks.AssertCalled(t, "AppendConversationMessages", "demo-project", "TASK-0001", task.StageReview, []task.ConversationMessage{
		{Role: "user", Content: `Rejected the proposed knowledge concept "coding-standards/logging".`},
	})
}

// TestHandleFinalizeKnowledge_ConversationNoteFailure_StillSucceeds covers
// appendKnowledgeDecisionNote's best-effort contract: a failure appending
// the acknowledgment message to the Review Conversation must not turn an
// otherwise-successful accept into a client-visible error — the response
// simply omits Note.
func TestHandleFinalizeKnowledge_ConversationNoteFailure_StillSucceeds(t *testing.T) {
	knowledgeStore := new(mockKnowledgeStore)
	concept := knowledge.Concept{Type: "Coding Standard", Body: "x"}
	knowledgeStore.On("Get", "coding-standards/logging").Return(knowledge.Concept{}, os.ErrNotExist)
	knowledgeStore.On("Put", "coding-standards/logging", concept).Return(nil)

	projects := new(mockProjectStore)
	projects.On("Get", "demo-project").Return(project.Project{ID: "demo-project"}, nil)
	tasks := new(mockTaskStore)
	tasks.On("Get", "demo-project", "TASK-0001").Return(task.Task{ID: "TASK-0001", Stage: task.StageReview}, nil)
	tasks.On("AppendKnowledgeActivity", "demo-project", "TASK-0001", mock.Anything).Return(task.Task{}, nil)
	tasks.On("AppendConversationMessages", "demo-project", "TASK-0001", task.StageReview, mock.Anything).
		Return(task.Conversation{}, assert.AnError)
	server := &Server{Projects: projects, Tasks: tasks, KnowledgeStore: knowledgeStore}

	req := newFinalizeKnowledgeRequest(t, finalizeKnowledgeRequest{
		ConceptID: "coding-standards/logging", Type: "Coding Standard", Body: "x", Decision: knowledgeDecisionAccepted,
	})
	w := httptest.NewRecorder()
	server.handleFinalizeKnowledge()(w, req)

	require.Equal(t, http.StatusOK, w.Code)
	var got finalizeKnowledgeResponse
	require.NoError(t, json.Unmarshal(w.Body.Bytes(), &got))
	assert.Empty(t, got.Note)
	require.NotNil(t, got.Concept, "the primary accept (writing the concept) still succeeds")
}

func TestHandleFinalizeKnowledge_WrongStage(t *testing.T) {
	projects := new(mockProjectStore)
	projects.On("Get", "demo-project").Return(project.Project{ID: "demo-project"}, nil)

	tasks := new(mockTaskStore)
	tasks.On("Get", "demo-project", "TASK-0001").Return(task.Task{ID: "TASK-0001", Stage: task.StageImplementation}, nil)

	server := &Server{Projects: projects, Tasks: tasks, KnowledgeStore: new(mockKnowledgeStore)}

	req := newFinalizeKnowledgeRequest(t, finalizeKnowledgeRequest{
		ConceptID: "x", Type: "Reference", Decision: knowledgeDecisionAccepted,
	})
	w := httptest.NewRecorder()
	server.handleFinalizeKnowledge()(w, req)

	assert.Equal(t, http.StatusConflict, w.Code)
}

func TestHandleFinalizeKnowledge_InvalidBody(t *testing.T) {
	_, _, server := newReviewStageServer(t, new(mockKnowledgeStore))

	req := httptest.NewRequest(http.MethodPost, "/api/v1/projects/demo-project/tasks/TASK-0001/knowledge/finalize", bytes.NewReader([]byte("not json")))
	req.SetPathValue("projectId", "demo-project")
	req.SetPathValue("taskId", "TASK-0001")
	w := httptest.NewRecorder()
	server.handleFinalizeKnowledge()(w, req)

	assert.Equal(t, http.StatusBadRequest, w.Code)
}

func TestHandleFinalizeKnowledge_MissingConceptID(t *testing.T) {
	_, _, server := newReviewStageServer(t, new(mockKnowledgeStore))

	req := newFinalizeKnowledgeRequest(t, finalizeKnowledgeRequest{Type: "Reference", Decision: knowledgeDecisionAccepted})
	w := httptest.NewRecorder()
	server.handleFinalizeKnowledge()(w, req)

	assert.Equal(t, http.StatusBadRequest, w.Code)
}

func TestHandleFinalizeKnowledge_AcceptWithoutTypeIsRejected(t *testing.T) {
	knowledgeStore := new(mockKnowledgeStore)
	_, _, server := newReviewStageServer(t, knowledgeStore)

	req := newFinalizeKnowledgeRequest(t, finalizeKnowledgeRequest{ConceptID: "x", Decision: knowledgeDecisionAccepted})
	w := httptest.NewRecorder()
	server.handleFinalizeKnowledge()(w, req)

	assert.Equal(t, http.StatusBadRequest, w.Code)
	knowledgeStore.AssertNotCalled(t, "Put", mock.Anything, mock.Anything)
}

func TestHandleFinalizeKnowledge_InvalidDecision(t *testing.T) {
	_, _, server := newReviewStageServer(t, new(mockKnowledgeStore))

	req := newFinalizeKnowledgeRequest(t, finalizeKnowledgeRequest{ConceptID: "x", Type: "Reference", Decision: "maybe"})
	w := httptest.NewRecorder()
	server.handleFinalizeKnowledge()(w, req)

	assert.Equal(t, http.StatusBadRequest, w.Code)
}

func TestHandleFinalizeKnowledge_PutFailure_InvalidConceptID(t *testing.T) {
	knowledgeStore := new(mockKnowledgeStore)
	knowledgeStore.On("Get", "../escape").Return(knowledge.Concept{}, os.ErrNotExist)
	knowledgeStore.On("Put", "../escape", knowledge.Concept{Type: "Reference", Body: "x"}).Return(knowledge.ErrInvalidConceptID)
	_, _, server := newReviewStageServer(t, knowledgeStore)

	req := newFinalizeKnowledgeRequest(t, finalizeKnowledgeRequest{
		ConceptID: "../escape", Type: "Reference", Body: "x", Decision: knowledgeDecisionAccepted,
	})
	w := httptest.NewRecorder()
	server.handleFinalizeKnowledge()(w, req)

	assert.Equal(t, http.StatusBadRequest, w.Code)
}

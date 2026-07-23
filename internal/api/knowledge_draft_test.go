package api

import (
	"bytes"
	"encoding/json"
	"net/http"
	"net/http/httptest"
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
	projects.On("TasksRoot", "demo-project").Return("/data/projects/demo-project/tasks", nil)

	tasks := new(mockTaskStore)
	tasks.On("Get", "TASK-0001").Return(task.Task{ID: "TASK-0001", Stage: task.StageReview}, nil)

	return projects, tasks, &Server{Projects: projects, TaskStores: fixedTaskStoreFactory(tasks), KnowledgeStore: knowledgeStore}
}

func TestHandleFinalizeKnowledge_Accepted_WritesConcept(t *testing.T) {
	knowledgeStore := new(mockKnowledgeStore)
	concept := knowledge.Concept{Type: "Coding Standard", Frontmatter: map[string]any{"title": "Logging"}, Body: "Use structured logging.\n"}
	knowledgeStore.On("Put", "coding-standards/logging", concept).Return(nil)

	_, _, server := newReviewStageServer(t, knowledgeStore)

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
}

func TestHandleFinalizeKnowledge_Rejected_NeverWrites(t *testing.T) {
	knowledgeStore := new(mockKnowledgeStore)
	_, _, server := newReviewStageServer(t, knowledgeStore)

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
}

func TestHandleFinalizeKnowledge_WrongStage(t *testing.T) {
	projects := new(mockProjectStore)
	projects.On("Get", "demo-project").Return(project.Project{ID: "demo-project"}, nil)
	projects.On("TasksRoot", "demo-project").Return("/data/projects/demo-project/tasks", nil)

	tasks := new(mockTaskStore)
	tasks.On("Get", "TASK-0001").Return(task.Task{ID: "TASK-0001", Stage: task.StageImplementation}, nil)

	server := &Server{Projects: projects, TaskStores: fixedTaskStoreFactory(tasks), KnowledgeStore: new(mockKnowledgeStore)}

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
	knowledgeStore.On("Put", "../escape", knowledge.Concept{Type: "Reference", Body: "x"}).Return(knowledge.ErrInvalidConceptID)
	_, _, server := newReviewStageServer(t, knowledgeStore)

	req := newFinalizeKnowledgeRequest(t, finalizeKnowledgeRequest{
		ConceptID: "../escape", Type: "Reference", Body: "x", Decision: knowledgeDecisionAccepted,
	})
	w := httptest.NewRecorder()
	server.handleFinalizeKnowledge()(w, req)

	assert.Equal(t, http.StatusBadRequest, w.Code)
}

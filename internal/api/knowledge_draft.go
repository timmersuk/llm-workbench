package api

import (
	"encoding/json"
	"fmt"
	"net/http"

	"github.com/timmersuk/llm-workbench/internal/knowledge"
	"github.com/timmersuk/llm-workbench/internal/task"
)

// Decision values for handleFinalizeKnowledge — two-way, unlike Review's
// own three-way approved/rejected/needs_changes verdict: a knowledge
// proposal has no prior execution branch for a "needs_changes" state to
// continue from (docs/milestones/done/milestone9.md), so a rejected proposal is
// just more conversation, not a state this endpoint records.
const (
	knowledgeDecisionAccepted = "accepted"
	knowledgeDecisionRejected = "rejected"
)

// finalizeKnowledgeRequest is the request body for handleFinalizeKnowledge:
// a propose_knowledge Draft carried over from the Review-stage conversation
// (concept_id, type, frontmatter, body — the same shape the tool call
// itself carries, per drafttool.ProposeKnowledge's schema) plus the
// human's accept/reject decision.
type finalizeKnowledgeRequest struct {
	ConceptID   string         `json:"concept_id"`
	Type        string         `json:"type"`
	Frontmatter map[string]any `json:"frontmatter,omitempty"`
	Body        string         `json:"body"`
	Decision    string         `json:"decision"`
}

// finalizeKnowledgeResponse echoes back what was decided. Concept is only
// populated on accept, letting a client confirm what was actually written
// without a second GET.
type finalizeKnowledgeResponse struct {
	ConceptID string             `json:"concept_id"`
	Decision  string             `json:"decision"`
	Concept   *knowledge.Concept `json:"concept,omitempty"`
}

// handleFinalizeKnowledge is the human's accept/reject decision on a
// propose_knowledge Draft (docs/milestones/done/milestone9.md). Unlike
// handleFinalizeReview, this never touches TaskStore or the task's stage:
// a knowledge concept lives in a workspace-wide store independent of any
// one task, and the Review conversation itself (and its own eventual
// propose_review verdict) continues regardless of what a human decides
// here. On accept, the concept is written via KnowledgeStore.Put — a
// whole-file replace, never a partial merge, matching propose_knowledge's
// own "always the full content, never a diff" tool contract. On reject,
// this is a no-op beyond acknowledging the decision: there is nothing to
// record, the executor is simply free to redraft and re-propose within the
// same conversation.
//
// Scoped under a task path (mirroring every other Finalize-family route)
// and gated on that task currently being at stage: review — the only stage
// propose_knowledge is ever offered from. The write itself touches no task
// state, but a call for a task that isn't at Review is almost certainly a
// stale or buggy client, worth surfacing as a 409 rather than a silent
// knowledge-store write with no relationship to what's on screen.
func (s *Server) handleFinalizeKnowledge() http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		projectId := r.PathValue("projectId")
		store, ok := s.resolveTaskStore(w, projectId)
		if !ok {
			return
		}

		taskId := r.PathValue("taskId")
		t, err := store.Get(projectId, taskId)
		if err != nil {
			writeGetError(w, err)
			return
		}
		if err := requireCurrentStage(t, task.StageReview); err != nil {
			writeGetError(w, err)
			return
		}

		var req finalizeKnowledgeRequest
		if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
			writeAPIError(w, http.StatusBadRequest, "invalid request body")
			return
		}
		if req.ConceptID == "" {
			writeAPIError(w, http.StatusBadRequest, "concept_id is required")
			return
		}

		switch req.Decision {
		case knowledgeDecisionAccepted:
			if req.Type == "" {
				writeAPIError(w, http.StatusBadRequest, "type is required to accept a knowledge proposal")
				return
			}
			concept := knowledge.Concept{Type: req.Type, Frontmatter: req.Frontmatter, Body: req.Body}
			if err := s.KnowledgeStore.Put(req.ConceptID, concept); err != nil {
				writeMutationError(w, err)
				return
			}
			writeJSON(w, http.StatusOK, finalizeKnowledgeResponse{ConceptID: req.ConceptID, Decision: req.Decision, Concept: &concept})
		case knowledgeDecisionRejected:
			writeJSON(w, http.StatusOK, finalizeKnowledgeResponse{ConceptID: req.ConceptID, Decision: req.Decision})
		default:
			writeAPIError(w, http.StatusBadRequest, fmt.Sprintf("invalid decision %q", req.Decision))
		}
	}
}

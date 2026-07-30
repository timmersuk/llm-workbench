package api

import (
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"strings"
)

// reviseRequest is the optional JSON body for the Revise Requirements/Plan
// actions: a short, human-typed explanation for why the task is going back
// (e.g. "I wanted icons, not words"), recorded on the resulting
// StageTransition. An empty or omitted body means no reason was given, not
// an error — these actions predate this field and stay one-click by
// default.
type reviseRequest struct {
	Reason string `json:"reason"`
}

// decodeReviseReason reads an optional reviseRequest body, tolerating an
// empty body (io.EOF) as "no reason given" rather than a malformed request.
// The bool return is false only for an actually malformed (non-empty,
// non-JSON) body.
func decodeReviseReason(r *http.Request) (reason string, ok bool) {
	var req reviseRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		return "", errors.Is(err, io.EOF)
	}
	return strings.TrimSpace(req.Reason), true
}

// handleReviseRequirements is the "Revise Requirements" action
// (CONTEXT.md's "Revise"): moves stage back from planning to requirements,
// reopening the requirements Conversation. 409 if the task isn't currently
// in planning stage.
func (s *Server) handleReviseRequirements() http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		projectId := r.PathValue("projectId")
		store, ok := s.resolveTaskStore(w, projectId)
		if !ok {
			return
		}

		reason, ok := decodeReviseReason(r)
		if !ok {
			writeAPIError(w, http.StatusBadRequest, "invalid request body")
			return
		}

		updated, err := store.ReviseToRequirements(projectId, r.PathValue("taskId"), reason)
		if err != nil {
			writeMutationError(w, err)
			return
		}
		writeJSON(w, http.StatusOK, updated)
	}
}

// handleRevisePlan is the "Revise Plan" action: moves stage back from
// implementation/review to planning. 409 if the task isn't currently in
// implementation or review stage.
func (s *Server) handleRevisePlan() http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		projectId := r.PathValue("projectId")
		store, ok := s.resolveTaskStore(w, projectId)
		if !ok {
			return
		}

		reason, ok := decodeReviseReason(r)
		if !ok {
			writeAPIError(w, http.StatusBadRequest, "invalid request body")
			return
		}

		updated, err := store.ReviseToPlanning(projectId, r.PathValue("taskId"), reason)
		if err != nil {
			writeMutationError(w, err)
			return
		}
		writeJSON(w, http.StatusOK, updated)
	}
}

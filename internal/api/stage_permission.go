package api

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"net/http"
	"sync"
)

// newEscalationID returns a random, unguessable request ID. crypto/rand keeps
// this in the standard library (no UUID dependency); 16 random bytes are unique
// within this process and hard to forge across sessions.
func newEscalationID() string {
	var b [16]byte
	_, _ = rand.Read(b[:])
	return hex.EncodeToString(b[:])
}

// escalationRegistry tracks in-flight human permission escalations (a
// RunInput.OnPermissionRequest blocked on the human — docs/adr/0024), keyed by
// request ID. Each entry records its owning SessionKey so one session can never
// resolve another's request.
type escalationRegistry struct {
	mu      sync.Mutex
	pending map[string]*pendingEscalation
}

// pendingEscalation is one blocked escalation: its owning SessionKey and a
// buffered channel the decision endpoint sends the human's allow/deny on.
type pendingEscalation struct {
	sessionKey string
	decision   chan bool
}

func newEscalationRegistry() *escalationRegistry {
	return &escalationRegistry{pending: make(map[string]*pendingEscalation)}
}

// register creates a pending escalation for sessionKey and returns its ID, the
// decision channel (buffered so a resolve never blocks), and a cleanup func the
// waiter defers to forget the entry.
func (e *escalationRegistry) register(sessionKey string) (id string, decision <-chan bool, cleanup func()) {
	ch := make(chan bool, 1)
	id = newEscalationID()
	e.mu.Lock()
	e.pending[id] = &pendingEscalation{sessionKey: sessionKey, decision: ch}
	e.mu.Unlock()
	return id, ch, func() {
		e.mu.Lock()
		delete(e.pending, id)
		e.mu.Unlock()
	}
}

// resolve delivers allow to escalation id, but only if it exists and belongs to
// sessionKey — a foreign or unknown ID returns false rather than resolving.
func (e *escalationRegistry) resolve(sessionKey, id string, allow bool) bool {
	e.mu.Lock()
	p, found := e.pending[id]
	if found && p.sessionKey == sessionKey {
		delete(e.pending, id)
	}
	e.mu.Unlock()
	if !found || p.sessionKey != sessionKey {
		return false
	}
	p.decision <- allow // buffered + pre-deleted: never blocks, no double-resolve
	return true
}

// synchronizeWriteEvent serializes concurrent writers to one SSE
// ResponseWriter. A human-paced turn has two: the message-stream drain and the
// escalation callback (invoked from the runner's goroutine).
func synchronizeWriteEvent(writeEvent func(chatStreamEvent)) func(chatStreamEvent) {
	var mu sync.Mutex
	return func(ev chatStreamEvent) {
		mu.Lock()
		defer mu.Unlock()
		writeEvent(ev)
	}
}

// stagePermissionRequestHook builds RunInput.OnPermissionRequest for one turn:
// it registers a pending escalation, emits the permission_request SSE event, and
// blocks until the human decides or ctx is cancelled (closed tab / timeout ->
// deny). writeEvent must be the concurrency-safe writer.
func (s *Server) stagePermissionRequestHook(sessionKey string, writeEvent func(chatStreamEvent)) func(context.Context, string, string) (bool, error) {
	if s.escalations == nil {
		return nil
	}
	return func(ctx context.Context, toolName, argsJSON string) (bool, error) {
		id, decision, cleanup := s.escalations.register(sessionKey)
		defer cleanup()
		writeEvent(chatStreamEvent{PermissionRequest: &chatPermissionRequestEvent{ID: id, Name: toolName, Arguments: argsJSON}})
		select {
		case allow := <-decision:
			return allow, nil
		case <-ctx.Done():
			return false, ctx.Err()
		}
	}
}

// stagePermissionDecisionRequest is the /permission endpoint body.
type stagePermissionDecisionRequest struct {
	RequestID string `json:"request_id"`
	Allow     bool   `json:"allow"`
}

// handleStagePermissionDecision resolves a pending escalation. The request ID is
// validated against this route's own SessionKey (taskId:stage) inside resolve,
// so one session can't answer another's; an unknown or foreign ID is a 404.
func (s *Server) handleStagePermissionDecision() http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		var req stagePermissionDecisionRequest
		if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
			writeAPIError(w, http.StatusBadRequest, "invalid request body")
			return
		}
		if req.RequestID == "" {
			writeAPIError(w, http.StatusBadRequest, "request_id is required")
			return
		}
		if s.escalations == nil {
			writeAPIError(w, http.StatusNotFound, "no pending permission request")
			return
		}
		sessionKey := r.PathValue("taskId") + ":" + r.PathValue("stage")
		if !s.escalations.resolve(sessionKey, req.RequestID, req.Allow) {
			writeAPIError(w, http.StatusNotFound, "no pending permission request for this session")
			return
		}
		w.WriteHeader(http.StatusNoContent)
	}
}

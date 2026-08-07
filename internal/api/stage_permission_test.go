package api

import (
	"bytes"
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// TestEscalationRegistry_ResolveApproveAndDeny confirms a registered
// escalation's waiter receives exactly the decision the matching resolve delivers.
func TestEscalationRegistry_ResolveApproveAndDeny(t *testing.T) {
	for _, want := range []bool{true, false} {
		reg := newEscalationRegistry()
		id, decision, cleanup := reg.register("task-1:planning")
		defer cleanup()

		require.True(t, reg.resolve("task-1:planning", id, want))
		select {
		case got := <-decision:
			assert.Equal(t, want, got)
		case <-time.After(time.Second):
			t.Fatal("waiter never received a decision")
		}
	}
}

// TestEscalationRegistry_RejectsForeignAndUnknownID is the security-critical
// case: a foreign SessionKey or unknown ID must not resolve the request.
func TestEscalationRegistry_RejectsForeignAndUnknownID(t *testing.T) {
	reg := newEscalationRegistry()
	id, decision, cleanup := reg.register("task-1:planning")
	defer cleanup()

	assert.False(t, reg.resolve("task-2:planning", id, true), "a foreign session must not resolve the request")
	assert.False(t, reg.resolve("task-1:planning", "does-not-exist", true), "an unknown ID must not resolve anything")

	select {
	case <-decision:
		t.Fatal("a rejected resolve must not have delivered a decision")
	default:
	}
	require.True(t, reg.resolve("task-1:planning", id, true), "the real owner can still resolve it")
	assert.Equal(t, true, <-decision)
}

// TestStagePermissionRequestHook_BlocksUntilDecision drives the full hook: it
// emits a permission_request event, blocks, and returns the resolved decision.
func TestStagePermissionRequestHook_BlocksUntilDecision(t *testing.T) {
	s := &Server{escalations: newEscalationRegistry()}
	var events []chatStreamEvent
	hook := s.stagePermissionRequestHook("task-1:planning", func(ev chatStreamEvent) {
		events = append(events, ev)
	})
	require.NotNil(t, hook)

	type result struct {
		allow bool
		err   error
	}
	done := make(chan result, 1)
	go func() {
		allow, err := hook(context.Background(), "Bash", `{"command":"ls"}`)
		done <- result{allow, err}
	}()

	// The hook emits a permission_request with an ID before it blocks.
	var id string
	require.Eventually(t, func() bool {
		s.escalations.mu.Lock()
		defer s.escalations.mu.Unlock()
		for pid := range s.escalations.pending {
			id = pid
			return true
		}
		return false
	}, time.Second, 5*time.Millisecond)

	require.Len(t, events, 1)
	require.NotNil(t, events[0].PermissionRequest)
	assert.Equal(t, "Bash", events[0].PermissionRequest.Name)
	assert.Equal(t, id, events[0].PermissionRequest.ID)

	require.True(t, s.escalations.resolve("task-1:planning", id, true))
	select {
	case r := <-done:
		require.NoError(t, r.err)
		assert.True(t, r.allow)
	case <-time.After(time.Second):
		t.Fatal("hook never returned after decision")
	}
}

// TestStagePermissionRequestHook_DeniesOnCancelledContext confirms a cancelled
// turn (closed tab / timeout) denies rather than hanging.
func TestStagePermissionRequestHook_DeniesOnCancelledContext(t *testing.T) {
	s := &Server{escalations: newEscalationRegistry()}
	hook := s.stagePermissionRequestHook("task-1:planning", func(chatStreamEvent) {})
	ctx, cancel := context.WithCancel(context.Background())

	done := make(chan bool, 1)
	go func() {
		allow, _ := hook(ctx, "Bash", "{}")
		done <- allow
	}()
	cancel()
	select {
	case allow := <-done:
		assert.False(t, allow, "a cancelled turn denies")
	case <-time.After(time.Second):
		t.Fatal("hook never returned after ctx cancel")
	}
}

// TestHandleStagePermissionDecision covers the HTTP endpoint: an approve for a
// pending request succeeds, and an unknown request ID is a 404.
func TestHandleStagePermissionDecision(t *testing.T) {
	s := &Server{escalations: newEscalationRegistry()}
	id, decision, cleanup := s.escalations.register("task-1:planning")
	defer cleanup()

	req := httptest.NewRequest(http.MethodPost, "/api/v1/projects/p/tasks/task-1/stages/planning/permission",
		bytes.NewReader(mustJSON(t, stagePermissionDecisionRequest{RequestID: id, Allow: true})))
	req.SetPathValue("taskId", "task-1")
	req.SetPathValue("stage", "planning")
	rec := httptest.NewRecorder()
	s.handleStagePermissionDecision()(rec, req)
	assert.Equal(t, http.StatusNoContent, rec.Code)
	assert.Equal(t, true, <-decision)

	req = httptest.NewRequest(http.MethodPost, "/api/v1/projects/p/tasks/task-1/stages/planning/permission",
		bytes.NewReader(mustJSON(t, stagePermissionDecisionRequest{RequestID: "nope", Allow: true})))
	req.SetPathValue("taskId", "task-1")
	req.SetPathValue("stage", "planning")
	rec = httptest.NewRecorder()
	s.handleStagePermissionDecision()(rec, req)
	assert.Equal(t, http.StatusNotFound, rec.Code)
}

// TestHandleStagePermissionDecision_ForeignSessionRejected confirms a decision
// posted to a different task's route can't resolve another session's request.
func TestHandleStagePermissionDecision_ForeignSessionRejected(t *testing.T) {
	s := &Server{escalations: newEscalationRegistry()}
	id, decision, cleanup := s.escalations.register("task-1:planning")
	defer cleanup()

	req := httptest.NewRequest(http.MethodPost, "/api/v1/projects/p/tasks/task-2/stages/planning/permission",
		bytes.NewReader(mustJSON(t, stagePermissionDecisionRequest{RequestID: id, Allow: true})))
	req.SetPathValue("taskId", "task-2")
	req.SetPathValue("stage", "planning")
	rec := httptest.NewRecorder()
	s.handleStagePermissionDecision()(rec, req)
	assert.Equal(t, http.StatusNotFound, rec.Code)

	select {
	case <-decision:
		t.Fatal("a foreign session must not have resolved the request")
	default:
	}
}

func mustJSON(t *testing.T, v any) []byte {
	t.Helper()
	b, err := json.Marshal(v)
	require.NoError(t, err)
	return b
}

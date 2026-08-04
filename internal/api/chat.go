package api

import (
	"encoding/json"
	"fmt"
	"net/http"

	"github.com/timmersuk/llm-workbench/internal/agentrunner"
	"github.com/timmersuk/llm-workbench/internal/chat"
)

// defaultChatExecutor is the agentRunners key handleChatCompletions falls
// back to when the request doesn't name one — the OpenAI-compatible
// local-LLM path (internal/agentrunner.ChatClientRunner), registered under
// this key in cmd/server/main.go.
const defaultChatExecutor = "local"

// chatStreamEvent is the shape re-emitted to the browser for each streamed
// chat completion piece, decoupled from whichever upstream provider is
// actually configured — the frontend only ever sees this shape, never the
// upstream's own wire format. At most one of Content/ReasoningContent/
// ToolCall/ToolActivity is set per event; Error is only set on the final
// event of a stream that failed partway through: whatever content/
// reasoning_content already streamed is left in place, not discarded.
// ToolCall is only ever set by the stage-conversation handlers
// (stage_conversation.go), when the stage's registered Draft tool is called
// — the free-floating chat endpoint below never registers any tools, so it
// never populates it, but shares the same event shape. ToolActivity carries
// the reviewing/interviewing agent's INTERMEDIATE tool calls and their
// results (each read_file/grep_search/glob/bash the loop actually ran),
// distinct from the single final Draft ToolCall — so a client can render the
// agent's checks live, not just its prose.
type chatStreamEvent struct {
	Content          string                 `json:"content,omitempty"`
	ReasoningContent string                 `json:"reasoning_content,omitempty"`
	ToolCall         *chatToolCallEvent     `json:"tool_call,omitempty"`
	ToolActivity     *chatToolActivityEvent `json:"tool_activity,omitempty"`
	// Usage carries the real token count once available (only ever on the
	// final chunk of a stream — see chat.Delta.Usage), for the frontend's
	// live token-estimate indicator (docs/adr/0018) to snap to once a turn
	// completes rather than staying an approximation forever. nil for every
	// executor/chunk that doesn't report it (the claude/codex CLI paths
	// never do), which the frontend treats as "keep estimating."
	Usage *chatUsageEvent `json:"usage,omitempty"`
	Error string          `json:"error,omitempty"`
}

// chatUsageEvent is the wire shape of chat.Usage's total, the only figure
// the frontend's token counter actually needs.
type chatUsageEvent struct {
	TotalTokens int `json:"total_tokens"`
}

// usageEvent adapts a possibly-nil chat.Usage (only ever set on the final
// chunk of a stream, see chat.Delta.Usage) into the wire shape, or nil when
// there's nothing to report yet.
func usageEvent(u *chat.Usage) *chatUsageEvent {
	if u == nil {
		return nil
	}
	return &chatUsageEvent{TotalTokens: u.TotalTokens}
}

// chatToolActivityEvent is the wire shape of one intermediate tool step the
// agent's loop executed. Phase is "call" (Name/Arguments set, the tool's raw
// JSON input) or "result" (Name/Result set, IsError flags a failed call).
// Unlike a Draft chatToolCallEvent, a single turn can emit many of these —
// they narrate the agent's actual actions (e.g. running the test suite)
// before it either replies in prose or proposes its Draft. ID is the same
// per-call correlation key agentrunner.RunInput.OnToolCall/OnToolResult
// carries (see its doc comment) — a "call" and its later "result" share it,
// so the frontend can attach a result to the exact call it belongs to
// rather than assuming they always arrive call-then-result in order.
type chatToolActivityEvent struct {
	ID        string `json:"id"`
	Phase     string `json:"phase"` // "call" | "result"
	Name      string `json:"name"`
	Arguments string `json:"arguments,omitempty"`
	Result    string `json:"result,omitempty"`
	IsError   bool   `json:"is_error,omitempty"`
}

// chatToolCallEvent is the wire shape of a proposed Draft's tool call:
// Arguments is the raw JSON-string of the proposed fields, left for the
// frontend to parse per-stage (RequirementsDraft vs Plan shape) rather than
// decoded server-side.
type chatToolCallEvent struct {
	Name      string `json:"name"`
	Arguments string `json:"arguments"`
}

// chatCompletionRequest is the request body for handleChatCompletions.
// SessionKey is required — the server holds this conversation's history
// keyed by it (agentrunner.RunInput.SessionKey), so the client sends only
// the newest turn rather than resending full history every call. Executor
// selects which registered agentRunners entry produces the reply,
// defaulting to defaultChatExecutor when empty. History is optional and
// normally omitted — free chat has no durable server-side transcript to
// resend from, so it's only populated by the frontend immediately after a
// delete/edit/regenerate action closes the session (closeChatSession):
// RunInput.History is "only consulted when the runner has no live session"
// (agentrunner/runner.go), so sending it alongside a live session is a
// harmless no-op, and sending it right after an eviction is what makes the
// correction actually stick.
type chatCompletionRequest struct {
	Content    string         `json:"content"`
	Model      string         `json:"model"`
	Executor   string         `json:"executor,omitempty"`
	SessionKey string         `json:"session_key"`
	History    []chat.Message `json:"history,omitempty"`
	Effort     string         `json:"effort,omitempty"`
}

func (s *Server) handleChatCompletions() http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		var req chatCompletionRequest
		if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
			writeAPIError(w, http.StatusBadRequest, "invalid request body")
			return
		}
		if req.SessionKey == "" {
			writeAPIError(w, http.StatusBadRequest, "session_key is required")
			return
		}

		executorKey := req.Executor
		if executorKey == "" {
			executorKey = defaultChatExecutor
		}
		runner, ok := s.AgentRunners[executorKey]
		if !ok {
			writeAPIError(w, http.StatusBadRequest, fmt.Sprintf("unknown executor %q", executorKey))
			return
		}

		flusher, ok := w.(http.Flusher)
		if !ok {
			writeAPIError(w, http.StatusInternalServerError, "streaming unsupported")
			return
		}

		w.Header().Set("Content-Type", "text/event-stream")
		w.Header().Set("Cache-Control", "no-cache")
		w.Header().Set("Connection", "keep-alive")
		w.WriteHeader(http.StatusOK)

		writeEvent := func(ev chatStreamEvent) {
			data, _ := json.Marshal(ev)
			fmt.Fprintf(w, "data: %s\n\n", data)
			flusher.Flush()
		}

		// Free chat has no per-task project to resolve a workspace from
		// (agentrunner.ResolveWorkspace, used by stage_conversation.go),
		// so a Claude Code selection gets reposRoot itself as its
		// workspace — read access rooted at the whole sibling-repos
		// directory rather than one specific repo. ChatClientRunner
		// ignores this field entirely.
		_, err := runner.Run(r.Context(), agentrunner.RunInput{
			SessionKey:      req.SessionKey,
			Workspace:       s.ReposRoot,
			UserMessage:     req.Content,
			Model:           req.Model,
			History:         req.History,
			ReasoningEffort: agentrunner.ReasoningEffort(req.Effort),
		}, func(d chat.Delta) error {
			writeEvent(chatStreamEvent{Content: d.Content, ReasoningContent: d.ReasoningContent, Usage: usageEvent(d.Usage)})
			return nil
		})
		if err != nil {
			// Headers (200 OK) are already sent by this point, so a
			// failed stream can't surface as an HTTP error status — the
			// error is relayed as a final SSE event instead, alongside
			// whatever content/reasoning_content already streamed.
			writeEvent(chatStreamEvent{Error: err.Error()})
		}
	}
}

// chatSessionCloseRequest is the request body for handleCloseChatSession.
type chatSessionCloseRequest struct {
	SessionKey string `json:"session_key"`
}

// handleCloseChatSession discards a free-chat session's held history
// (whichever registered runner actually holds one for this key — safe to
// call CloseSession on every entry, since it's a no-op for a key a given
// runner never saw) — the "New chat" action's server-side counterpart.
func (s *Server) handleCloseChatSession() http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		var req chatSessionCloseRequest
		if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
			writeAPIError(w, http.StatusBadRequest, "invalid request body")
			return
		}
		if req.SessionKey == "" {
			writeAPIError(w, http.StatusBadRequest, "session_key is required")
			return
		}
		s.closeSessions(req.SessionKey)
		w.WriteHeader(http.StatusNoContent)
	}
}

package api

import (
	"encoding/json"
	"fmt"
	"net/http"

	"github.com/timmersuk/llm-workbench/internal/agentrunner"
	"github.com/timmersuk/llm-workbench/internal/chat"
)

// defaultChatExecutor is the agentRunners key handleChatCompletions/
// handleListModels fall back to when the request doesn't name one — the
// OpenAI-compatible local-LLM path (internal/agentrunner.ChatClientRunner),
// registered under this key in cmd/server/main.go.
const defaultChatExecutor = "local"

// chatStreamEvent is the shape re-emitted to the browser for each streamed
// chat completion piece, decoupled from whichever upstream provider is
// actually configured — the frontend only ever sees this shape, never the
// upstream's own wire format. Error is only set on the final event of a
// stream that failed partway through: whatever content/reasoning_content
// already streamed is left in place, not discarded. ToolCall is only ever
// set by handlePostStageMessage (stage_conversation.go), when GrillMe/
// Planning Mode's registered tool is called — the free-floating chat
// endpoint below never registers any tools, so it never populates this
// field, but shares the same event shape.
type chatStreamEvent struct {
	Content          string             `json:"content,omitempty"`
	ReasoningContent string             `json:"reasoning_content,omitempty"`
	ToolCall         *chatToolCallEvent `json:"tool_call,omitempty"`
	Error            string             `json:"error,omitempty"`
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
// defaulting to defaultChatExecutor when empty.
type chatCompletionRequest struct {
	Content    string `json:"content"`
	Model      string `json:"model"`
	Executor   string `json:"executor,omitempty"`
	SessionKey string `json:"session_key"`
}

func handleChatCompletions(agentRunners map[string]agentrunner.AgentRunner, reposRoot string) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		var req chatCompletionRequest
		if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
			http.Error(w, "invalid request body", http.StatusBadRequest)
			return
		}
		if req.SessionKey == "" {
			http.Error(w, "session_key is required", http.StatusBadRequest)
			return
		}

		executorKey := req.Executor
		if executorKey == "" {
			executorKey = defaultChatExecutor
		}
		runner, ok := agentRunners[executorKey]
		if !ok {
			http.Error(w, fmt.Sprintf("unknown executor %q", executorKey), http.StatusBadRequest)
			return
		}

		flusher, ok := w.(http.Flusher)
		if !ok {
			http.Error(w, "streaming unsupported", http.StatusInternalServerError)
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
			SessionKey:  req.SessionKey,
			Workspace:   reposRoot,
			UserMessage: req.Content,
			Model:       req.Model,
		}, func(d chat.Delta) error {
			writeEvent(chatStreamEvent{Content: d.Content, ReasoningContent: d.ReasoningContent})
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
func handleCloseChatSession(agentRunners map[string]agentrunner.AgentRunner) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		var req chatSessionCloseRequest
		if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
			http.Error(w, "invalid request body", http.StatusBadRequest)
			return
		}
		if req.SessionKey == "" {
			http.Error(w, "session_key is required", http.StatusBadRequest)
			return
		}
		closeSessions(agentRunners, req.SessionKey)
		w.WriteHeader(http.StatusNoContent)
	}
}

func handleListModels(agentRunners map[string]agentrunner.AgentRunner) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		runner, ok := agentRunners[defaultChatExecutor]
		if !ok {
			http.Error(w, "no local chat executor registered", http.StatusInternalServerError)
			return
		}
		models, err := runner.ListModels(r.Context())
		if err != nil {
			http.Error(w, "listing models failed: "+err.Error(), http.StatusBadGateway)
			return
		}
		writeJSON(w, http.StatusOK, map[string][]string{"models": models})
	}
}

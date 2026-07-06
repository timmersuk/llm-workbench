package api

import (
	"encoding/json"
	"fmt"
	"net/http"

	"github.com/timmersuk/llm-workbench/internal/chat"
)

// chatStreamEvent is the shape re-emitted to the browser for each streamed
// chat completion piece, decoupled from whichever upstream provider is
// actually configured — the frontend only ever sees this shape, never the
// upstream's own wire format. Error is only set on the final event of a
// stream that failed partway through: whatever content/reasoning_content
// already streamed is left in place, not discarded.
type chatStreamEvent struct {
	Content          string `json:"content,omitempty"`
	ReasoningContent string `json:"reasoning_content,omitempty"`
	Error            string `json:"error,omitempty"`
}

func handleChatCompletions(completer chat.ChatClient) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		var req chat.CompletionRequest
		if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
			http.Error(w, "invalid request body", http.StatusBadRequest)
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

		err := completer.StreamChatCompletion(r.Context(), req, func(d chat.Delta) error {
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

func handleListModels(completer chat.ChatClient) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		models, err := completer.ListModels(r.Context())
		if err != nil {
			http.Error(w, "listing models failed: "+err.Error(), http.StatusBadGateway)
			return
		}
		writeJSON(w, http.StatusOK, map[string][]string{"models": models})
	}
}

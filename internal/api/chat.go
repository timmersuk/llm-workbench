package api

import (
	"encoding/json"
	"net/http"

	"github.com/timmersuk/llm-workbench/internal/chat"
)

func handleChatCompletions(completer ChatCompleter) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		var req chat.CompletionRequest
		if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
			http.Error(w, "invalid request body", http.StatusBadRequest)
			return
		}

		resp, err := completer.CreateChatCompletion(r.Context(), req)
		if err != nil {
			http.Error(w, "upstream chat completion failed: "+err.Error(), http.StatusBadGateway)
			return
		}
		writeJSON(w, http.StatusOK, resp)
	}
}

func handleListModels(completer ChatCompleter) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		models, err := completer.ListModels(r.Context())
		if err != nil {
			http.Error(w, "listing models failed: "+err.Error(), http.StatusBadGateway)
			return
		}
		writeJSON(w, http.StatusOK, map[string][]string{"models": models})
	}
}

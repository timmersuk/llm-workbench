package api

import (
	"encoding/json"
	"net/http"

	"github.com/timmersuk/llm-workbench/internal/project"
)

func handleListProjects(lister ProjectStore) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		result, err := lister.List()
		if err != nil {
			http.Error(w, "internal error", http.StatusInternalServerError)
			return
		}
		writeJSON(w, http.StatusOK, result)
	}
}

func handleGetProject(lister ProjectStore) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		p, err := lister.Get(r.PathValue("id"))
		if err != nil {
			writeGetError(w, err)
			return
		}
		writeJSON(w, http.StatusOK, p)
	}
}

func handleCreateProject(store ProjectStore) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		var in project.CreateInput
		if err := json.NewDecoder(r.Body).Decode(&in); err != nil {
			http.Error(w, "invalid request body", http.StatusBadRequest)
			return
		}

		created, err := store.Create(in)
		if err != nil {
			writeMutationError(w, err)
			return
		}
		writeJSON(w, http.StatusCreated, created)
	}
}

func handleUpdateProject(store ProjectStore) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		var in project.UpdateInput
		if err := json.NewDecoder(r.Body).Decode(&in); err != nil {
			http.Error(w, "invalid request body", http.StatusBadRequest)
			return
		}

		updated, err := store.Update(r.PathValue("id"), in)
		if err != nil {
			writeMutationError(w, err)
			return
		}
		writeJSON(w, http.StatusOK, updated)
	}
}

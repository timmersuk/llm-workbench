package api

import (
	"encoding/json"
	"errors"
	"io/fs"
	"net/http"

	"github.com/timmersuk/llm-workbench/internal/project"
	"github.com/timmersuk/llm-workbench/internal/task"
)

func writeJSON(w http.ResponseWriter, status int, v any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(v)
}

func writeGetError(w http.ResponseWriter, err error) {
	switch {
	case errors.Is(err, task.ErrInvalidID), errors.Is(err, project.ErrInvalidID):
		http.Error(w, err.Error(), http.StatusBadRequest)
	case errors.Is(err, fs.ErrNotExist):
		http.Error(w, "not found", http.StatusNotFound)
	default:
		http.Error(w, "internal error", http.StatusInternalServerError)
	}
}

// writeMutationError maps Create/Update errors to HTTP status, the same
// centralized-per-resource approach as writeGetError: bad input (invalid
// id, missing name, id mismatch) -> 400, id collision -> 409, missing
// target (edit of a nonexistent id) -> 404, anything else -> 500 generic.
func writeMutationError(w http.ResponseWriter, err error) {
	switch {
	case errors.Is(err, task.ErrInvalidID), errors.Is(err, project.ErrInvalidID),
		errors.Is(err, project.ErrMissingName), errors.Is(err, task.ErrIDMismatch):
		http.Error(w, err.Error(), http.StatusBadRequest)
	case errors.Is(err, task.ErrAlreadyExists), errors.Is(err, project.ErrAlreadyExists):
		http.Error(w, err.Error(), http.StatusConflict)
	case errors.Is(err, fs.ErrNotExist):
		http.Error(w, "not found", http.StatusNotFound)
	default:
		http.Error(w, "internal error", http.StatusInternalServerError)
	}
}

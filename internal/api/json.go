package api

import (
	"encoding/json"
	"errors"
	"io/fs"
	"net/http"

	"github.com/timmersuk/llm-workbench/internal/knowledge"
	"github.com/timmersuk/llm-workbench/internal/project"
	"github.com/timmersuk/llm-workbench/internal/task"
)

func writeJSON(w http.ResponseWriter, status int, v any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(v)
}

// apiErrorBody is the wire shape of every writeAPIError response — a
// single, simple JSON envelope in place of the plain-text bodies
// http.Error produces, so the frontend can reliably parse a message
// instead of reading raw response text. code is a small fixed slug per
// status category, not a message-specific enum (see errorCodeForStatus) —
// deliberately coarse, matching the "simple flat shape" decision recorded
// in docs/engineering conventions.md's API error responses section.
type apiErrorBody struct {
	Error apiError `json:"error"`
}

type apiError struct {
	Code    string `json:"code"`
	Message string `json:"message"`
}

// errorCodeForStatus maps an HTTP status to apiErrorBody's code slug.
func errorCodeForStatus(status int) string {
	switch status {
	case http.StatusBadRequest:
		return "bad_request"
	case http.StatusConflict:
		return "conflict"
	case http.StatusNotFound:
		return "not_found"
	case http.StatusBadGateway:
		return "bad_gateway"
	default:
		return "internal_error"
	}
}

// writeAPIError is this package's replacement for the standard library's
// http.Error: same status code and message text every caller already
// passed, wrapped in apiErrorBody instead of written as a plain-text body.
func writeAPIError(w http.ResponseWriter, status int, message string) {
	writeJSON(w, status, apiErrorBody{Error: apiError{Code: errorCodeForStatus(status), Message: message}})
}

func writeGetError(w http.ResponseWriter, err error) {
	switch {
	case errors.Is(err, task.ErrInvalidID), errors.Is(err, project.ErrInvalidID),
		errors.Is(err, task.ErrInvalidStage), errors.Is(err, knowledge.ErrInvalidConceptID):
		writeAPIError(w, http.StatusBadRequest, err.Error())
	case errors.Is(err, task.ErrWrongStage):
		writeAPIError(w, http.StatusConflict, err.Error())
	case errors.Is(err, fs.ErrNotExist):
		writeAPIError(w, http.StatusNotFound, "not found")
	default:
		writeAPIError(w, http.StatusInternalServerError, "internal error")
	}
}

// writeMutationError maps Create/Update errors to HTTP status, the same
// centralized-per-resource approach as writeGetError: bad input (invalid
// id, missing name, id mismatch, invalid stage) -> 400, id collision or
// wrong-stage Finalize/Revise -> 409, missing target (edit of a
// nonexistent id) -> 404, anything else -> 500 generic.
func writeMutationError(w http.ResponseWriter, err error) {
	switch {
	case errors.Is(err, task.ErrInvalidID), errors.Is(err, project.ErrInvalidID),
		errors.Is(err, project.ErrMissingName), errors.Is(err, task.ErrIDMismatch),
		errors.Is(err, task.ErrInvalidStage), errors.Is(err, task.ErrStageImmutable),
		errors.Is(err, knowledge.ErrInvalidConceptID):
		writeAPIError(w, http.StatusBadRequest, err.Error())
	case errors.Is(err, task.ErrAlreadyExists), errors.Is(err, project.ErrAlreadyExists),
		errors.Is(err, task.ErrWrongStage):
		writeAPIError(w, http.StatusConflict, err.Error())
	case errors.Is(err, fs.ErrNotExist):
		writeAPIError(w, http.StatusNotFound, "not found")
	default:
		writeAPIError(w, http.StatusInternalServerError, "internal error")
	}
}

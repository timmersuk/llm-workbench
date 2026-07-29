package api

import (
	"encoding/json"
	"net/http"

	"github.com/timmersuk/llm-workbench/internal/project"
)

func (s *Server) handleListProjects() http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		result, err := s.Projects.List()
		if err != nil {
			writeAPIError(w, http.StatusInternalServerError, "internal error")
			return
		}
		writeJSON(w, http.StatusOK, result)
	}
}

func (s *Server) handleGetProject() http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		p, err := s.Projects.Get(r.PathValue("id"))
		if err != nil {
			writeGetError(w, err)
			return
		}
		writeJSON(w, http.StatusOK, p)
	}
}

func (s *Server) handleCreateProject() http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		var in project.CreateInput
		if err := json.NewDecoder(r.Body).Decode(&in); err != nil {
			writeAPIError(w, http.StatusBadRequest, "invalid request body")
			return
		}

		created, err := s.Projects.Create(in)
		if err != nil {
			writeMutationError(w, err)
			return
		}
		writeJSON(w, http.StatusCreated, created)
	}
}

func (s *Server) handleUpdateProject() http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		var in project.UpdateInput
		if err := json.NewDecoder(r.Body).Decode(&in); err != nil {
			writeAPIError(w, http.StatusBadRequest, "invalid request body")
			return
		}

		updated, err := s.Projects.Update(r.PathValue("id"), in)
		if err != nil {
			writeMutationError(w, err)
			return
		}
		writeJSON(w, http.StatusOK, updated)
	}
}

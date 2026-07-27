package api

import (
	"encoding/json"
	"net/http"

	"github.com/timmersuk/llm-workbench/internal/task"
)

// resolveTaskStore confirms the project named by projectId exists, then
// returns the shared TaskStore singleton (s.Tasks) for the caller to
// operate against, always passing projectId explicitly on every call.
// Returns false (having already written a response) if the project doesn't
// exist or the id is otherwise invalid. A method rather than a free
// function taking ProjectStore/TaskStore as parameters — the same shape as
// stage_conversation.go's helpers, folded into the Server-methods
// refactor even though it wasn't one of the five originally named in
// docs/milestones/done/milestone8b.md's scan, since it re-passes the exact
// same invariant deps to a dozen call sites (docs/adr/0016).
func (s *Server) resolveTaskStore(w http.ResponseWriter, projectId string) (TaskStore, bool) {
	if _, err := s.Projects.Get(projectId); err != nil {
		writeGetError(w, err)
		return nil, false
	}
	return s.Tasks, true
}

func (s *Server) handleListProjectTasks() http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		projectId := r.PathValue("projectId")
		store, ok := s.resolveTaskStore(w, projectId)
		if !ok {
			return
		}

		result, err := store.List(projectId)
		if err != nil {
			http.Error(w, "internal error", http.StatusInternalServerError)
			return
		}
		writeJSON(w, http.StatusOK, result)
	}
}

func (s *Server) handleGetProjectTask() http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		projectId := r.PathValue("projectId")
		store, ok := s.resolveTaskStore(w, projectId)
		if !ok {
			return
		}

		t, err := store.Get(projectId, r.PathValue("taskId"))
		if err != nil {
			writeGetError(w, err)
			return
		}
		writeJSON(w, http.StatusOK, t)
	}
}

func (s *Server) handleCreateProjectTask() http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		projectId := r.PathValue("projectId")
		store, ok := s.resolveTaskStore(w, projectId)
		if !ok {
			return
		}

		var t task.Task
		if err := json.NewDecoder(r.Body).Decode(&t); err != nil {
			http.Error(w, "invalid request body", http.StatusBadRequest)
			return
		}
		t.Project = projectId

		created, err := store.Create(projectId, t)
		if err != nil {
			writeMutationError(w, err)
			return
		}
		writeJSON(w, http.StatusCreated, created)
	}
}

func (s *Server) handleUpdateProjectTask() http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		projectId := r.PathValue("projectId")
		store, ok := s.resolveTaskStore(w, projectId)
		if !ok {
			return
		}

		var t task.Task
		if err := json.NewDecoder(r.Body).Decode(&t); err != nil {
			http.Error(w, "invalid request body", http.StatusBadRequest)
			return
		}
		t.Project = projectId

		updated, err := store.Update(projectId, r.PathValue("taskId"), t)
		if err != nil {
			writeMutationError(w, err)
			return
		}
		writeJSON(w, http.StatusOK, updated)
	}
}

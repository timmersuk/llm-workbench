package api

import "net/http"

// handleGetTaskContext returns the task's context.yaml, produced once
// GrillMe's Draft has been Finalized. 404 (via writeGetError's fs.ErrNotExist
// case) if requirements haven't been finalized yet.
func (s *Server) handleGetTaskContext() http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		projectId := r.PathValue("projectId")
		store, ok := s.resolveTaskStore(w, projectId)
		if !ok {
			return
		}

		ctx, err := store.GetContext(projectId, r.PathValue("taskId"))
		if err != nil {
			writeGetError(w, err)
			return
		}
		writeJSON(w, http.StatusOK, ctx)
	}
}

// handleGetTaskPlan returns the task's plan.yaml, produced once Planning
// Mode's Draft has been Finalized. 404 if planning hasn't been finalized
// yet.
func (s *Server) handleGetTaskPlan() http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		projectId := r.PathValue("projectId")
		store, ok := s.resolveTaskStore(w, projectId)
		if !ok {
			return
		}

		plan, err := store.GetPlan(projectId, r.PathValue("taskId"))
		if err != nil {
			writeGetError(w, err)
			return
		}
		writeJSON(w, http.StatusOK, plan)
	}
}

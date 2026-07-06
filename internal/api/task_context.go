package api

import "net/http"

// handleGetTaskContext returns the task's context.yaml, produced once
// GrillMe's Draft has been Finalized. 404 (via writeGetError's fs.ErrNotExist
// case) if requirements haven't been finalized yet.
func handleGetTaskContext(projects ProjectStore, factory TaskStoreFactory) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		store, ok := resolveTaskStore(w, projects, factory, r.PathValue("projectId"))
		if !ok {
			return
		}

		ctx, err := store.GetContext(r.PathValue("taskId"))
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
func handleGetTaskPlan(projects ProjectStore, factory TaskStoreFactory) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		store, ok := resolveTaskStore(w, projects, factory, r.PathValue("projectId"))
		if !ok {
			return
		}

		plan, err := store.GetPlan(r.PathValue("taskId"))
		if err != nil {
			writeGetError(w, err)
			return
		}
		writeJSON(w, http.StatusOK, plan)
	}
}

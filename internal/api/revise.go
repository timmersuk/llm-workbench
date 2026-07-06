package api

import "net/http"

// handleReviseRequirements is the "Revise Requirements" action
// (CONTEXT.md's "Revise"): moves stage back from planning to requirements,
// reopening the requirements Conversation. 409 if the task isn't currently
// in planning stage.
func handleReviseRequirements(projects ProjectStore, factory TaskStoreFactory) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		store, ok := resolveTaskStore(w, projects, factory, r.PathValue("projectId"))
		if !ok {
			return
		}

		updated, err := store.ReviseToRequirements(r.PathValue("taskId"))
		if err != nil {
			writeMutationError(w, err)
			return
		}
		writeJSON(w, http.StatusOK, updated)
	}
}

// handleRevisePlan is the "Revise Plan" action: moves stage back from
// implementation/review to planning. 409 if the task isn't currently in
// implementation or review stage.
func handleRevisePlan(projects ProjectStore, factory TaskStoreFactory) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		store, ok := resolveTaskStore(w, projects, factory, r.PathValue("projectId"))
		if !ok {
			return
		}

		updated, err := store.ReviseToPlanning(r.PathValue("taskId"))
		if err != nil {
			writeMutationError(w, err)
			return
		}
		writeJSON(w, http.StatusOK, updated)
	}
}

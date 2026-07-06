package api

import (
	"encoding/json"
	"net/http"

	"github.com/timmersuk/llm-workbench/internal/task"
)

// finalizeRequirementsResponse is the wire shape for a successful
// GrillMe Finalize: the task (now advanced to stage: planning) plus the
// context.yaml just written.
type finalizeRequirementsResponse struct {
	Task    task.Task    `json:"task"`
	Context task.Context `json:"context"`
}

// finalizePlanResponse mirrors finalizeRequirementsResponse for Planning
// Mode's Finalize (task now advanced to stage: implementation).
type finalizePlanResponse struct {
	Task task.Task `json:"task"`
	Plan task.Plan `json:"plan"`
}

// handleFinalizeRequirements is the human "Finalize" action for GrillMe
// (CONTEXT.md): persists the (possibly human-edited) Draft's task.yaml
// fields and context.yaml, and advances stage from requirements to
// planning. 409 if the task isn't currently in requirements stage.
func handleFinalizeRequirements(projects ProjectStore, factory TaskStoreFactory) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		store, ok := resolveTaskStore(w, projects, factory, r.PathValue("projectId"))
		if !ok {
			return
		}

		var draft task.RequirementsDraft
		if err := json.NewDecoder(r.Body).Decode(&draft); err != nil {
			http.Error(w, "invalid request body", http.StatusBadRequest)
			return
		}

		taskId := r.PathValue("taskId")
		updated, err := store.FinalizeRequirements(taskId, draft)
		if err != nil {
			writeMutationError(w, err)
			return
		}

		ctx, err := store.GetContext(taskId)
		if err != nil {
			writeGetError(w, err)
			return
		}
		writeJSON(w, http.StatusOK, finalizeRequirementsResponse{Task: updated, Context: ctx})
	}
}

// handleFinalizePlan is the human "Finalize" action for Planning Mode:
// persists plan.yaml and advances stage from planning to implementation.
// 409 if the task isn't currently in planning stage.
func handleFinalizePlan(projects ProjectStore, factory TaskStoreFactory) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		store, ok := resolveTaskStore(w, projects, factory, r.PathValue("projectId"))
		if !ok {
			return
		}

		var plan task.Plan
		if err := json.NewDecoder(r.Body).Decode(&plan); err != nil {
			http.Error(w, "invalid request body", http.StatusBadRequest)
			return
		}

		taskId := r.PathValue("taskId")
		updated, err := store.FinalizePlan(taskId, plan)
		if err != nil {
			writeMutationError(w, err)
			return
		}

		savedPlan, err := store.GetPlan(taskId)
		if err != nil {
			writeGetError(w, err)
			return
		}
		writeJSON(w, http.StatusOK, finalizePlanResponse{Task: updated, Plan: savedPlan})
	}
}

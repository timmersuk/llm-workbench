package api

import (
	"encoding/json"
	"net/http"

	"github.com/timmersuk/llm-workbench/internal/agentrunner"
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

// closeSessions calls CloseSession(sessionKey) on every runner in
// agentRunners — safe even for runners that never held a session under
// that key, since a stage's conversation (or free-chat session) could
// have used any (or none) of them across its turns.
func closeSessions(agentRunners map[string]agentrunner.AgentRunner, sessionKey string) {
	for _, runner := range agentRunners {
		runner.CloseSession(sessionKey)
	}
}

// handleFinalizeRequirements is the human "Finalize" action for GrillMe
// (CONTEXT.md): persists the (possibly human-edited) Draft's task.yaml
// fields and context.yaml, and advances stage from requirements to
// planning. 409 if the task isn't currently in requirements stage. A
// stage conversation is conceptually done once finalized, so its agent
// session (whichever executor produced it, if any) is torn down here —
// deliberately not done on Revise, which resumes the same Conversation.
func handleFinalizeRequirements(projects ProjectStore, factory TaskStoreFactory, agentRunners map[string]agentrunner.AgentRunner) http.HandlerFunc {
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
		closeSessions(agentRunners, taskId+":"+task.StageRequirements)

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
// 409 if the task isn't currently in planning stage. See
// handleFinalizeRequirements's comment for the CloseSession rationale.
func handleFinalizePlan(projects ProjectStore, factory TaskStoreFactory, agentRunners map[string]agentrunner.AgentRunner) http.HandlerFunc {
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
		closeSessions(agentRunners, taskId+":"+task.StagePlanning)

		savedPlan, err := store.GetPlan(taskId)
		if err != nil {
			writeGetError(w, err)
			return
		}
		writeJSON(w, http.StatusOK, finalizePlanResponse{Task: updated, Plan: savedPlan})
	}
}

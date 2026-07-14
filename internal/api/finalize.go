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

// finalizeReviewResponse is the wire shape for a Review Finalize: the task
// (now moved by the verdict — complete/implementation/requirements) plus the
// review-NNN.yaml just recorded. Unlike context/plan, the review record is
// what the terminal "complete" screen reads back to show the verdict notes,
// so it travels in the response rather than being re-fetched by id.
type finalizeReviewResponse struct {
	Task   task.Task   `json:"task"`
	Review task.Review `json:"review"`
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

// handleFinalizeReview is the human "Finalize" action for a Review
// conversation: records the verdict (reviews/review-NNN.yaml) and moves the
// task by its decision — approved→complete, needs_changes→implementation,
// rejected→requirements (task.FinalizeReview). 409 if the task isn't at
// stage review. See handleFinalizeRequirements's comment for the CloseSession
// rationale — a finalized review conversation is done, so its agent session
// is torn down. The just-recorded review is read back so the response can
// carry the verdict notes the "complete" screen shows without a second call.
func handleFinalizeReview(projects ProjectStore, factory TaskStoreFactory, agentRunners map[string]agentrunner.AgentRunner) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		store, ok := resolveTaskStore(w, projects, factory, r.PathValue("projectId"))
		if !ok {
			return
		}

		var draft task.ReviewDraft
		if err := json.NewDecoder(r.Body).Decode(&draft); err != nil {
			http.Error(w, "invalid request body", http.StatusBadRequest)
			return
		}

		taskId := r.PathValue("taskId")
		updated, err := store.FinalizeReview(taskId, draft)
		if err != nil {
			writeMutationError(w, err)
			return
		}
		closeSessions(agentRunners, taskId+":"+task.StageReview)

		// FinalizeReview appended a fresh review-NNN.yaml; the last entry is
		// that verdict (ListReviews sorts ascending by zero-padded id).
		reviews, err := store.ListReviews(taskId)
		if err != nil {
			writeGetError(w, err)
			return
		}
		var latest task.Review
		if len(reviews) > 0 {
			latest = reviews[len(reviews)-1]
		}
		writeJSON(w, http.StatusOK, finalizeReviewResponse{Task: updated, Review: latest})
	}
}

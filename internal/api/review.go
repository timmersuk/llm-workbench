package api

import (
	"fmt"
	"net/http"

	"github.com/timmersuk/llm-workbench/internal/agentrunner"
	"github.com/timmersuk/llm-workbench/internal/task"
)

// reviewDiffResponse is the wire shape for handleReviewDiff — the raw patch
// of the execution currently under review. The branch/commit/changed-file
// summary the ReviewPanel shows alongside it comes from the executions list
// (handleListExecutions); this endpoint carries only the full diff text, kept
// out of that list so the potentially-large patch is fetched on demand (the
// panel renders it inside a collapsed "View diff", docs/milestones/done/milestone6.md
// PR 3 decision 1).
type reviewDiffResponse struct {
	Patch string `json:"patch"`
}

// handleListReviews returns every recorded review verdict for a task, oldest
// first — a file-for-file mirror of handleListExecutions. The terminal
// "merged" screen reads this to show the latest verdict's notes on a fresh
// visit, when no just-finalized response is in hand (docs/milestones/done/milestone6.md
// PR 3 decision 4).
func (s *Server) handleListReviews() http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		store, ok := s.resolveTaskStore(w, r.PathValue("projectId"))
		if !ok {
			return
		}

		reviews, err := store.ListReviews(r.PathValue("taskId"))
		if err != nil {
			writeGetError(w, err)
			return
		}
		writeJSON(w, http.StatusOK, map[string][]task.Review{"reviews": reviews})
	}
}

// handleReviewDiff returns the full patch of the task's most recent execution
// — the diff the ReviewPanel shows before the review conversation starts, so
// the human can read the actual change without leaving the app. It resolves
// the same worktree the review conversation confines its tools to
// (ResolveReviewWorkspace) and collects the same patch fed to the agent's
// prompt (CollectExecutionPatch), just handed to the browser instead. 404 if
// the task has no execution to review yet.
func (s *Server) handleReviewDiff() http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		projectId := r.PathValue("projectId")
		taskId := r.PathValue("taskId")

		proj, err := s.Projects.Get(projectId)
		if err != nil {
			writeGetError(w, err)
			return
		}
		defaultBranch, err := s.ensureDefaultBranch(r.Context(), proj)
		if err != nil {
			http.Error(w, fmt.Sprintf("determining default branch: %v", err), http.StatusInternalServerError)
			return
		}
		root, err := s.Projects.TasksRoot(projectId)
		if err != nil {
			writeGetError(w, err)
			return
		}
		store := s.TaskStores(root)

		executions, err := store.ListExecutions(taskId)
		if err != nil {
			writeGetError(w, err)
			return
		}
		if len(executions) == 0 {
			http.Error(w, "no execution to review", http.StatusNotFound)
			return
		}
		// ListExecutions sorts ascending by the zero-padded id, so the last
		// entry is the most recent attempt — the one that advanced the task
		// to review (same selection buildReviewContext makes).
		latest := executions[len(executions)-1]

		ws, err := agentrunner.ResolveReviewWorkspace(r.Context(), s.ReposRoot, proj.Repositories, latest.ExecutionID, defaultBranch)
		if err != nil {
			http.Error(w, fmt.Sprintf("resolving review workspace: %v", err), http.StatusInternalServerError)
			return
		}
		_, patch, err := agentrunner.CollectExecutionPatch(r.Context(), ws, latest.Output.ForkedFromBranch)
		if err != nil {
			http.Error(w, fmt.Sprintf("collecting execution diff: %v", err), http.StatusInternalServerError)
			return
		}
		writeJSON(w, http.StatusOK, reviewDiffResponse{Patch: patch})
	}
}

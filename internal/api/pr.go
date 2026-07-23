package api

import (
	"fmt"
	"net/http"

	"github.com/timmersuk/llm-workbench/internal/agentrunner"
	"github.com/timmersuk/llm-workbench/internal/task"
)

// handlePushPR is the "Push & Open PR" action for pr_review
// (docs/milestones/done/milestone7.md PR 3): pushes the task's latest execution
// branch to the project's GitHub remote and opens (or continues) a PR via
// agentrunner.PushAndOpenPR, then persists the result (task.RecordPullRequest).
// 409 if the task isn't currently at pr_review — checked explicitly before
// any git/gh activity, so a wrong-stage request never touches the network.
// Takes no request body: everything PushAndOpenPR needs (branch, title,
// body, existing PR to continue) is derived server-side from the task, its
// latest execution, and its latest (necessarily approving — see below)
// review, per PR 2 decision 7. Returns the plain updated Task, now carrying
// pull_request.
func (s *Server) handlePushPR() http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		projectId := r.PathValue("projectId")
		taskId := r.PathValue("taskId")

		proj, err := s.Projects.Get(projectId)
		if err != nil {
			writeGetError(w, err)
			return
		}
		root, err := s.Projects.TasksRoot(projectId)
		if err != nil {
			writeGetError(w, err)
			return
		}
		store := s.TaskStores(root)

		t, err := store.Get(taskId)
		if err != nil {
			writeGetError(w, err)
			return
		}
		if t.Stage != task.StagePRReview {
			http.Error(w, fmt.Sprintf("task is not in pr_review stage (currently %q)", t.Stage), http.StatusConflict)
			return
		}

		executions, err := store.ListExecutions(taskId)
		if err != nil {
			writeGetError(w, err)
			return
		}
		if len(executions) == 0 {
			http.Error(w, "no execution to push", http.StatusNotFound)
			return
		}
		// ListExecutions sorts ascending by the zero-padded id, so the last
		// entry is the most recent attempt — the one Review actually
		// approved (same selection handleReviewDiff makes).
		newBranch := executions[len(executions)-1].Output.GitBranch

		// The task is confirmed at pr_review above, and approved is only
		// ever recorded while leaving StageReview (task.FinalizeReview) —
		// pr_review's only other review-writing path (reject) moves the
		// task away from pr_review entirely — so as long as t.Stage is
		// still pr_review, the latest recorded review is guaranteed to be
		// the approval that landed the task here.
		reviews, err := store.ListReviews(taskId)
		if err != nil {
			writeGetError(w, err)
			return
		}
		var reviewNotes string
		if len(reviews) > 0 {
			reviewNotes = reviews[len(reviews)-1].Notes
		}

		var existingURL, existingBranch string
		var existingNumber int
		if t.PullRequest != nil {
			existingURL = t.PullRequest.URL
			existingNumber = t.PullRequest.Number
			existingBranch = t.PullRequest.Branch
		}

		dir, err := agentrunner.ResolveWorkspace(r.Context(), s.ReposRoot, proj.Repositories)
		if err != nil {
			http.Error(w, fmt.Sprintf("resolving workspace: %v", err), http.StatusInternalServerError)
			return
		}

		url, number, branch, err := agentrunner.PushAndOpenPR(
			r.Context(), dir, newBranch, prTitle(t), prBody(t, reviewNotes),
			existingURL, existingNumber, existingBranch, s.PRClient,
		)
		if err != nil {
			http.Error(w, fmt.Sprintf("pushing and opening PR: %v", err), http.StatusInternalServerError)
			return
		}

		updated, err := store.RecordPullRequest(taskId, task.PullRequest{URL: url, Number: number, Branch: branch})
		if err != nil {
			writeMutationError(w, err)
			return
		}
		writeJSON(w, http.StatusOK, updated)
	}
}

// prTitle mirrors TaskKanbanBoard.tsx's existing task.title || task.id
// fallback (docs/milestones/done/milestone7.md PR 2 decision 7).
func prTitle(t task.Task) string {
	if t.Title != "" {
		return t.Title
	}
	return t.ID
}

// prBody assembles the PR description PR 2 decision 7 specifies: the
// task's objective, the approving review's notes (if any) under a short
// heading, and a plain marker that the workbench opened this PR.
func prBody(t task.Task, reviewNotes string) string {
	body := t.Objective
	if reviewNotes != "" {
		body += "\n\n## Review notes\n" + reviewNotes
	}
	body += "\n\n_Opened by the LLM Workbench._"
	return body
}

// handleMarkPRMerged is the human "Mark as merged" action for pr_review: a
// human assertion that the PR was merged on GitHub, no polling and no
// review-record write (task.MarkPRMerged). 409 if the task isn't at
// pr_review, or if it has no pull_request recorded yet.
func (s *Server) handleMarkPRMerged() http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		store, ok := s.resolveTaskStore(w, r.PathValue("projectId"))
		if !ok {
			return
		}

		updated, err := store.MarkPRMerged(r.PathValue("taskId"))
		if err != nil {
			writeMutationError(w, err)
			return
		}
		writeJSON(w, http.StatusOK, updated)
	}
}

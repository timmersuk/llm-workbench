package api

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"

	"github.com/sirupsen/logrus"

	"github.com/timmersuk/llm-workbench/internal/agentrunner"
	"github.com/timmersuk/llm-workbench/internal/project"
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
		store := s.Tasks

		t, err := store.Get(projectId, taskId)
		if err != nil {
			writeGetError(w, err)
			return
		}
		if t.Stage != task.StagePRReview {
			writeAPIError(w, http.StatusConflict, fmt.Sprintf("task is not in pr_review stage (currently %q)", t.Stage))
			return
		}

		executions, err := store.ListExecutions(projectId, taskId)
		if err != nil {
			writeGetError(w, err)
			return
		}
		if len(executions) == 0 {
			writeAPIError(w, http.StatusNotFound, "no execution to push")
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
		reviews, err := store.ListReviews(projectId, taskId)
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
			writeAPIError(w, http.StatusInternalServerError, fmt.Sprintf("resolving workspace: %v", err))
			return
		}

		url, number, branch, err := agentrunner.PushAndOpenPR(
			r.Context(), dir, newBranch, prTitle(t), prBody(t, reviewNotes),
			existingURL, existingNumber, existingBranch, s.PRClient,
		)
		if err != nil {
			writeAPIError(w, http.StatusInternalServerError, fmt.Sprintf("pushing and opening PR: %v", err))
			return
		}

		updated, err := store.RecordPullRequest(projectId, taskId, task.PullRequest{URL: url, Number: number, Branch: branch})
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
// pr_review, or if it has no pull_request recorded yet. As of the
// execution-worktree-cleanup milestone, MarkPRMerged itself only lands the
// task on task.StageCleanup — this handler then tries to immediately run the
// same best-effort worktree-removal routine handleTaskCleanup's retry action
// re-drives (runCleanupPass, below), advancing all the way to
// task.StageMerged when every worktree came back clean.
//
// The human's merge assertion (store.MarkPRMerged) is recorded first and
// unconditionally: nothing about resolving the project or running cleanup is
// allowed to make that assertion disappear behind a 500. If proj resolution
// itself fails, that's logged and the handler returns the task exactly as
// MarkPRMerged left it — parked at "cleanup" with no report yet, still a
// 200. Once proj is in hand, any further cleanup problem (a dirty/unpushed
// worktree, a git failure) also never turns into an error response: it's
// logged and the task is simply returned still parked at "cleanup" with a
// fresh CleanupStatus report for a human to act on via POST .../cleanup.
func (s *Server) handleMarkPRMerged() http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		projectId := r.PathValue("projectId")
		taskId := r.PathValue("taskId")
		store := s.Tasks

		t, err := store.MarkPRMerged(projectId, taskId)
		if err != nil {
			writeMutationError(w, err)
			return
		}

		proj, err := s.Projects.Get(projectId)
		if err != nil {
			logrus.WithError(err).WithFields(logrus.Fields{"project": projectId, "task": taskId}).
				Warn("resolving project for execution-worktree cleanup")
			writeJSON(w, http.StatusOK, t)
			return
		}

		final := s.runCleanupPass(r.Context(), proj, store, t, false)
		writeJSON(w, http.StatusOK, final)
	}
}

// cleanupRequest is the optional JSON body for POST .../cleanup: force, when
// true, overrides CleanupTaskWorktrees's dirty/unpushed safety checks
// (agentrunner.CleanupTaskWorktrees's force parameter) — reserved for a
// human who has manually confirmed a flagged worktree is actually safe to
// discard. An empty or omitted body means force: false, matching
// reviseRequest's "omitted is fine" treatment above.
type cleanupRequest struct {
	Force bool `json:"force"`
}

// decodeCleanupForce mirrors decodeReviseReason's shape: an empty body
// (io.EOF) is "no force requested," not a malformed request; the bool
// return is false only for an actually malformed (non-empty, non-JSON)
// body.
func decodeCleanupForce(r *http.Request) (force bool, ok bool) {
	var req cleanupRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		return false, errors.Is(err, io.EOF)
	}
	return req.Force, true
}

// handleTaskCleanup is the human Retry/Force action available while a task
// is parked at task.StageCleanup (its automatic pass, run inline by
// handleMarkPRMerged, left at least one worktree skipped or failed): it
// re-runs the exact same routine (runCleanupPass), optionally with force —
// overriding the dirty/unpushed safety checks for a worktree a human has
// confirmed is safe to discard. 409 if the task isn't currently at
// task.StageCleanup.
func (s *Server) handleTaskCleanup() http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		projectId := r.PathValue("projectId")
		taskId := r.PathValue("taskId")

		proj, err := s.Projects.Get(projectId)
		if err != nil {
			writeGetError(w, err)
			return
		}
		store := s.Tasks

		t, err := store.Get(projectId, taskId)
		if err != nil {
			writeGetError(w, err)
			return
		}
		if t.Stage != task.StageCleanup {
			writeAPIError(w, http.StatusConflict, fmt.Sprintf("task is not in cleanup stage (currently %q)", t.Stage))
			return
		}

		force, ok := decodeCleanupForce(r)
		if !ok {
			writeAPIError(w, http.StatusBadRequest, "invalid request body")
			return
		}

		final := s.runCleanupPass(r.Context(), proj, store, t, force)
		writeJSON(w, http.StatusOK, final)
	}
}

// runCleanupPass runs agentrunner.CleanupTaskWorktrees over every one of t's
// recorded executions, persists the resulting per-worktree report
// (task.SetCleanupStatus), and — only when every worktree came back
// "removed" or "already-gone" — advances the task to task.StageMerged
// (task.CompleteCleanup), returning that. Otherwise (or if any step here
// itself errors) it returns t as last successfully persisted, still parked
// at task.StageCleanup. Best-effort throughout, mirroring
// CollectExecutionOutput/CollectExecutionPatch's own posture (internal/api/execution.go):
// every problem is logged via logrus, none of them ever become an error
// response — the caller (handleMarkPRMerged/handleTaskCleanup) always
// writes a 200 with whatever task state this function returns.
func (s *Server) runCleanupPass(ctx context.Context, proj project.Project, store TaskStore, t task.Task, force bool) task.Task {
	logFields := logrus.Fields{"project": proj.ID, "task": t.ID}

	executions, err := store.ListExecutions(proj.ID, t.ID)
	if err != nil {
		logrus.WithError(err).WithFields(logFields).Warn("listing executions for execution-worktree cleanup")
		return t
	}
	executionIDs := make([]string, len(executions))
	for i, e := range executions {
		executionIDs[i] = e.ExecutionID
	}

	var pushedBranch string
	if t.PullRequest != nil {
		pushedBranch = t.PullRequest.Branch
	}

	results := agentrunner.CleanupTaskWorktrees(ctx, s.ReposRoot, proj.Repositories, t.ID, pushedBranch, executionIDs, agentrunner.CleanupOptions{Force: force, Apply: true})

	status := make([]task.CleanupWorktreeStatus, len(results))
	allClean := true
	for i, res := range results {
		status[i] = task.CleanupWorktreeStatus{ExecutionID: res.ExecutionID, Outcome: res.Outcome, Reason: res.Reason}

		fields := logrus.Fields{
			"project": proj.ID, "task": t.ID, "execution": res.ExecutionID,
			"outcome": res.Outcome, "path": res.Path,
		}
		switch res.Outcome {
		case agentrunner.CleanupOutcomeRemoved, agentrunner.CleanupOutcomeAlreadyGone:
			// Success — still worth a Warn if a secondary step (e.g. the
			// branch delete after a successful worktree remove) hit a
			// problem, matching Reason's own "always set for skipped/
			// failed, sometimes for removed/already-gone" doc comment.
			if res.Reason != "" {
				logrus.WithFields(fields).WithField("reason", res.Reason).Warn("execution worktree cleanup: secondary issue")
			}
		case agentrunner.CleanupOutcomeSkipped:
			allClean = false
			logrus.WithFields(fields).WithField("reason", res.Reason).Warn("skipping execution worktree cleanup")
		case agentrunner.CleanupOutcomeFailed:
			allClean = false
			logrus.WithFields(fields).WithField("reason", res.Reason).Error("execution worktree cleanup failed")
		}
	}

	updated, err := store.SetCleanupStatus(proj.ID, t.ID, status)
	if err != nil {
		logrus.WithError(err).WithFields(logFields).Warn("persisting cleanup status")
		return t
	}

	if !allClean {
		return updated
	}

	final, err := store.CompleteCleanup(proj.ID, t.ID)
	if err != nil {
		logrus.WithError(err).WithFields(logFields).Warn("completing cleanup")
		return updated
	}
	return final
}

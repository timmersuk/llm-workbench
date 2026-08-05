// Command sweep-merged-worktrees is a one-off CLI addressing execution
// worktrees left behind by tasks that reached "merged" before the
// automatic cleanup routine (internal/api's handleMarkPRMerged) existed —
// disk usage under .worktrees/<repo>/<taskID>/<executionID> that nothing
// would otherwise ever reclaim. It walks every project's tasks already at
// task.StageMerged (plus any parked at task.StageCleanup, which the same
// routine would otherwise only ever retry via the API) and runs the exact
// same agentrunner.CleanupTaskWorktrees routine handleMarkPRMerged/
// handleTaskCleanup use.
//
// Dry-run by default: every worktree that passes the routine's safety
// checks is reported as "would-remove" rather than actually touched.
// -apply performs the real `git worktree remove`/`branch -D` calls.
// -force overrides the dirty/unpushed safety checks (only meaningful
// together with -apply) — the same override the human-triggered
// POST .../cleanup{force:true} action exposes, here applied in bulk.
//
// Mirrors cmd/migrate-agent-defaults's shape: load config, construct the
// real gitstore-backed stores, walk every project/task, print a plain
// per-task/per-worktree summary to stdout.
package main

import (
	"context"
	"flag"
	"fmt"
	"os"

	"github.com/timmersuk/llm-workbench/internal/agentrunner"
	"github.com/timmersuk/llm-workbench/internal/gitstore"
	"github.com/timmersuk/llm-workbench/internal/serverapp"
	"github.com/timmersuk/llm-workbench/internal/task"
)

func main() {
	apply := flag.Bool("apply", false, "actually remove worktrees/delete branches instead of only reporting what would happen")
	force := flag.Bool("force", false, "override the dirty/unpushed safety checks (only meaningful with -apply)")
	flag.Parse()

	if err := run(context.Background(), *apply, *force); err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
}

func run(ctx context.Context, apply, force bool) error {
	cfg := serverapp.LoadConfig()
	store, err := gitstore.Open(cfg.WorkspaceRoot, cfg.DataRepoURL)
	if err != nil {
		return err
	}

	if apply {
		fmt.Println("mode: apply (worktrees/branches will actually be removed)")
	} else {
		fmt.Println("mode: dry run (pass -apply to actually remove anything)")
	}

	projects, err := store.Projects.List()
	if err != nil {
		return err
	}

	failed := 0
	for _, proj := range projects.Projects {
		result, listErr := store.Tasks.List(proj.ID)
		if listErr != nil {
			fmt.Printf("failed listing tasks for %s: %v\n", proj.ID, listErr)
			failed++
			continue
		}
		for _, t := range result.Tasks {
			if t.Stage != task.StageMerged && t.Stage != task.StageCleanup {
				continue
			}
			if err := sweepTask(ctx, store, cfg.ReposRoot, proj.ID, proj.Repositories, t, apply, force); err != nil {
				fmt.Printf("failed %s/%s: %v\n", proj.ID, t.ID, err)
				failed++
			}
		}
		for _, loadErr := range result.Errors {
			fmt.Printf("failed %s/%s: %s\n", proj.ID, loadErr.ID, loadErr.Error)
			failed++
		}
	}
	if failed > 0 {
		return fmt.Errorf("sweep completed with %d failure(s)", failed)
	}
	return nil
}

// sweepTask runs the cleanup routine for one task and prints its
// per-worktree outcomes. Only ever returns an error for something outside
// the routine's own best-effort scope (listing t's executions, persisting
// the report) — a skipped or failed worktree is reported, not an error, the
// same "best-effort, always logged, never fatal" posture
// internal/api/pr.go's runCleanupPass follows. Only actually persists
// anything back onto the task (SetCleanupStatus/CompleteCleanup) in apply
// mode — a dry run reports what would happen without mutating any task's
// stored CleanupStatus or Stage.
func sweepTask(ctx context.Context, store *gitstore.Store, reposRoot, projectID string, repositories []string, t task.Task, apply, force bool) error {
	if t.PullRequest == nil {
		fmt.Printf("%s/%s: skipped (no pull_request recorded)\n", projectID, t.ID)
		return nil
	}

	executions, err := store.Tasks.ListExecutions(projectID, t.ID)
	if err != nil {
		return fmt.Errorf("listing executions: %w", err)
	}
	if len(executions) == 0 {
		return nil
	}
	executionIDs := make([]string, len(executions))
	for i, e := range executions {
		executionIDs[i] = e.ExecutionID
	}

	results := agentrunner.CleanupTaskWorktrees(ctx, reposRoot, repositories, t.ID, t.PullRequest.Branch, executionIDs, agentrunner.CleanupOptions{Force: force, Apply: apply})

	status := make([]task.CleanupWorktreeStatus, len(results))
	allClean := true
	for i, res := range results {
		status[i] = task.CleanupWorktreeStatus{ExecutionID: res.ExecutionID, Outcome: res.Outcome, Reason: res.Reason}
		if res.Outcome == agentrunner.CleanupOutcomeSkipped || res.Outcome == agentrunner.CleanupOutcomeFailed {
			allClean = false
		}
		reason := ""
		if res.Reason != "" {
			reason = ": " + res.Reason
		}
		fmt.Printf("%s/%s %s [%s] %s%s\n", projectID, t.ID, res.ExecutionID, res.Outcome, res.Path, reason)
	}

	if !apply {
		return nil
	}

	if _, err := store.Tasks.SetCleanupStatus(projectID, t.ID, status); err != nil {
		return fmt.Errorf("persisting cleanup status: %w", err)
	}
	if t.Stage == task.StageCleanup && allClean {
		if _, err := store.Tasks.CompleteCleanup(projectID, t.ID); err != nil {
			return fmt.Errorf("completing cleanup: %w", err)
		}
		fmt.Printf("%s/%s: advanced to merged\n", projectID, t.ID)
	}
	return nil
}

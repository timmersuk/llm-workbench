package agentrunner

import (
	"context"
	"fmt"
	"os"
	"path/filepath"

	"github.com/timmersuk/llm-workbench/internal/gitutil"
)

// Cleanup outcome values a WorktreeCleanupResult reports. Mirrored, not
// imported, by task.CleanupOutcome* (internal/task/task.go) — this package
// has no dependency on internal/task (agentrunner sits below it), and
// internal/api's handleMarkPRMerged/handleTaskCleanup, which already import
// both, are what actually maps a Result's Outcome onto a persisted
// task.CleanupWorktreeStatus.
const (
	CleanupOutcomeRemoved     = "removed"
	CleanupOutcomeAlreadyGone = "already-gone"
	CleanupOutcomeSkipped     = "skipped"
	CleanupOutcomeFailed      = "failed"
	// CleanupOutcomeWouldRemove only ever appears when CleanupOptions.Apply
	// is false (cmd/sweep-merged-worktrees's default dry-run mode): every
	// safety check passed, but no `git worktree remove`/`branch -D` was
	// actually run. Never produced by internal/api's handleMarkPRMerged/
	// handleTaskCleanup, which always apply — there's no dry-run concept
	// over HTTP — and never persisted onto task.Task.CleanupStatus, whose
	// task.CleanupOutcome* vocabulary deliberately has no matching value.
	CleanupOutcomeWouldRemove = "would-remove"
)

// CleanupOptions configures CleanupTaskWorktrees.
type CleanupOptions struct {
	// Force skips both the dirty and ancestry safety checks and passes
	// --force to `git worktree remove` — reserved for the human-triggered
	// POST .../cleanup{force:true} action or the sweep CLI's explicit
	// -force flag, never the automatic path handleMarkPRMerged drives.
	Force bool
	// Apply, when false, runs every check but stops short of actually
	// calling `git worktree remove`/`branch -D` — a worktree that passed
	// every safety check is reported as CleanupOutcomeWouldRemove instead
	// of being touched. internal/api's handlers always pass true (there's
	// no dry-run concept over HTTP); cmd/sweep-merged-worktrees defaults to
	// false so an operator can see what a sweep would do before committing
	// to it.
	Apply bool
}

// WorktreeCleanupResult is one execution attempt's outcome from
// CleanupTaskWorktrees.
type WorktreeCleanupResult struct {
	ExecutionID string
	Path        string
	Branch      string
	Outcome     string
	Reason      string
}

// CleanupTaskWorktrees removes every execution worktree a task left behind
// under .worktrees/<repo>/<taskID>/<executionID> (the layout
// ResolveExecutionWorkspace creates) via `git worktree remove`, plus each
// worktree's own task-exec/<taskID>/<executionID> branch via `git branch -D`
// — mirroring the stray-branch handling ResolveExecutionWorkspace already
// does before creating a fresh worktree. This is the best-effort routine
// internal/api's handleMarkPRMerged runs synchronously once a task reaches
// task.StageCleanup, and the one the sweep-merged-worktrees CLI re-runs
// across every already-merged task. It never fails as a whole: every
// executionIDs entry gets its own WorktreeCleanupResult, and one attempt's
// problem never aborts the rest. Callers own all logging — this function
// only returns structured results, the same "agentrunner returns, the
// caller logs" split CollectExecutionOutput/CollectExecutionPatch already
// use.
//
// pushedBranch is the task's task.PullRequest.Branch — the one branch name
// PushAndOpenPR actually pushed to origin (MarkPRMerged already requires
// PullRequest to be set before a task can reach StageCleanup, so this is
// always available in the real handleMarkPRMerged/handleTaskCleanup path).
// Unless force is set, a worktree is only removed when its own working tree
// is clean (gitutil.DirtyWorkingTree) and its HEAD is an ancestor of (or
// identical to) origin/<pushedBranch>'s current tip (gitutil.IsAncestor
// against gitutil.RemoteBranchTip) — never compared against the project's
// BaseBranch, which a squash/rebase merge rewrites in a way that would
// otherwise make an already-pushed worktree look permanently unmerged. A
// needs_changes retry chain's earlier attempts fork from their
// predecessor's branch tip (docs/adr/0012), so every attempt that actually
// fed into what got pushed is transitively an ancestor of it; an attempt
// that never fed into the pushed branch (e.g. an abandoned chain after a
// rejected review) correctly stays unsafe to auto-remove and is left in
// place, skipped and logged, for a human to force-remove if they're sure.
//
// See CleanupOptions for Force/Apply.
func CleanupTaskWorktrees(ctx context.Context, reposRoot string, repositories []string, taskID, pushedBranch string, executionIDs []string, opts CleanupOptions) []WorktreeCleanupResult {
	results := make([]WorktreeCleanupResult, 0, len(executionIDs))

	base, err := ResolveWorkspace(ctx, reposRoot, repositories)
	if err != nil {
		for _, executionID := range executionIDs {
			results = append(results, WorktreeCleanupResult{
				ExecutionID: executionID,
				Branch:      ExecutionBranchName(taskID, executionID),
				Outcome:     CleanupOutcomeFailed,
				Reason:      fmt.Sprintf("resolving shared checkout: %v", err),
			})
		}
		return results
	}
	root := filepath.Dir(base)
	repoName := filepath.Base(base)

	var pushedTip string
	var havePushedTip bool
	if !opts.Force && pushedBranch != "" {
		pushedTip, havePushedTip = gitutil.RemoteBranchTip(ctx, base, pushedBranch)
	}

	for _, executionID := range executionIDs {
		worktreePath := filepath.Join(root, ".worktrees", repoName, taskID, executionID)
		branch := ExecutionBranchName(taskID, executionID)
		results = append(results, cleanupOneWorktree(ctx, base, worktreePath, branch, executionID, pushedTip, havePushedTip, opts))
	}
	return results
}

func cleanupOneWorktree(ctx context.Context, base, worktreePath, branch, executionID, pushedTip string, havePushedTip bool, opts CleanupOptions) WorktreeCleanupResult {
	result := WorktreeCleanupResult{ExecutionID: executionID, Path: worktreePath, Branch: branch}

	info, statErr := os.Stat(worktreePath)
	switch {
	case os.IsNotExist(statErr):
		result.Outcome = CleanupOutcomeAlreadyGone
		// The worktree itself — the thing actually accumulating disk usage
		// — is already gone (removed by hand, or a prior pass that removed
		// the worktree but then failed the branch delete below). `git
		// branch -D` still refuses to delete a branch git's own worktree
		// administrative metadata (.git/worktrees/<id>) considers checked
		// out elsewhere, even once the directory itself no longer exists
		// (verified against real git behavior) — `worktree prune` clears
		// that stale metadata first so the best-effort branch delete that
		// follows actually succeeds. A failure either way is recorded but
		// doesn't downgrade the already-gone outcome. Skipped entirely in
		// dry-run mode (opts.Apply false): a dry run must never touch
		// anything, including a leftover branch.
		if !opts.Apply {
			return result
		}
		_, _ = gitutil.RunGit(ctx, base, "worktree", "prune")
		if _, err := gitutil.RunGit(ctx, base, "branch", "-D", branch); err != nil {
			result.Reason = fmt.Sprintf("worktree already gone; branch delete failed: %v", err)
		}
		return result
	case statErr != nil:
		result.Outcome = CleanupOutcomeFailed
		result.Reason = fmt.Sprintf("checking worktree path: %v", statErr)
		return result
	case !info.IsDir():
		result.Outcome = CleanupOutcomeFailed
		result.Reason = fmt.Sprintf("%s is not a directory", worktreePath)
		return result
	}

	if !opts.Force {
		if dirty := gitutil.DirtyWorkingTree(ctx, worktreePath); !dirty.Known {
			result.Outcome = CleanupOutcomeSkipped
			result.Reason = "unable to determine whether the worktree has uncommitted changes"
			return result
		} else if dirty.Dirty {
			result.Outcome = CleanupOutcomeSkipped
			result.Reason = "worktree has uncommitted changes"
			return result
		}

		if !havePushedTip {
			result.Outcome = CleanupOutcomeSkipped
			result.Reason = "could not resolve the pushed branch's remote-tracking tip"
			return result
		}
		ancestor, err := gitutil.IsAncestor(ctx, worktreePath, "HEAD", pushedTip)
		if err != nil {
			result.Outcome = CleanupOutcomeSkipped
			result.Reason = fmt.Sprintf("checking ancestry against pushed branch: %v", err)
			return result
		}
		if !ancestor {
			result.Outcome = CleanupOutcomeSkipped
			result.Reason = "worktree has commits not reachable from the pushed branch (possibly unpushed)"
			return result
		}
	}

	if !opts.Apply {
		result.Outcome = CleanupOutcomeWouldRemove
		return result
	}

	args := []string{"worktree", "remove"}
	if opts.Force {
		args = append(args, "--force")
	}
	args = append(args, worktreePath)
	if _, err := gitutil.RunGit(ctx, base, args...); err != nil {
		result.Outcome = CleanupOutcomeFailed
		result.Reason = err.Error()
		return result
	}

	result.Outcome = CleanupOutcomeRemoved
	if _, err := gitutil.RunGit(ctx, base, "branch", "-D", branch); err != nil {
		result.Reason = fmt.Sprintf("worktree removed; branch delete failed: %v", err)
	}
	return result
}

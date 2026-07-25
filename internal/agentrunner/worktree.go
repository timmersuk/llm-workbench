package agentrunner

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"github.com/timmersuk/llm-workbench/internal/gitutil"
)

// ErrWrongBranch is returned by ResolveExecutionWorkspace/
// ResolveReviewWorkspace when the shared checkout isn't on the project's
// known default branch. Unlike every other check in
// docs/milestones/done/milestone8a.md (behind-origin, dirty-working-tree, both
// advisory only), this one blocks: a worktree forked from the wrong branch
// commits an entire execution to the wrong PR base, discovered only once
// it's time to merge — the one place a wrong answer here is expensive to
// undo.
var ErrWrongBranch = errors.New("shared checkout is not on the project's default branch")

// checkDefaultBranch returns ErrWrongBranch if baseBranch isn't
// defaultBranch — including when defaultBranch is empty, i.e. never
// determined, which callers must fail closed on rather than pass through
// (docs/milestones/done/milestone8a.md's fail-closed decision) rather than
// silently allowing any branch when there's nothing to compare against.
func checkDefaultBranch(baseBranch, defaultBranch string) error {
	if defaultBranch == "" {
		return fmt.Errorf("%w: no default branch known for this project", ErrWrongBranch)
	}
	if baseBranch != defaultBranch {
		return fmt.Errorf("%w: shared checkout is on %q, expected %q", ErrWrongBranch, baseBranch, defaultBranch)
	}
	return nil
}

// ExecutionBranchName returns the deterministic branch name
// ResolveExecutionWorkspace creates for one execution attempt, without
// touching git or the filesystem. Callers that only need the name — e.g.
// Requirements-stage prompt text referencing a rejected attempt's branch
// (docs/milestones/done/milestone6.md's PR 5) — can call this instead of
// resolving a real workspace just to read .Branch off it.
func ExecutionBranchName(taskID, executionID string) string {
	return "task-exec/" + taskID + "/" + executionID
}

// ExecutionWorkspace is the isolated git worktree an Execute run works in —
// never the shared checkout ResolveWorkspace returns for
// Requirements/Planning stage conversations, since Execute writes to disk
// and commits.
type ExecutionWorkspace struct {
	// Path is the worktree's filesystem directory, always nested under
	// reposRoot (see ResolveExecutionWorkspace) so it never escapes the
	// same boundary ResolveWorkspace enforces for the shared checkout.
	Path string
	// Branch is the new branch the worktree was created on
	// ("task-exec/<taskID>/<executionID>").
	Branch string
	// BaseBranch is the branch the worktree's Branch was cut from — needed
	// afterward by CollectExecutionOutput to diff/log against.
	BaseBranch string
}

// ResolveExecutionWorkspace derives an isolated ExecutionWorkspace for one
// execution attempt: resolves the project's shared checkout via the
// existing ResolveWorkspace (reusing its "only place a workspace is
// decided, never escapes reposRoot" contract), then creates a fresh `git
// worktree` for taskID/executionID on a new branch cut from forkFrom (or
// the shared checkout's current branch, if forkFrom is empty) — so the
// execution can write, run tests, and commit without ever touching the
// shared checkout a human (or a Requirements/Planning agent) might have
// open. taskID and executionID are expected to already be validated slugs
// (task/execution store ids) by the caller; this function still rejects
// path separators/".." defensively since both are joined into filesystem
// paths and a git branch name here.
//
// forkFrom lets a needs_changes retry continue from its prior attempt's
// branch tip instead of starting blank (docs/adr/0012): the new branch is
// cut from forkFrom, but the returned ExecutionWorkspace.BaseBranch is
// still always the shared checkout's own branch (typically "main"),
// independent of forkFrom — so CollectExecutionOutput/CollectExecutionPatch
// keep diffing the cumulative change against main regardless of which ref
// the worktree actually started from.
//
// The worktree is left in place on return regardless of what happens
// afterward — no automatic cleanup — so a human can inspect it, and a
// future Review-stage UI can read its diff.
//
// defaultBranch is the project's known default branch (docs/milestones/done/milestone8a.md,
// Project.DefaultBranch) — the shared checkout's current branch must match
// it exactly, or this refuses to fork at all (ErrWrongBranch, via
// checkDefaultBranch). Callers are responsible for having already resolved
// this (internal/api's ensureDefaultBranch); an empty string always blocks,
// it is never treated as "no expectation."
func ResolveExecutionWorkspace(ctx context.Context, reposRoot string, repositories []string, taskID, executionID, forkFrom, defaultBranch string) (ExecutionWorkspace, error) {
	if strings.ContainsAny(taskID, `/\`) || strings.Contains(taskID, "..") {
		return ExecutionWorkspace{}, fmt.Errorf("%w: task id %q", ErrInvalidRepository, taskID)
	}
	if strings.ContainsAny(executionID, `/\`) || strings.Contains(executionID, "..") {
		return ExecutionWorkspace{}, fmt.Errorf("%w: execution id %q", ErrInvalidRepository, executionID)
	}

	base, err := ResolveWorkspace(ctx, reposRoot, repositories)
	if err != nil {
		return ExecutionWorkspace{}, err
	}

	baseBranch, err := gitutil.CurrentBranch(ctx, base)
	if err != nil {
		return ExecutionWorkspace{}, err
	}
	if err := checkDefaultBranch(baseBranch, defaultBranch); err != nil {
		return ExecutionWorkspace{}, err
	}

	// base == filepath.Join(root, repoName) by ResolveWorkspace's own
	// construction, so filepath.Dir(base) recovers the same reposRoot
	// (absolute, already validated) without re-deriving it.
	//
	// worktreePath is keyed by taskID/executionID, not executionID alone:
	// NextExecutionID (internal/task/execution.go) numbers each task's
	// executions independently starting at "exec-001", so two different
	// tasks' first executions collide on the same "exec-001" id. Without
	// taskID in the path, a second task's first execution would try to
	// create a worktree at a path an earlier, unrelated task already left
	// behind, and `git worktree add` fails with "already exists".
	root := filepath.Dir(base)
	repoName := filepath.Base(base)
	worktreePath := filepath.Join(root, ".worktrees", repoName, taskID, executionID)
	branch := ExecutionBranchName(taskID, executionID)

	forkRef := baseBranch
	if forkFrom != "" {
		forkRef = forkFrom
	}

	if err := os.MkdirAll(filepath.Dir(worktreePath), 0o755); err != nil {
		return ExecutionWorkspace{}, fmt.Errorf("creating worktree parent directory: %w", err)
	}

	// `git worktree add -b <branch>` creates the branch before it validates
	// the target path, and does not roll the branch back if that later step
	// fails — verified against real git behavior, not assumed. Since branch
	// is a pure function of taskID/executionID (ExecutionBranchName) and
	// this is the only place that name is ever created, and the caller only
	// reaches RecordExecution (the one thing that retires an executionID so
	// a later request gets a fresh one) when this function succeeds, a retry
	// after a failed call here always reuses the exact same branch name. Any
	// existing branch with that name is therefore always a stray from a
	// prior failed attempt, never real work — remove it before asking git to
	// create it again. `git branch -D` itself refuses (and returns an error
	// this surfaces) if the branch is actually checked out in some worktree,
	// so this can't silently discard live work.
	if exists, err := gitutil.BranchExists(ctx, base, branch); err != nil {
		return ExecutionWorkspace{}, fmt.Errorf("checking for stale branch %s: %w", branch, err)
	} else if exists {
		if _, err := gitutil.RunGit(ctx, base, "branch", "-D", branch); err != nil {
			return ExecutionWorkspace{}, fmt.Errorf("removing stale branch %s before creating worktree: %w", branch, err)
		}
	}

	if _, err := gitutil.RunGit(ctx, base, "worktree", "add", "-b", branch, worktreePath, forkRef); err != nil {
		return ExecutionWorkspace{}, fmt.Errorf("creating git worktree for execution %s: %w", executionID, err)
	}

	return ExecutionWorkspace{Path: worktreePath, Branch: branch, BaseBranch: baseBranch}, nil
}

// ResolveReviewWorkspace locates the execution worktree ResolveExecutionWorkspace
// already created for taskID/executionID and left in place — it never creates
// one. The Review conversation (Milestone 6) runs against this same isolated
// worktree so its confined bash tool can run the project's tests over the
// executed change, and its diff can be collected (CollectExecutionPatch),
// without touching the project's shared checkout. The worktree path is
// reconstructed deterministically the same way ResolveExecutionWorkspace built
// it (<reposRoot>/.worktrees/<repoName>/<taskID>/<executionID>) and must
// already exist; BaseBranch is re-derived from the shared checkout's current
// branch, exactly as the execution derived it originally — which is also why
// defaultBranch (see ResolveExecutionWorkspace's doc comment) is checked here
// too, even though this never forks a new worktree: a wrong branch would
// silently corrupt BaseBranch, and with it every diff Review computes against
// it.
func ResolveReviewWorkspace(ctx context.Context, reposRoot string, repositories []string, taskID, executionID, defaultBranch string) (ExecutionWorkspace, error) {
	if strings.ContainsAny(taskID, `/\`) || strings.Contains(taskID, "..") {
		return ExecutionWorkspace{}, fmt.Errorf("%w: task id %q", ErrInvalidRepository, taskID)
	}
	if strings.ContainsAny(executionID, `/\`) || strings.Contains(executionID, "..") {
		return ExecutionWorkspace{}, fmt.Errorf("%w: execution id %q", ErrInvalidRepository, executionID)
	}

	base, err := ResolveWorkspace(ctx, reposRoot, repositories)
	if err != nil {
		return ExecutionWorkspace{}, err
	}

	baseBranch, err := gitutil.CurrentBranch(ctx, base)
	if err != nil {
		return ExecutionWorkspace{}, err
	}
	if err := checkDefaultBranch(baseBranch, defaultBranch); err != nil {
		return ExecutionWorkspace{}, err
	}

	root := filepath.Dir(base)
	repoName := filepath.Base(base)
	worktreePath := filepath.Join(root, ".worktrees", repoName, taskID, executionID)

	info, err := os.Stat(worktreePath)
	if err != nil {
		return ExecutionWorkspace{}, fmt.Errorf("%w: resolving execution worktree %s: %v", ErrInvalidRepository, worktreePath, err)
	}
	if !info.IsDir() {
		return ExecutionWorkspace{}, fmt.Errorf("%w: %s is not a directory", ErrInvalidRepository, worktreePath)
	}

	branch, err := gitutil.CurrentBranch(ctx, worktreePath)
	if err != nil {
		return ExecutionWorkspace{}, err
	}

	return ExecutionWorkspace{Path: worktreePath, Branch: branch, BaseBranch: baseBranch}, nil
}

// CollectExecutionOutput inspects ws after an Execute run has finished,
// returning the commits this specific attempt made (oldest first) and the
// paths of every file that differs from ws.BaseBranch. Best-effort: callers
// should log a failure here rather than fail the whole execution response
// over it, since the execution itself already succeeded or failed
// independently of whether this inspection works.
//
// forkPoint is the ref this attempt's worktree was actually created from
// (task.ExecutionOutput.ForkedFromBranch) — empty for a first attempt
// forked directly from BaseBranch. The commit list is scoped to forkPoint
// (or BaseBranch if forkPoint is empty), so a needs_changes retry's Commits
// reports only what *this* attempt contributed, not every ancestor
// attempt's commits too (docs/adr/0012 chains retries from the prior
// attempt's branch tip, not from BaseBranch). The diff is deliberately kept
// separate and always cumulative against BaseBranch regardless — Review
// wants the full change so far, not just the latest attempt's slice of it.
//
// The diff uses three-dot (`BaseBranch...HEAD`, merge-base diff) rather than
// two-dot: two-dot compares the two branch tips directly, which is only
// correct while BaseBranch is a strict ancestor of HEAD. Since a retry's
// worktree is forked from a fixed tip rather than BaseBranch's current tip,
// if the shared checkout's BaseBranch advances during a long-running retry
// cycle, a two-dot diff would pick up BaseBranch's new commits as spurious
// reverse-diff noise. `git log A..B` doesn't have this problem — it already
// means "commits reachable from B but not A" — so only the diff calls use
// three-dot.
func CollectExecutionOutput(ctx context.Context, ws ExecutionWorkspace, forkPoint string) (commits []string, artifacts []string, err error) {
	commitsRange := ws.BaseBranch
	if forkPoint != "" {
		commitsRange = forkPoint
	}
	commitsOut, err := gitutil.RunGit(ctx, ws.Path, "log", "--format=%H", "--reverse", commitsRange+"..HEAD")
	if err != nil {
		return nil, nil, fmt.Errorf("listing commits for %s: %w", ws.Path, err)
	}
	commits = splitNonEmptyLines(commitsOut)

	artifactsOut, err := gitutil.RunGit(ctx, ws.Path, "diff", "--name-only", ws.BaseBranch+"...HEAD")
	if err != nil {
		return nil, nil, fmt.Errorf("listing changed files for %s: %w", ws.Path, err)
	}
	artifacts = splitNonEmptyLines(artifactsOut)

	return commits, artifacts, nil
}

// CollectExecutionPatch is the full-patch variant of CollectExecutionOutput:
// it returns the same commits (see CollectExecutionOutput's doc comment for
// how forkPoint scopes them), but instead of just the changed file names it
// returns the actual unified diff of ws.Branch against ws.BaseBranch
// (three-dot/merge-base — always cumulative, unaffected by forkPoint). The
// Review conversation (Milestone 6) carries this real diff in its prompt so
// the agent has the concrete change to check, rather than a bare list of
// touched paths. Best-effort in the same way — a caller should log a
// failure here rather than fail the whole review over it.
func CollectExecutionPatch(ctx context.Context, ws ExecutionWorkspace, forkPoint string) (commits []string, patch string, err error) {
	commitsRange := ws.BaseBranch
	if forkPoint != "" {
		commitsRange = forkPoint
	}
	commitsOut, err := gitutil.RunGit(ctx, ws.Path, "log", "--format=%H", "--reverse", commitsRange+"..HEAD")
	if err != nil {
		return nil, "", fmt.Errorf("listing commits for %s: %w", ws.Path, err)
	}
	commits = splitNonEmptyLines(commitsOut)

	patchOut, err := gitutil.RunGit(ctx, ws.Path, "diff", ws.BaseBranch+"...HEAD")
	if err != nil {
		return nil, "", fmt.Errorf("collecting patch for %s: %w", ws.Path, err)
	}

	return commits, patchOut, nil
}

func splitNonEmptyLines(s string) []string {
	lines := []string{}
	for _, line := range strings.Split(s, "\n") {
		line = strings.TrimSpace(line)
		if line != "" {
			lines = append(lines, line)
		}
	}
	return lines
}

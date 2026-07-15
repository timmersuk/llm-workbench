package agentrunner

import (
	"context"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
)

// ExecutionBranchName returns the deterministic branch name
// ResolveExecutionWorkspace creates for one execution attempt, without
// touching git or the filesystem. Callers that only need the name — e.g.
// Requirements-stage prompt text referencing a rejected attempt's branch
// (docs/milestones/milestone6.md's PR 5) — can call this instead of
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
func ResolveExecutionWorkspace(ctx context.Context, reposRoot string, repositories []string, taskID, executionID, forkFrom string) (ExecutionWorkspace, error) {
	if strings.ContainsAny(taskID, `/\`) || strings.Contains(taskID, "..") {
		return ExecutionWorkspace{}, fmt.Errorf("%w: task id %q", ErrInvalidRepository, taskID)
	}
	if strings.ContainsAny(executionID, `/\`) || strings.Contains(executionID, "..") {
		return ExecutionWorkspace{}, fmt.Errorf("%w: execution id %q", ErrInvalidRepository, executionID)
	}

	base, err := ResolveWorkspace(reposRoot, repositories)
	if err != nil {
		return ExecutionWorkspace{}, err
	}

	baseBranchOut, err := runGit(ctx, base, "rev-parse", "--abbrev-ref", "HEAD")
	if err != nil {
		return ExecutionWorkspace{}, fmt.Errorf("resolving base branch for %s: %w", base, err)
	}
	baseBranch := strings.TrimSpace(baseBranchOut)

	// base == filepath.Join(root, repoName) by ResolveWorkspace's own
	// construction, so filepath.Dir(base) recovers the same reposRoot
	// (absolute, already validated) without re-deriving it.
	root := filepath.Dir(base)
	repoName := filepath.Base(base)
	worktreePath := filepath.Join(root, ".worktrees", repoName, executionID)
	branch := ExecutionBranchName(taskID, executionID)

	forkRef := baseBranch
	if forkFrom != "" {
		forkRef = forkFrom
	}

	if err := os.MkdirAll(filepath.Dir(worktreePath), 0o755); err != nil {
		return ExecutionWorkspace{}, fmt.Errorf("creating worktree parent directory: %w", err)
	}

	if _, err := runGit(ctx, base, "worktree", "add", "-b", branch, worktreePath, forkRef); err != nil {
		return ExecutionWorkspace{}, fmt.Errorf("creating git worktree for execution %s: %w", executionID, err)
	}

	return ExecutionWorkspace{Path: worktreePath, Branch: branch, BaseBranch: baseBranch}, nil
}

// ResolveReviewWorkspace locates the execution worktree ResolveExecutionWorkspace
// already created for executionID and left in place — it never creates one.
// The Review conversation (Milestone 6) runs against this same isolated
// worktree so its confined bash tool can run the project's tests over the
// executed change, and its diff can be collected (CollectExecutionPatch),
// without touching the project's shared checkout. The worktree path is
// reconstructed deterministically the same way ResolveExecutionWorkspace built
// it (<reposRoot>/.worktrees/<repoName>/<executionID>) and must already exist;
// BaseBranch is re-derived from the shared checkout's current branch, exactly
// as the execution derived it originally.
func ResolveReviewWorkspace(ctx context.Context, reposRoot string, repositories []string, executionID string) (ExecutionWorkspace, error) {
	if strings.ContainsAny(executionID, `/\`) || strings.Contains(executionID, "..") {
		return ExecutionWorkspace{}, fmt.Errorf("%w: execution id %q", ErrInvalidRepository, executionID)
	}

	base, err := ResolveWorkspace(reposRoot, repositories)
	if err != nil {
		return ExecutionWorkspace{}, err
	}

	baseBranchOut, err := runGit(ctx, base, "rev-parse", "--abbrev-ref", "HEAD")
	if err != nil {
		return ExecutionWorkspace{}, fmt.Errorf("resolving base branch for %s: %w", base, err)
	}
	baseBranch := strings.TrimSpace(baseBranchOut)

	root := filepath.Dir(base)
	repoName := filepath.Base(base)
	worktreePath := filepath.Join(root, ".worktrees", repoName, executionID)

	info, err := os.Stat(worktreePath)
	if err != nil {
		return ExecutionWorkspace{}, fmt.Errorf("%w: resolving execution worktree %s: %v", ErrInvalidRepository, worktreePath, err)
	}
	if !info.IsDir() {
		return ExecutionWorkspace{}, fmt.Errorf("%w: %s is not a directory", ErrInvalidRepository, worktreePath)
	}

	branchOut, err := runGit(ctx, worktreePath, "rev-parse", "--abbrev-ref", "HEAD")
	if err != nil {
		return ExecutionWorkspace{}, fmt.Errorf("resolving worktree branch for %s: %w", worktreePath, err)
	}

	return ExecutionWorkspace{Path: worktreePath, Branch: strings.TrimSpace(branchOut), BaseBranch: baseBranch}, nil
}

// CollectExecutionOutput inspects ws after an Execute run has finished,
// returning the commits made on ws.Branch since it diverged from
// ws.BaseBranch (oldest first) and the paths of every file that differs
// from ws.BaseBranch. Best-effort: callers should log a failure here
// rather than fail the whole execution response over it, since the
// execution itself already succeeded or failed independently of whether
// this inspection works.
func CollectExecutionOutput(ctx context.Context, ws ExecutionWorkspace) (commits []string, artifacts []string, err error) {
	commitsOut, err := runGit(ctx, ws.Path, "log", "--format=%H", "--reverse", ws.BaseBranch+"..HEAD")
	if err != nil {
		return nil, nil, fmt.Errorf("listing commits for %s: %w", ws.Path, err)
	}
	commits = splitNonEmptyLines(commitsOut)

	artifactsOut, err := runGit(ctx, ws.Path, "diff", "--name-only", ws.BaseBranch+"..HEAD")
	if err != nil {
		return nil, nil, fmt.Errorf("listing changed files for %s: %w", ws.Path, err)
	}
	artifacts = splitNonEmptyLines(artifactsOut)

	return commits, artifacts, nil
}

// CollectExecutionPatch is the full-patch variant of CollectExecutionOutput:
// it returns the same commits (oldest first), but instead of just the changed
// file names it returns the actual unified diff of ws.Branch against
// ws.BaseBranch. The Review conversation (Milestone 6) carries this real diff
// in its prompt so the agent has the concrete change to check, rather than a
// bare list of touched paths. Best-effort in the same way — a caller should
// log a failure here rather than fail the whole review over it.
func CollectExecutionPatch(ctx context.Context, ws ExecutionWorkspace) (commits []string, patch string, err error) {
	commitsOut, err := runGit(ctx, ws.Path, "log", "--format=%H", "--reverse", ws.BaseBranch+"..HEAD")
	if err != nil {
		return nil, "", fmt.Errorf("listing commits for %s: %w", ws.Path, err)
	}
	commits = splitNonEmptyLines(commitsOut)

	patchOut, err := runGit(ctx, ws.Path, "diff", ws.BaseBranch+"..HEAD")
	if err != nil {
		return nil, "", fmt.Errorf("collecting patch for %s: %w", ws.Path, err)
	}

	return commits, patchOut, nil
}

// runGit runs `git <args...>` with dir as its working directory, returning
// combined stdout+stderr so a failure's error message actually explains
// what went wrong (git writes its errors to stderr).
func runGit(ctx context.Context, dir string, args ...string) (string, error) {
	cmd := exec.CommandContext(ctx, "git", args...)
	cmd.Dir = dir
	out, err := cmd.CombinedOutput()
	if err != nil {
		return "", fmt.Errorf("git %s: %w: %s", strings.Join(args, " "), err, strings.TrimSpace(string(out)))
	}
	return string(out), nil
}

func splitNonEmptyLines(s string) []string {
	var lines []string
	for _, line := range strings.Split(s, "\n") {
		line = strings.TrimSpace(line)
		if line != "" {
			lines = append(lines, line)
		}
	}
	return lines
}

package agentrunner

import (
	"context"
	"os"
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/timmersuk/llm-workbench/internal/gitutil"
)

func TestCleanupTaskWorktrees_RemovesCleanPushedChainAcrossRetries(t *testing.T) {
	reposRoot := t.TempDir()
	repoDir := initTestRepo(t, reposRoot, "myrepo")
	initBareRemote(t, reposRoot, repoDir)
	ctx := context.Background()

	// exec-001 is a first attempt; exec-002 continues from it, per
	// docs/adr/0012's fork-from-prior-branch chain — mirroring a
	// needs_changes retry.
	ws1, err := ResolveExecutionWorkspace(ctx, reposRoot, []string{"github.com/x/myrepo"}, "task-a", "exec-001", "", "main")
	require.NoError(t, err)
	require.NoError(t, os.WriteFile(filepath.Join(ws1.Path, "wip.txt"), []byte("attempt 1\n"), 0o644))
	_, err = gitutil.RunGit(ctx, ws1.Path, "add", ".")
	require.NoError(t, err)
	_, err = gitutil.RunGit(ctx, ws1.Path, "commit", "-q", "-m", "attempt 1")
	require.NoError(t, err)

	ws2, err := ResolveExecutionWorkspace(ctx, reposRoot, []string{"github.com/x/myrepo"}, "task-a", "exec-002", ws1.Branch, "main")
	require.NoError(t, err)
	require.NoError(t, os.WriteFile(filepath.Join(ws2.Path, "wip2.txt"), []byte("attempt 2\n"), 0o644))
	_, err = gitutil.RunGit(ctx, ws2.Path, "add", ".")
	require.NoError(t, err)
	_, err = gitutil.RunGit(ctx, ws2.Path, "commit", "-q", "-m", "attempt 2")
	require.NoError(t, err)

	// Only the final, approved attempt's branch is actually pushed
	// (mirrors handlePushPR: the latest execution's branch).
	_, err = gitutil.RunGit(ctx, repoDir, "push", "origin", ws2.Branch)
	require.NoError(t, err)

	results := CleanupTaskWorktrees(ctx, reposRoot, []string{"github.com/x/myrepo"}, "task-a", ws2.Branch, []string{"exec-001", "exec-002"}, CleanupOptions{Apply: true})
	require.Len(t, results, 2)
	for _, r := range results {
		assert.Equalf(t, CleanupOutcomeRemoved, r.Outcome, "execution %s: %s", r.ExecutionID, r.Reason)
		assert.NoDirExists(t, r.Path)
	}

	exists, err := gitutil.BranchExists(ctx, repoDir, ws1.Branch)
	require.NoError(t, err)
	assert.False(t, exists, "exec-001's branch must be deleted too")
	exists, err = gitutil.BranchExists(ctx, repoDir, ws2.Branch)
	require.NoError(t, err)
	assert.False(t, exists, "exec-002's branch must be deleted too")
}

func TestCleanupTaskWorktrees_SkipsDirtyWorktree(t *testing.T) {
	reposRoot := t.TempDir()
	repoDir := initTestRepo(t, reposRoot, "myrepo")
	initBareRemote(t, reposRoot, repoDir)
	ctx := context.Background()

	ws, err := ResolveExecutionWorkspace(ctx, reposRoot, []string{"github.com/x/myrepo"}, "task-a", "exec-001", "", "main")
	require.NoError(t, err)
	_, err = gitutil.RunGit(ctx, repoDir, "push", "origin", ws.Branch)
	require.NoError(t, err)

	require.NoError(t, os.WriteFile(filepath.Join(ws.Path, "dirty.txt"), []byte("uncommitted\n"), 0o644))

	results := CleanupTaskWorktrees(ctx, reposRoot, []string{"github.com/x/myrepo"}, "task-a", ws.Branch, []string{"exec-001"}, CleanupOptions{Apply: true})
	require.Len(t, results, 1)
	assert.Equal(t, CleanupOutcomeSkipped, results[0].Outcome)
	assert.Contains(t, results[0].Reason, "uncommitted")
	assert.DirExists(t, ws.Path)

	exists, err := gitutil.BranchExists(ctx, repoDir, ws.Branch)
	require.NoError(t, err)
	assert.True(t, exists, "a skipped worktree's branch must not be deleted")
}

func TestCleanupTaskWorktrees_SkipsCommitsNotReachableFromPushedBranch(t *testing.T) {
	reposRoot := t.TempDir()
	repoDir := initTestRepo(t, reposRoot, "myrepo")
	initBareRemote(t, reposRoot, repoDir)
	ctx := context.Background()

	// exec-001 is an abandoned attempt (e.g. its review was rejected): a
	// later, unrelated exec-002 forks straight from main again rather than
	// continuing exec-001's branch, so exec-001's commit is never an
	// ancestor of whatever eventually got pushed.
	ws1, err := ResolveExecutionWorkspace(ctx, reposRoot, []string{"github.com/x/myrepo"}, "task-a", "exec-001", "", "main")
	require.NoError(t, err)
	require.NoError(t, os.WriteFile(filepath.Join(ws1.Path, "wip.txt"), []byte("abandoned\n"), 0o644))
	_, err = gitutil.RunGit(ctx, ws1.Path, "add", ".")
	require.NoError(t, err)
	_, err = gitutil.RunGit(ctx, ws1.Path, "commit", "-q", "-m", "abandoned attempt")
	require.NoError(t, err)

	ws2, err := ResolveExecutionWorkspace(ctx, reposRoot, []string{"github.com/x/myrepo"}, "task-a", "exec-002", "", "main")
	require.NoError(t, err)
	_, err = gitutil.RunGit(ctx, repoDir, "push", "origin", ws2.Branch)
	require.NoError(t, err)

	results := CleanupTaskWorktrees(ctx, reposRoot, []string{"github.com/x/myrepo"}, "task-a", ws2.Branch, []string{"exec-001"}, CleanupOptions{Apply: true})
	require.Len(t, results, 1)
	assert.Equal(t, CleanupOutcomeSkipped, results[0].Outcome)
	assert.Contains(t, results[0].Reason, "not reachable")
	assert.DirExists(t, ws1.Path)
}

func TestCleanupTaskWorktrees_ForceRemovesDirtyWorktree(t *testing.T) {
	reposRoot := t.TempDir()
	repoDir := initTestRepo(t, reposRoot, "myrepo")
	ctx := context.Background()

	ws, err := ResolveExecutionWorkspace(ctx, reposRoot, []string{"github.com/x/myrepo"}, "task-a", "exec-001", "", "main")
	require.NoError(t, err)
	require.NoError(t, os.WriteFile(filepath.Join(ws.Path, "dirty.txt"), []byte("uncommitted\n"), 0o644))

	results := CleanupTaskWorktrees(ctx, reposRoot, []string{"github.com/x/myrepo"}, "task-a", "", []string{"exec-001"}, CleanupOptions{Force: true, Apply: true})
	require.Len(t, results, 1)
	assert.Equal(t, CleanupOutcomeRemoved, results[0].Outcome)
	assert.NoDirExists(t, ws.Path)

	exists, err := gitutil.BranchExists(ctx, repoDir, ws.Branch)
	require.NoError(t, err)
	assert.False(t, exists)
}

func TestCleanupTaskWorktrees_AlreadyGoneWorktreeIsSkippedCleanly(t *testing.T) {
	reposRoot := t.TempDir()
	initTestRepo(t, reposRoot, "myrepo")
	ctx := context.Background()

	ws, err := ResolveExecutionWorkspace(ctx, reposRoot, []string{"github.com/x/myrepo"}, "task-a", "exec-001", "", "main")
	require.NoError(t, err)
	// Simulate an operator manually deleting the worktree directory by
	// hand, leaving git's own worktree admin metadata stale.
	require.NoError(t, os.RemoveAll(ws.Path))

	results := CleanupTaskWorktrees(ctx, reposRoot, []string{"github.com/x/myrepo"}, "task-a", "", []string{"exec-001"}, CleanupOptions{Apply: true})
	require.Len(t, results, 1)
	assert.Equal(t, CleanupOutcomeAlreadyGone, results[0].Outcome)

	exists, err := gitutil.BranchExists(ctx, filepath.Join(reposRoot, "myrepo"), ws.Branch)
	require.NoError(t, err)
	assert.False(t, exists, "the leftover branch should still be cleaned up")
}

func TestCleanupTaskWorktrees_OneFailureDoesNotAbortTheRest(t *testing.T) {
	reposRoot := t.TempDir()
	repoDir := initTestRepo(t, reposRoot, "myrepo")
	initBareRemote(t, reposRoot, repoDir)
	ctx := context.Background()

	ws1, err := ResolveExecutionWorkspace(ctx, reposRoot, []string{"github.com/x/myrepo"}, "task-a", "exec-001", "", "main")
	require.NoError(t, err)
	require.NoError(t, os.WriteFile(filepath.Join(ws1.Path, "dirty.txt"), []byte("uncommitted\n"), 0o644)) // will be skipped, not failed

	ws2, err := ResolveExecutionWorkspace(ctx, reposRoot, []string{"github.com/x/myrepo"}, "task-a", "exec-002", ws1.Branch, "main")
	require.NoError(t, err)
	_, err = gitutil.RunGit(ctx, repoDir, "push", "origin", ws2.Branch)
	require.NoError(t, err)

	// exec-003 was never a real worktree at all — a plain file sits where
	// one would be, so resolving it hits a hard error rather than a mere
	// skip.
	brokenPath := filepath.Join(reposRoot, ".worktrees", "myrepo", "task-a", "exec-003")
	require.NoError(t, os.MkdirAll(filepath.Dir(brokenPath), 0o755))
	require.NoError(t, os.WriteFile(brokenPath, []byte("not a worktree"), 0o644))

	results := CleanupTaskWorktrees(ctx, reposRoot, []string{"github.com/x/myrepo"}, "task-a", ws2.Branch, []string{"exec-001", "exec-002", "exec-003"}, CleanupOptions{Apply: true})
	require.Len(t, results, 3)

	byID := map[string]WorktreeCleanupResult{}
	for _, r := range results {
		byID[r.ExecutionID] = r
	}
	assert.Equal(t, CleanupOutcomeSkipped, byID["exec-001"].Outcome, "dirty, left in place")
	assert.Equal(t, CleanupOutcomeRemoved, byID["exec-002"].Outcome, "clean and pushed, removed regardless")
	assert.Equal(t, CleanupOutcomeFailed, byID["exec-003"].Outcome, "a hard error on one attempt doesn't abort the others")
}

func TestCleanupTaskWorktrees_DryRunLeavesAlreadyGoneWorktreesBranchAlone(t *testing.T) {
	reposRoot := t.TempDir()
	repoDir := initTestRepo(t, reposRoot, "myrepo")
	ctx := context.Background()

	ws, err := ResolveExecutionWorkspace(ctx, reposRoot, []string{"github.com/x/myrepo"}, "task-a", "exec-001", "", "main")
	require.NoError(t, err)
	require.NoError(t, os.RemoveAll(ws.Path))

	results := CleanupTaskWorktrees(ctx, reposRoot, []string{"github.com/x/myrepo"}, "task-a", "", []string{"exec-001"}, CleanupOptions{})
	require.Len(t, results, 1)
	assert.Equal(t, CleanupOutcomeAlreadyGone, results[0].Outcome)

	exists, err := gitutil.BranchExists(ctx, repoDir, ws.Branch)
	require.NoError(t, err)
	assert.True(t, exists, "dry run must not delete the leftover branch")
}

func TestCleanupTaskWorktrees_DryRunReportsWithoutTouchingAnything(t *testing.T) {
	reposRoot := t.TempDir()
	repoDir := initTestRepo(t, reposRoot, "myrepo")
	initBareRemote(t, reposRoot, repoDir)
	ctx := context.Background()

	ws, err := ResolveExecutionWorkspace(ctx, reposRoot, []string{"github.com/x/myrepo"}, "task-a", "exec-001", "", "main")
	require.NoError(t, err)
	_, err = gitutil.RunGit(ctx, repoDir, "push", "origin", ws.Branch)
	require.NoError(t, err)

	results := CleanupTaskWorktrees(ctx, reposRoot, []string{"github.com/x/myrepo"}, "task-a", ws.Branch, []string{"exec-001"}, CleanupOptions{})
	require.Len(t, results, 1)
	assert.Equal(t, CleanupOutcomeWouldRemove, results[0].Outcome)
	assert.DirExists(t, ws.Path, "dry run must not actually remove the worktree")

	exists, err := gitutil.BranchExists(ctx, repoDir, ws.Branch)
	require.NoError(t, err)
	assert.True(t, exists, "dry run must not actually delete the branch")
}

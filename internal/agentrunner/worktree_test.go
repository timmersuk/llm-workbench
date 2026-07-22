package agentrunner

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/timmersuk/llm-workbench/internal/gitutil"
)

// initTestRepo creates a real git repository under reposRoot/repoName with
// one commit, and returns its directory. Real `git` subprocess calls, not
// mocks — worktree.go is a thin wrapper around the git CLI, so the CLI's
// actual behavior is what needs verifying.
func initTestRepo(t *testing.T, reposRoot, repoName string) string {
	t.Helper()
	dir := filepath.Join(reposRoot, repoName)
	require.NoError(t, os.MkdirAll(dir, 0o755))

	run := func(args ...string) {
		out, err := gitutil.RunGit(context.Background(), dir, args...)
		require.NoErrorf(t, err, "git %v: %s", args, out)
	}
	run("init", "-q", "-b", "main")
	run("config", "user.email", "test@example.com")
	run("config", "user.name", "Test")
	require.NoError(t, os.WriteFile(filepath.Join(dir, "README.md"), []byte("hello\n"), 0o644))
	run("add", ".")
	run("commit", "-q", "-m", "init")

	return dir
}

func TestResolveExecutionWorkspace_CreatesIsolatedWorktreeOnNewBranch(t *testing.T) {
	reposRoot := t.TempDir()
	initTestRepo(t, reposRoot, "myrepo")

	ws, err := ResolveExecutionWorkspace(context.Background(), reposRoot, []string{"github.com/x/myrepo"}, "task-a", "exec-001", "", "main")
	require.NoError(t, err)

	assert.Equal(t, "task-exec/task-a/exec-001", ws.Branch)
	assert.NotEmpty(t, ws.BaseBranch)
	assert.DirExists(t, ws.Path)
	assert.FileExists(t, filepath.Join(ws.Path, "README.md"))

	// The shared checkout itself must be untouched: still on its original
	// branch, not the new one.
	branch, err := gitutil.RunGit(context.Background(), filepath.Join(reposRoot, "myrepo"), "rev-parse", "--abbrev-ref", "HEAD")
	require.NoError(t, err)
	assert.Equal(t, ws.BaseBranch, strings.TrimSpace(branch))
}

// TestResolveExecutionWorkspace_ForkFromForksNewBranchButKeepsMainAsBaseBranch
// locks in docs/adr/0012: passing forkFrom cuts the new branch from that ref
// (so the new worktree already contains whatever forkFrom had committed),
// while BaseBranch on the returned ExecutionWorkspace still reports the
// shared checkout's own branch — independent of forkFrom — so
// CollectExecutionOutput/CollectExecutionPatch keep diffing against main.
func TestResolveExecutionWorkspace_ForkFromForksNewBranchButKeepsMainAsBaseBranch(t *testing.T) {
	reposRoot := t.TempDir()
	repoDir := initTestRepo(t, reposRoot, "myrepo")

	priorWs, err := ResolveExecutionWorkspace(context.Background(), reposRoot, []string{"github.com/x/myrepo"}, "task-a", "exec-001", "", "main")
	require.NoError(t, err)

	require.NoError(t, os.WriteFile(filepath.Join(priorWs.Path, "wip.txt"), []byte("prior attempt\n"), 0o644))
	_, err = gitutil.RunGit(context.Background(), priorWs.Path, "add", ".")
	require.NoError(t, err)
	_, err = gitutil.RunGit(context.Background(), priorWs.Path, "commit", "-q", "-m", "prior attempt")
	require.NoError(t, err)

	retryWs, err := ResolveExecutionWorkspace(context.Background(), reposRoot, []string{"github.com/x/myrepo"}, "task-a", "exec-002", priorWs.Branch, "main")
	require.NoError(t, err)

	assert.Equal(t, "task-exec/task-a/exec-002", retryWs.Branch)
	assert.FileExists(t, filepath.Join(retryWs.Path, "wip.txt"))
	assert.Equal(t, priorWs.BaseBranch, retryWs.BaseBranch)

	// The shared checkout is still untouched by either resolution.
	branch, err := gitutil.RunGit(context.Background(), repoDir, "rev-parse", "--abbrev-ref", "HEAD")
	require.NoError(t, err)
	assert.Equal(t, retryWs.BaseBranch, strings.TrimSpace(branch))
}

func TestResolveExecutionWorkspace_WorktreeStaysUnderReposRoot(t *testing.T) {
	reposRoot := t.TempDir()
	initTestRepo(t, reposRoot, "myrepo")

	ws, err := ResolveExecutionWorkspace(context.Background(), reposRoot, []string{"github.com/x/myrepo"}, "task-a", "exec-001", "", "main")
	require.NoError(t, err)

	rootAbs, err := filepath.Abs(reposRoot)
	require.NoError(t, err)
	rel, err := filepath.Rel(rootAbs, ws.Path)
	require.NoError(t, err)
	assert.False(t, rel == ".." || filepath.IsAbs(rel) || len(rel) >= 2 && rel[:2] == ".."+string(filepath.Separator))
}

func TestResolveExecutionWorkspace_RejectsUnsafeIDs(t *testing.T) {
	reposRoot := t.TempDir()
	initTestRepo(t, reposRoot, "myrepo")

	_, err := ResolveExecutionWorkspace(context.Background(), reposRoot, []string{"github.com/x/myrepo"}, "../escape", "exec-001", "", "main")
	assert.ErrorIs(t, err, ErrInvalidRepository)

	_, err = ResolveExecutionWorkspace(context.Background(), reposRoot, []string{"github.com/x/myrepo"}, "task-a", "../escape", "", "main")
	assert.ErrorIs(t, err, ErrInvalidRepository)
}

func TestCollectExecutionOutput_ReturnsCommitsAndChangedFiles(t *testing.T) {
	reposRoot := t.TempDir()
	initTestRepo(t, reposRoot, "myrepo")

	ws, err := ResolveExecutionWorkspace(context.Background(), reposRoot, []string{"github.com/x/myrepo"}, "task-a", "exec-001", "", "main")
	require.NoError(t, err)

	require.NoError(t, os.WriteFile(filepath.Join(ws.Path, "new-file.txt"), []byte("content\n"), 0o644))
	_, err = gitutil.RunGit(context.Background(), ws.Path, "add", ".")
	require.NoError(t, err)
	_, err = gitutil.RunGit(context.Background(), ws.Path, "commit", "-q", "-m", "add new file")
	require.NoError(t, err)

	commits, artifacts, err := CollectExecutionOutput(context.Background(), ws, "")
	require.NoError(t, err)
	assert.Len(t, commits, 1)
	assert.Equal(t, []string{"new-file.txt"}, artifacts)
}

func TestCollectExecutionOutput_EmptyWhenNoCommitsMade(t *testing.T) {
	reposRoot := t.TempDir()
	initTestRepo(t, reposRoot, "myrepo")

	ws, err := ResolveExecutionWorkspace(context.Background(), reposRoot, []string{"github.com/x/myrepo"}, "task-a", "exec-001", "", "main")
	require.NoError(t, err)

	commits, artifacts, err := CollectExecutionOutput(context.Background(), ws, "")
	require.NoError(t, err)
	assert.Empty(t, commits)
	assert.Empty(t, artifacts)
}

func TestCollectExecutionPatch_ReturnsCommitsAndRealDiff(t *testing.T) {
	reposRoot := t.TempDir()
	initTestRepo(t, reposRoot, "myrepo")

	ws, err := ResolveExecutionWorkspace(context.Background(), reposRoot, []string{"github.com/x/myrepo"}, "task-a", "exec-001", "", "main")
	require.NoError(t, err)

	require.NoError(t, os.WriteFile(filepath.Join(ws.Path, "new-file.txt"), []byte("added line\n"), 0o644))
	_, err = gitutil.RunGit(context.Background(), ws.Path, "add", ".")
	require.NoError(t, err)
	_, err = gitutil.RunGit(context.Background(), ws.Path, "commit", "-q", "-m", "add new file")
	require.NoError(t, err)

	commits, patch, err := CollectExecutionPatch(context.Background(), ws, "")
	require.NoError(t, err)
	assert.Len(t, commits, 1)
	// The full-patch variant carries the actual diff text, not just names.
	assert.Contains(t, patch, "new-file.txt")
	assert.Contains(t, patch, "+added line")
}

// TestCollectExecutionPatch_UsesMergeBaseDiffSoAdvancingBaseBranchDoesntLeakIn
// proves the two-dot-to-three-dot fix: if BaseBranch advances with an
// unrelated commit after the execution's worktree forked (e.g. during a
// long-running needs_changes retry cycle), a two-dot diff would compare the
// two branch tips directly and show the unrelated commit's file as removed;
// a three-dot (merge-base) diff must not.
func TestCollectExecutionPatch_UsesMergeBaseDiffSoAdvancingBaseBranchDoesntLeakIn(t *testing.T) {
	reposRoot := t.TempDir()
	repoDir := initTestRepo(t, reposRoot, "myrepo")

	ws, err := ResolveExecutionWorkspace(context.Background(), reposRoot, []string{"github.com/x/myrepo"}, "task-a", "exec-001", "", "main")
	require.NoError(t, err)

	require.NoError(t, os.WriteFile(filepath.Join(ws.Path, "new-file.txt"), []byte("added line\n"), 0o644))
	_, err = gitutil.RunGit(context.Background(), ws.Path, "add", ".")
	require.NoError(t, err)
	_, err = gitutil.RunGit(context.Background(), ws.Path, "commit", "-q", "-m", "add new file")
	require.NoError(t, err)

	// Advance the shared checkout's own branch with an unrelated commit
	// after the worktree already forked.
	require.NoError(t, os.WriteFile(filepath.Join(repoDir, "unrelated.txt"), []byte("unrelated\n"), 0o644))
	_, err = gitutil.RunGit(context.Background(), repoDir, "add", ".")
	require.NoError(t, err)
	_, err = gitutil.RunGit(context.Background(), repoDir, "commit", "-q", "-m", "unrelated change on main")
	require.NoError(t, err)

	_, patch, err := CollectExecutionPatch(context.Background(), ws, "")
	require.NoError(t, err)
	assert.Contains(t, patch, "new-file.txt")
	assert.NotContains(t, patch, "unrelated.txt", "merge-base diff must not surface changes only on BaseBranch's advanced tip")

	_, artifacts, err := CollectExecutionOutput(context.Background(), ws, "")
	require.NoError(t, err)
	assert.Equal(t, []string{"new-file.txt"}, artifacts)
}

// TestCollectExecutionOutput_ForkPointScopesCommitsButNotTheDiff proves the
// "Commits over-reporting" fix: a retry forked from a prior attempt's branch
// tip (docs/adr/0012) reports only its own commits when forkPoint names that
// prior branch, not the ancestor attempt's commits too — while the diff
// stays cumulative against BaseBranch regardless, since Review wants the
// full change so far, not just the latest attempt's slice of it.
func TestCollectExecutionOutput_ForkPointScopesCommitsButNotTheDiff(t *testing.T) {
	reposRoot := t.TempDir()
	initTestRepo(t, reposRoot, "myrepo")

	priorWs, err := ResolveExecutionWorkspace(context.Background(), reposRoot, []string{"github.com/x/myrepo"}, "task-a", "exec-001", "", "main")
	require.NoError(t, err)
	require.NoError(t, os.WriteFile(filepath.Join(priorWs.Path, "first-file.txt"), []byte("first attempt\n"), 0o644))
	_, err = gitutil.RunGit(context.Background(), priorWs.Path, "add", ".")
	require.NoError(t, err)
	_, err = gitutil.RunGit(context.Background(), priorWs.Path, "commit", "-q", "-m", "first attempt")
	require.NoError(t, err)

	retryWs, err := ResolveExecutionWorkspace(context.Background(), reposRoot, []string{"github.com/x/myrepo"}, "task-a", "exec-002", priorWs.Branch, "main")
	require.NoError(t, err)
	require.NoError(t, os.WriteFile(filepath.Join(retryWs.Path, "second-file.txt"), []byte("retry\n"), 0o644))
	_, err = gitutil.RunGit(context.Background(), retryWs.Path, "add", ".")
	require.NoError(t, err)
	_, err = gitutil.RunGit(context.Background(), retryWs.Path, "commit", "-q", "-m", "retry attempt")
	require.NoError(t, err)

	commits, artifacts, err := CollectExecutionOutput(context.Background(), retryWs, priorWs.Branch)
	require.NoError(t, err)
	assert.Len(t, commits, 1, "only this attempt's own commit, not the prior attempt's too")
	// The diff/artifacts list is unaffected by forkPoint — still cumulative
	// against BaseBranch, so it shows both attempts' files.
	assert.ElementsMatch(t, []string{"first-file.txt", "second-file.txt"}, artifacts)

	// Passing no forkPoint (as a first attempt would) falls back to
	// BaseBranch, which for this same retry workspace would report both
	// commits — confirming forkPoint is what does the scoping, not some
	// property of the workspace itself.
	allCommits, _, err := CollectExecutionOutput(context.Background(), retryWs, "")
	require.NoError(t, err)
	assert.Len(t, allCommits, 2)
}

func TestResolveReviewWorkspace_LocatesExistingExecutionWorktree(t *testing.T) {
	reposRoot := t.TempDir()
	initTestRepo(t, reposRoot, "myrepo")

	created, err := ResolveExecutionWorkspace(context.Background(), reposRoot, []string{"github.com/x/myrepo"}, "task-a", "exec-001", "", "main")
	require.NoError(t, err)

	// Review resolves the same worktree the execution left in place, without
	// creating anything new.
	review, err := ResolveReviewWorkspace(context.Background(), reposRoot, []string{"github.com/x/myrepo"}, "exec-001", "main")
	require.NoError(t, err)
	assert.Equal(t, created.Path, review.Path)
	assert.Equal(t, created.Branch, review.Branch)
	assert.Equal(t, created.BaseBranch, review.BaseBranch)
}

func TestResolveReviewWorkspace_MissingWorktreeIsAnError(t *testing.T) {
	reposRoot := t.TempDir()
	initTestRepo(t, reposRoot, "myrepo")

	_, err := ResolveReviewWorkspace(context.Background(), reposRoot, []string{"github.com/x/myrepo"}, "exec-404", "main")
	assert.ErrorIs(t, err, ErrInvalidRepository)
}

func TestResolveReviewWorkspace_RejectsUnsafeID(t *testing.T) {
	reposRoot := t.TempDir()
	initTestRepo(t, reposRoot, "myrepo")

	_, err := ResolveReviewWorkspace(context.Background(), reposRoot, []string{"github.com/x/myrepo"}, "../escape", "main")
	assert.ErrorIs(t, err, ErrInvalidRepository)
}

// TestResolveExecutionWorkspace_RefusesToForkFromWrongBranch locks in the
// one blocking check docs/milestones/milestone8a.md introduces: unlike
// behind-origin/dirty-working-tree (advisory only, not built in this PR), a
// shared checkout that isn't on the project's known default branch must
// refuse to fork a worktree at all, since a fork from the wrong branch
// commits an entire execution to the wrong PR base.
func TestResolveExecutionWorkspace_RefusesToForkFromWrongBranch(t *testing.T) {
	reposRoot := t.TempDir()
	repoDir := initTestRepo(t, reposRoot, "myrepo")
	_, err := gitutil.RunGit(context.Background(), repoDir, "checkout", "-q", "-b", "some-feature-branch")
	require.NoError(t, err)

	_, err = ResolveExecutionWorkspace(context.Background(), reposRoot, []string{"github.com/x/myrepo"}, "task-a", "exec-001", "", "main")
	assert.ErrorIs(t, err, ErrWrongBranch)
}

// TestResolveExecutionWorkspace_EmptyDefaultBranchFailsClosed proves the
// fail-closed decision: an empty defaultBranch (never determined) blocks
// exactly like a real mismatch does, rather than being treated as "no
// expectation, allow anything."
func TestResolveExecutionWorkspace_EmptyDefaultBranchFailsClosed(t *testing.T) {
	reposRoot := t.TempDir()
	initTestRepo(t, reposRoot, "myrepo")

	_, err := ResolveExecutionWorkspace(context.Background(), reposRoot, []string{"github.com/x/myrepo"}, "task-a", "exec-001", "", "")
	assert.ErrorIs(t, err, ErrWrongBranch)
}

// TestResolveReviewWorkspace_RefusesWrongBranch mirrors the execution-side
// test: ResolveReviewWorkspace re-derives BaseBranch from the shared
// checkout on every call (even though it never forks a new worktree), and a
// wrong branch there would corrupt the review's diff just as seriously as a
// bad fork would.
func TestResolveReviewWorkspace_RefusesWrongBranch(t *testing.T) {
	reposRoot := t.TempDir()
	repoDir := initTestRepo(t, reposRoot, "myrepo")

	_, err := ResolveExecutionWorkspace(context.Background(), reposRoot, []string{"github.com/x/myrepo"}, "task-a", "exec-001", "", "main")
	require.NoError(t, err)

	_, err = gitutil.RunGit(context.Background(), repoDir, "checkout", "-q", "-b", "some-feature-branch")
	require.NoError(t, err)

	_, err = ResolveReviewWorkspace(context.Background(), reposRoot, []string{"github.com/x/myrepo"}, "exec-001", "main")
	assert.ErrorIs(t, err, ErrWrongBranch)
}

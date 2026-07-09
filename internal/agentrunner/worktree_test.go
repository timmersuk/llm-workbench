package agentrunner

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
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
		out, err := runGit(context.Background(), dir, args...)
		require.NoErrorf(t, err, "git %v: %s", args, out)
	}
	run("init", "-q")
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

	ws, err := ResolveExecutionWorkspace(context.Background(), reposRoot, []string{"github.com/x/myrepo"}, "task-a", "exec-001")
	require.NoError(t, err)

	assert.Equal(t, "task-exec/task-a/exec-001", ws.Branch)
	assert.NotEmpty(t, ws.BaseBranch)
	assert.DirExists(t, ws.Path)
	assert.FileExists(t, filepath.Join(ws.Path, "README.md"))

	// The shared checkout itself must be untouched: still on its original
	// branch, not the new one.
	branch, err := runGit(context.Background(), filepath.Join(reposRoot, "myrepo"), "rev-parse", "--abbrev-ref", "HEAD")
	require.NoError(t, err)
	assert.Equal(t, ws.BaseBranch, strings.TrimSpace(branch))
}

func TestResolveExecutionWorkspace_WorktreeStaysUnderReposRoot(t *testing.T) {
	reposRoot := t.TempDir()
	initTestRepo(t, reposRoot, "myrepo")

	ws, err := ResolveExecutionWorkspace(context.Background(), reposRoot, []string{"github.com/x/myrepo"}, "task-a", "exec-001")
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

	_, err := ResolveExecutionWorkspace(context.Background(), reposRoot, []string{"github.com/x/myrepo"}, "../escape", "exec-001")
	assert.ErrorIs(t, err, ErrInvalidRepository)

	_, err = ResolveExecutionWorkspace(context.Background(), reposRoot, []string{"github.com/x/myrepo"}, "task-a", "../escape")
	assert.ErrorIs(t, err, ErrInvalidRepository)
}

func TestCollectExecutionOutput_ReturnsCommitsAndChangedFiles(t *testing.T) {
	reposRoot := t.TempDir()
	initTestRepo(t, reposRoot, "myrepo")

	ws, err := ResolveExecutionWorkspace(context.Background(), reposRoot, []string{"github.com/x/myrepo"}, "task-a", "exec-001")
	require.NoError(t, err)

	require.NoError(t, os.WriteFile(filepath.Join(ws.Path, "new-file.txt"), []byte("content\n"), 0o644))
	_, err = runGit(context.Background(), ws.Path, "add", ".")
	require.NoError(t, err)
	_, err = runGit(context.Background(), ws.Path, "commit", "-q", "-m", "add new file")
	require.NoError(t, err)

	commits, artifacts, err := CollectExecutionOutput(context.Background(), ws)
	require.NoError(t, err)
	assert.Len(t, commits, 1)
	assert.Equal(t, []string{"new-file.txt"}, artifacts)
}

func TestCollectExecutionOutput_EmptyWhenNoCommitsMade(t *testing.T) {
	reposRoot := t.TempDir()
	initTestRepo(t, reposRoot, "myrepo")

	ws, err := ResolveExecutionWorkspace(context.Background(), reposRoot, []string{"github.com/x/myrepo"}, "task-a", "exec-001")
	require.NoError(t, err)

	commits, artifacts, err := CollectExecutionOutput(context.Background(), ws)
	require.NoError(t, err)
	assert.Empty(t, commits)
	assert.Empty(t, artifacts)
}

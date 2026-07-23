package gitutil

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// initTestRepo creates a real git repository with one commit, mirroring
// agentrunner's own initTestRepo helper — real `git` subprocess calls, not
// mocks, since this package is a thin wrapper around the git CLI.
func initTestRepo(t *testing.T, dir string) {
	t.Helper()
	require.NoError(t, os.MkdirAll(dir, 0o755))

	run := func(args ...string) {
		out, err := RunGit(context.Background(), dir, args...)
		require.NoErrorf(t, err, "git %v: %s", args, out)
	}
	run("init", "-q")
	run("config", "user.email", "test@example.com")
	run("config", "user.name", "Test")
	require.NoError(t, os.WriteFile(filepath.Join(dir, "README.md"), []byte("hello\n"), 0o644))
	run("add", ".")
	run("commit", "-q", "-m", "init")
}

func TestEnsureGitExclude_WritesPatternsToInfoExclude(t *testing.T) {
	dir := filepath.Join(t.TempDir(), "repo")
	initTestRepo(t, dir)

	require.NoError(t, EnsureGitExclude(context.Background(), dir, "pr-comments.yaml", ".llm-workbench/"))

	content, err := os.ReadFile(filepath.Join(dir, ".git", "info", "exclude"))
	require.NoError(t, err)
	assert.Contains(t, string(content), "pr-comments.yaml")
	assert.Contains(t, string(content), ".llm-workbench/")
}

func TestEnsureGitExclude_Idempotent_NoDuplicateLines(t *testing.T) {
	dir := filepath.Join(t.TempDir(), "repo")
	initTestRepo(t, dir)

	require.NoError(t, EnsureGitExclude(context.Background(), dir, "pr-comments.yaml"))
	require.NoError(t, EnsureGitExclude(context.Background(), dir, "pr-comments.yaml"))

	content, err := os.ReadFile(filepath.Join(dir, ".git", "info", "exclude"))
	require.NoError(t, err)
	assert.Equal(t, 1, countOccurrences(string(content), "pr-comments.yaml"))
}

func TestEnsureGitExclude_PreservesExistingContent(t *testing.T) {
	dir := filepath.Join(t.TempDir(), "repo")
	initTestRepo(t, dir)
	require.NoError(t, os.WriteFile(filepath.Join(dir, ".git", "info", "exclude"), []byte("*.log\n"), 0o644))

	require.NoError(t, EnsureGitExclude(context.Background(), dir, "pr-comments.yaml"))

	content, err := os.ReadFile(filepath.Join(dir, ".git", "info", "exclude"))
	require.NoError(t, err)
	assert.Contains(t, string(content), "*.log")
	assert.Contains(t, string(content), "pr-comments.yaml")
}

func TestEnsureGitExclude_FromLinkedWorktree_WritesToSharedCommonDir(t *testing.T) {
	dir := filepath.Join(t.TempDir(), "repo")
	initTestRepo(t, dir)

	worktreeDir := filepath.Join(filepath.Dir(dir), "wt")
	out, err := RunGit(context.Background(), dir, "worktree", "add", "-b", "feature", worktreeDir)
	require.NoErrorf(t, err, "git worktree add: %s", out)

	// EnsureGitExclude called from the *worktree* must still land in the
	// main repo's shared .git/info/exclude, not a nonexistent per-worktree one.
	require.NoError(t, EnsureGitExclude(context.Background(), worktreeDir, "pr-comments.yaml"))

	content, err := os.ReadFile(filepath.Join(dir, ".git", "info", "exclude"))
	require.NoError(t, err)
	assert.Contains(t, string(content), "pr-comments.yaml")
}

func TestClone_ClonesRealRepository(t *testing.T) {
	src := filepath.Join(t.TempDir(), "source-repo")
	initTestRepo(t, src)

	dest := filepath.Join(t.TempDir(), "cloned-repo")
	require.NoError(t, Clone(context.Background(), src, dest))

	content, err := os.ReadFile(filepath.Join(dest, "README.md"))
	require.NoError(t, err)
	// TrimSpace rather than exact equality: git's core.autocrlf can convert
	// the checked-out line ending to \r\n on Windows, unrelated to whether
	// Clone itself worked.
	assert.Equal(t, "hello", strings.TrimSpace(string(content)))
}

func TestClone_FailureIsError(t *testing.T) {
	dest := filepath.Join(t.TempDir(), "cloned-repo")
	err := Clone(context.Background(), filepath.Join(t.TempDir(), "does-not-exist"), dest)
	assert.Error(t, err)
}

func TestDirtyWorkingTree_CleanIsNotDirty(t *testing.T) {
	dir := filepath.Join(t.TempDir(), "repo")
	initTestRepo(t, dir)

	got := DirtyWorkingTree(context.Background(), dir)
	assert.True(t, got.Known)
	assert.False(t, got.Dirty)
}

func TestDirtyWorkingTree_ModifiedTrackedFileIsDirty(t *testing.T) {
	dir := filepath.Join(t.TempDir(), "repo")
	initTestRepo(t, dir)
	require.NoError(t, os.WriteFile(filepath.Join(dir, "README.md"), []byte("changed\n"), 0o644))

	got := DirtyWorkingTree(context.Background(), dir)
	assert.True(t, got.Known)
	assert.True(t, got.Dirty)
}

func TestDirtyWorkingTree_UntrackedFileIsDirty(t *testing.T) {
	dir := filepath.Join(t.TempDir(), "repo")
	initTestRepo(t, dir)
	require.NoError(t, os.WriteFile(filepath.Join(dir, "scratch.txt"), []byte("x\n"), 0o644))

	got := DirtyWorkingTree(context.Background(), dir)
	assert.True(t, got.Known)
	assert.True(t, got.Dirty, "untracked files count as dirty (docs/milestones/milestone8a.md's resolved open question)")
}

func TestDirtyWorkingTree_NotAGitRepoIsUnknown(t *testing.T) {
	dir := t.TempDir()

	got := DirtyWorkingTree(context.Background(), dir)
	assert.False(t, got.Known)
}

func TestBehindOrigin_UpToDateIsZero(t *testing.T) {
	src := filepath.Join(t.TempDir(), "source-repo")
	initTestRepo(t, src)
	clone := filepath.Join(t.TempDir(), "clone")
	require.NoError(t, Clone(context.Background(), src, clone))

	got := BehindOrigin(context.Background(), clone)
	assert.True(t, got.Known)
	assert.Equal(t, 0, got.Behind)
}

func TestBehindOrigin_ReportsCommitsBehind(t *testing.T) {
	src := filepath.Join(t.TempDir(), "source-repo")
	initTestRepo(t, src)
	clone := filepath.Join(t.TempDir(), "clone")
	require.NoError(t, Clone(context.Background(), src, clone))

	// Advance the source after the clone, so the clone's origin/<branch> is
	// now ahead of the clone's own HEAD.
	require.NoError(t, os.WriteFile(filepath.Join(src, "second.txt"), []byte("x\n"), 0o644))
	_, err := RunGit(context.Background(), src, "add", ".")
	require.NoError(t, err)
	_, err = RunGit(context.Background(), src, "commit", "-q", "-m", "second commit")
	require.NoError(t, err)

	got := BehindOrigin(context.Background(), clone)
	assert.True(t, got.Known)
	assert.Equal(t, 1, got.Behind)
}

func TestBehindOrigin_NoUpstreamIsUnknown(t *testing.T) {
	dir := filepath.Join(t.TempDir(), "repo")
	initTestRepo(t, dir) // plain init, no clone -> no origin, no upstream tracking

	got := BehindOrigin(context.Background(), dir)
	assert.False(t, got.Known)
}

func TestBehindOrigin_NotAGitRepoIsUnknown(t *testing.T) {
	dir := t.TempDir()

	got := BehindOrigin(context.Background(), dir)
	assert.False(t, got.Known)
}

func countOccurrences(haystack, needle string) int {
	count := 0
	for i := 0; i+len(needle) <= len(haystack); i++ {
		if haystack[i:i+len(needle)] == needle {
			count++
			i += len(needle) - 1
		}
	}
	return count
}

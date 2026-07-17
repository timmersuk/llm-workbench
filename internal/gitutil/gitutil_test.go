package gitutil

import (
	"context"
	"os"
	"path/filepath"
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

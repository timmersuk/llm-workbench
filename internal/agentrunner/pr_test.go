package agentrunner

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/timmersuk/llm-workbench/internal/gitutil"
)

// fakeGitHubPRClient stands in for the real `gh` CLI in tests — no network
// or GitHub auth involved, per docs/milestones/done/milestone7.md PR 2 decision
// 4 (GitHubPRClient exists specifically to make this fakeable).
type fakeGitHubPRClient struct {
	createCalls int
	createURL   string
	createErr   error
	state       string
	stateErr    error
	comments    PRCommentsYAML
	commentsErr error
}

func (f *fakeGitHubPRClient) Create(_ context.Context, _, _, _, _, _ string) (string, int, error) {
	f.createCalls++
	if f.createErr != nil {
		return "", 0, f.createErr
	}
	url := f.createURL
	if url == "" {
		url = fmt.Sprintf("https://github.com/org/repo/pull/%d", 100+f.createCalls)
	}
	// parsePRNumber is exercised directly by TestParsePRNumber; every URL
	// used in this file is a fixed, well-formed constant, so any error here
	// would indicate a broken test fixture, not something worth a fake
	// *testing.T just to assert on.
	number, _ := parsePRNumber(url)
	return url, number, nil
}

func (f *fakeGitHubPRClient) State(_ context.Context, _ string, _ int) (string, error) {
	if f.stateErr != nil {
		return "", f.stateErr
	}
	if f.state == "" {
		return "OPEN", nil
	}
	return f.state, nil
}

func (f *fakeGitHubPRClient) Comments(_ context.Context, _ string, _ int) (PRCommentsYAML, error) {
	if f.commentsErr != nil {
		return "", f.commentsErr
	}
	return f.comments, nil
}

// initBareRemote creates a bare git repo standing in for `origin`, and
// wires it into repoDir as the "origin" remote — a real remote to push
// against, not a mock, mirroring initTestRepo's "real git subprocess"
// philosophy.
func initBareRemote(t *testing.T, root, repoDir string) string {
	t.Helper()
	remoteDir := filepath.Join(root, "origin.git")
	require.NoError(t, os.MkdirAll(remoteDir, 0o755))

	run := func(dir string, args ...string) {
		out, err := gitutil.RunGit(context.Background(), dir, args...)
		require.NoErrorf(t, err, "git %v: %s", args, out)
	}
	run(remoteDir, "init", "--bare", "-q")
	run(repoDir, "remote", "add", "origin", remoteDir)
	return remoteDir
}

func remoteHasBranch(t *testing.T, remoteDir, branch string) bool {
	t.Helper()
	out, err := gitutil.RunGit(context.Background(), remoteDir, "branch", "--list", branch)
	require.NoError(t, err)
	return len(out) > 0
}

func TestPushAndOpenPR_NoExistingPR_PushesAndCreates(t *testing.T) {
	reposRoot := t.TempDir()
	repoDir := initTestRepo(t, reposRoot, "myrepo")
	remoteDir := initBareRemote(t, reposRoot, repoDir)
	// Creates the branch ref without switching to it — the shared checkout
	// (repoDir) stays on its own branch, exactly as it would in production
	// while a real execution's branch lives in a separate worktree; only
	// the ref itself needs to exist for `git push` to find it by name.
	_, err := gitutil.RunGit(context.Background(), repoDir, "branch", "feature-a")
	require.NoError(t, err)

	client := &fakeGitHubPRClient{createURL: "https://github.com/org/repo/pull/42"}
	url, number, branch, err := PushAndOpenPR(context.Background(), repoDir, "feature-a", "Fix login bug", "Ships the fix.", "", 0, "", client)
	require.NoError(t, err)

	assert.Equal(t, "https://github.com/org/repo/pull/42", url)
	assert.Equal(t, 42, number)
	assert.Equal(t, "feature-a", branch)
	assert.Equal(t, 1, client.createCalls)
	assert.True(t, remoteHasBranch(t, remoteDir, "feature-a"), "the branch must actually land on the remote")
}

func TestPushAndOpenPR_ExistingOpenPR_RefspecPushesWithoutCreating(t *testing.T) {
	reposRoot := t.TempDir()
	repoDir := initTestRepo(t, reposRoot, "myrepo")
	remoteDir := initBareRemote(t, reposRoot, repoDir)
	_, err := gitutil.RunGit(context.Background(), repoDir, "checkout", "-b", "task-exec/task-a/exec-002")
	require.NoError(t, err)

	client := &fakeGitHubPRClient{state: "OPEN"}
	url, number, branch, err := PushAndOpenPR(context.Background(), repoDir, "task-exec/task-a/exec-002", "Fix login bug", "Ships the fix.",
		"https://github.com/org/repo/pull/42", 42, "task-exec/task-a/exec-001", client)
	require.NoError(t, err)

	assert.Equal(t, "https://github.com/org/repo/pull/42", url, "reuses the existing PR's URL")
	assert.Equal(t, 42, number, "reuses the existing PR's number")
	assert.Equal(t, "task-exec/task-a/exec-001", branch, "still tracks the PR's original remote branch, not the new local branch")
	assert.Equal(t, 0, client.createCalls, "no new PR should be opened for an already-open one")
	assert.True(t, remoteHasBranch(t, remoteDir, "task-exec/task-a/exec-001"), "the new attempt's commits must land on the existing PR's remote branch")
	assert.False(t, remoteHasBranch(t, remoteDir, "task-exec/task-a/exec-002"), "refspec push must not also create a same-named remote branch")
}

func TestPushAndOpenPR_ExistingClosedPR_OpensFreshPR(t *testing.T) {
	reposRoot := t.TempDir()
	repoDir := initTestRepo(t, reposRoot, "myrepo")
	remoteDir := initBareRemote(t, reposRoot, repoDir)
	_, err := gitutil.RunGit(context.Background(), repoDir, "checkout", "-b", "task-exec/task-a/exec-002")
	require.NoError(t, err)

	client := &fakeGitHubPRClient{state: "CLOSED", createURL: "https://github.com/org/repo/pull/55"}
	url, number, branch, err := PushAndOpenPR(context.Background(), repoDir, "task-exec/task-a/exec-002", "Fix login bug", "Ships the fix.",
		"https://github.com/org/repo/pull/42", 42, "task-exec/task-a/exec-001", client)
	require.NoError(t, err)

	assert.Equal(t, "https://github.com/org/repo/pull/55", url, "overwrites the stale closed PR's record")
	assert.Equal(t, 55, number)
	assert.Equal(t, "task-exec/task-a/exec-002", branch, "tracks the new attempt's own branch, not the closed PR's old one")
	assert.Equal(t, 1, client.createCalls)
	assert.True(t, remoteHasBranch(t, remoteDir, "task-exec/task-a/exec-002"), "the new attempt's own branch must be pushed fresh")
}

func TestPushAndOpenPR_StateCheckErrorPropagates(t *testing.T) {
	reposRoot := t.TempDir()
	repoDir := initTestRepo(t, reposRoot, "myrepo")
	initBareRemote(t, reposRoot, repoDir)

	client := &fakeGitHubPRClient{stateErr: assert.AnError}
	_, _, _, err := PushAndOpenPR(context.Background(), repoDir, "feature-a", "t", "b", "https://github.com/org/repo/pull/42", 42, "old-branch", client)
	require.Error(t, err)
	assert.ErrorIs(t, err, assert.AnError)
}

func TestParsePRNumber(t *testing.T) {
	n, err := parsePRNumber("https://github.com/org/repo/pull/123")
	require.NoError(t, err)
	assert.Equal(t, 123, n)

	_, err = parsePRNumber("not-a-url")
	require.Error(t, err, "no '/' to find a trailing path segment after")

	_, err = parsePRNumber("https://github.com/org/repo/pull/not-a-number")
	require.Error(t, err, "trailing segment isn't numeric")
}

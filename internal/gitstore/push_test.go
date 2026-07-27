package gitstore

import (
	"context"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// TestRunPushWorker_CommitsAndPushesPendingChanges exercises the worker end
// to end: a Create only enqueues (gitstore_test.go's
// TestStore_CreateEnqueuesPendingChange already covers that in isolation),
// and RunPushWorker's next tick must both commit it and push it to the
// bare remote.
func TestRunPushWorker_CommitsAndPushesPendingChanges(t *testing.T) {
	remote := newBareRemote(t)
	workspaceRoot := filepath.Join(t.TempDir(), "workspace")
	store, err := Open(workspaceRoot, remote)
	require.NoError(t, err)

	_, err = store.Projects.Create(newProjectCreateInput("Demo Project"))
	require.NoError(t, err)

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	done := make(chan struct{})
	go func() {
		store.RunPushWorker(ctx, 10*time.Millisecond)
		close(done)
	}()

	require.Eventually(t, func() bool {
		out, err := runGit(remote, "log", "--pretty=%s")
		return err == nil && len(out) > 0
	}, 2*time.Second, 10*time.Millisecond, "expected the pending change to be committed and pushed to the bare remote")

	out, err := runGit(remote, "log", "--pretty=%s")
	require.NoError(t, err)
	assert.Contains(t, out, `Create project "Demo Project"`)

	cancel()
	select {
	case <-done:
	case <-time.After(2 * time.Second):
		t.Fatal("RunPushWorker did not stop after ctx cancellation")
	}
}

// TestRunPushWorker_StopsOnContextCancelWithNothingToPush covers the
// steady-state "nothing pending" tick, which must not be logged as a
// failure or otherwise cause the worker to stop early.
func TestRunPushWorker_StopsOnContextCancelWithNothingToPush(t *testing.T) {
	remote := newBareRemote(t)
	workspaceRoot := filepath.Join(t.TempDir(), "workspace")
	store, err := Open(workspaceRoot, remote)
	require.NoError(t, err)

	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan struct{})
	go func() {
		store.RunPushWorker(ctx, 10*time.Millisecond)
		close(done)
	}()

	// Let a few ticks elapse with nothing to push before cancelling.
	time.Sleep(50 * time.Millisecond)
	cancel()

	select {
	case <-done:
	case <-time.After(2 * time.Second):
		t.Fatal("RunPushWorker did not stop after ctx cancellation")
	}
	assert.True(t, true) // reaching here without a hang/panic is the assertion
}

// TestCommitPending_SquashesPendingChangesToTheSameDirectory covers the
// accepted, deliberate squash: two pending changes touching the same
// task/project directory (queued within the same interval) can only ever
// produce one commit, since the first `git add <dir>` already captures
// both writes. The content of both is never lost — only the second
// message is.
func TestCommitPending_SquashesPendingChangesToTheSameDirectory(t *testing.T) {
	remote := newBareRemote(t)
	workspaceRoot := filepath.Join(t.TempDir(), "workspace")
	store, err := Open(workspaceRoot, remote)
	require.NoError(t, err)

	p, err := store.Projects.Create(newProjectCreateInput("Demo Project"))
	require.NoError(t, err)
	// Drain the Create on its own first — otherwise it would squash
	// together with the two Updates below too (they all touch the same
	// project directory), leaving only one commit total instead of the two
	// this test means to exercise (an initial Create, then one squashed
	// commit covering both Updates).
	require.NoError(t, store.core.commitPending(time.Hour))

	_, err = store.Projects.Update(p.ID, newProjectUpdateInput("Renamed Once"))
	require.NoError(t, err)
	_, err = store.Projects.Update(p.ID, newProjectUpdateInput("Renamed Twice"))
	require.NoError(t, err)

	require.NoError(t, store.core.commitPending(time.Hour))
	commits := logMessages(t, workspaceRoot)
	require.Len(t, commits, 2, "the two Updates to the same project should squash into one commit")
	assert.Equal(t, "Update project demo-project", commits[0])

	got, err := store.Projects.Get(p.ID)
	require.NoError(t, err)
	assert.Equal(t, "Renamed Twice", got.Name, "the squashed commit must still contain the latest content")
}

// TestCommitPending_SweepsStaleUnqueuedChanges covers the crash-recovery
// backstop: a file modified directly on disk (simulating a pending change
// whose in-memory queue entry was lost to a restart) with an old enough
// mtime must be picked up and committed by the next commitPending call,
// even though nothing ever enqueued it.
func TestCommitPending_SweepsStaleUnqueuedChanges(t *testing.T) {
	remote := newBareRemote(t)
	workspaceRoot := filepath.Join(t.TempDir(), "workspace")
	store, err := Open(workspaceRoot, remote)
	require.NoError(t, err)

	_, err = store.Projects.Create(newProjectCreateInput("Demo Project"))
	require.NoError(t, err)
	require.NoError(t, store.core.commitPending(time.Hour))

	// Simulate a write whose pending-queue entry was lost (e.g. a restart
	// between the FileStore write and the tick that would have enqueued
	// it): touch a file directly, bypassing Create/Update entirely, and
	// back-date its mtime so it reads as older than staleThreshold.
	strayPath := filepath.Join(workspaceRoot, "projects", "demo-project", "project.yaml")
	require.NoError(t, os.WriteFile(strayPath, []byte("id: demo-project\nname: Mutated Directly\n"), 0o644))
	old := time.Now().Add(-time.Hour)
	require.NoError(t, os.Chtimes(strayPath, old, old))

	// A short staleThreshold means this stray change is already "old
	// enough" to be swept.
	require.NoError(t, store.core.commitPending(10*time.Millisecond))

	commits := logMessages(t, workspaceRoot)
	require.Len(t, commits, 2)
	assert.Contains(t, commits[0], "Sweep:")
}

// TestCommitPending_LeavesFreshUnqueuedChangesAlone covers the other half
// of the sweep's staleness gate: a dirty file younger than staleThreshold
// must be left alone, since it could still be a write that's legitimately
// about to be enqueued.
func TestCommitPending_LeavesFreshUnqueuedChangesAlone(t *testing.T) {
	remote := newBareRemote(t)
	workspaceRoot := filepath.Join(t.TempDir(), "workspace")
	store, err := Open(workspaceRoot, remote)
	require.NoError(t, err)

	_, err = store.Projects.Create(newProjectCreateInput("Demo Project"))
	require.NoError(t, err)
	require.NoError(t, store.core.commitPending(time.Hour))

	strayPath := filepath.Join(workspaceRoot, "projects", "demo-project", "project.yaml")
	require.NoError(t, os.WriteFile(strayPath, []byte("id: demo-project\nname: Mutated Directly\n"), 0o644))

	// A long staleThreshold means this just-modified file is not old
	// enough to be swept yet.
	require.NoError(t, store.core.commitPending(time.Hour))

	commits := logMessages(t, workspaceRoot)
	require.Len(t, commits, 1, "a fresh unqueued change must not be swept before staleThreshold elapses")
}

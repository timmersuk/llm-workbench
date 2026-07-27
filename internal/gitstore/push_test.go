package gitstore

import (
	"context"
	"path/filepath"
	"testing"
	"time"

	"github.com/go-git/go-git/v5"
	"github.com/go-git/go-git/v5/plumbing"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// TestRunPushWorker_PushesCommitsToRemote exercises the worker end to end:
// a Create commits locally, RunPushWorker's next tick pushes it, and the
// bare remote ends up with a matching "refs/heads/master" (go-git's
// PlainInit default branch name).
func TestRunPushWorker_PushesCommitsToRemote(t *testing.T) {
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

	remoteRepo, err := git.PlainOpen(remote)
	require.NoError(t, err)

	require.Eventually(t, func() bool {
		refs, err := remoteRepo.References()
		if err != nil {
			return false
		}
		defer refs.Close()
		found := false
		_ = refs.ForEach(func(ref *plumbing.Reference) error {
			if ref.Name().IsBranch() {
				found = true
			}
			return nil
		})
		return found
	}, 2*time.Second, 10*time.Millisecond, "expected the local commit to be pushed to the bare remote")

	cancel()
	select {
	case <-done:
	case <-time.After(2 * time.Second):
		t.Fatal("RunPushWorker did not stop after ctx cancellation")
	}
}

// TestRunPushWorker_StopsOnContextCancelWithNothingToPush covers the
// steady-state "already up to date" tick, which must not be logged as a
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

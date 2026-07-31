package task

import (
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestFileStore_GetSessionID_EmptyWhenNoFileYet(t *testing.T) {
	root := t.TempDir()
	store := NewFileStore(root)
	_, err := store.Create("demo-project", Task{ID: "task-a", Title: "A"})
	require.NoError(t, err)

	id, err := store.GetSessionID("demo-project", "task-a", StageRequirements, "claude-code")
	require.NoError(t, err)
	assert.Empty(t, id)
}

func TestFileStore_SetSessionID_RoundTrips(t *testing.T) {
	root := t.TempDir()
	store := NewFileStore(root)
	_, err := store.Create("demo-project", Task{ID: "task-a", Title: "A"})
	require.NoError(t, err)

	err = store.SetSessionID("demo-project", "task-a", StageRequirements, "claude-code", "sess-123")
	require.NoError(t, err)

	id, err := store.GetSessionID("demo-project", "task-a", StageRequirements, "claude-code")
	require.NoError(t, err)
	assert.Equal(t, "sess-123", id)
}

// TestFileStore_SetSessionID_KeyedPerExecutor locks in the per-executor
// isolation the task doc's constraint requires: writing "codex"'s id must
// never disturb "claude-code"'s previously-recorded one, so switching
// executors mid-conversation can't misread a foreign session id as its own.
func TestFileStore_SetSessionID_KeyedPerExecutor(t *testing.T) {
	root := t.TempDir()
	store := NewFileStore(root)
	_, err := store.Create("demo-project", Task{ID: "task-a", Title: "A"})
	require.NoError(t, err)

	require.NoError(t, store.SetSessionID("demo-project", "task-a", StageRequirements, "claude-code", "claude-sess"))
	require.NoError(t, store.SetSessionID("demo-project", "task-a", StageRequirements, "codex", "codex-thread"))

	claudeID, err := store.GetSessionID("demo-project", "task-a", StageRequirements, "claude-code")
	require.NoError(t, err)
	assert.Equal(t, "claude-sess", claudeID)

	codexID, err := store.GetSessionID("demo-project", "task-a", StageRequirements, "codex")
	require.NoError(t, err)
	assert.Equal(t, "codex-thread", codexID)
}

// TestFileStore_SetSessionID_OverwritesSameExecutor covers clearing a stale
// id back to "" after a "not found" resume failure — SetSessionID is a
// plain overwrite, not an append.
func TestFileStore_SetSessionID_OverwritesSameExecutor(t *testing.T) {
	root := t.TempDir()
	store := NewFileStore(root)
	_, err := store.Create("demo-project", Task{ID: "task-a", Title: "A"})
	require.NoError(t, err)

	require.NoError(t, store.SetSessionID("demo-project", "task-a", StageRequirements, "claude-code", "sess-1"))
	require.NoError(t, store.SetSessionID("demo-project", "task-a", StageRequirements, "claude-code", ""))

	id, err := store.GetSessionID("demo-project", "task-a", StageRequirements, "claude-code")
	require.NoError(t, err)
	assert.Empty(t, id)
}

func TestFileStore_SetSessionID_SeparateFilesPerStage(t *testing.T) {
	root := t.TempDir()
	store := NewFileStore(root)
	_, err := store.Create("demo-project", Task{ID: "task-a", Title: "A"})
	require.NoError(t, err)

	require.NoError(t, store.SetSessionID("demo-project", "task-a", StageRequirements, "claude-code", "req-sess"))
	require.NoError(t, store.SetSessionID("demo-project", "task-a", StageReview, "claude-code", "review-sess"))

	reqID, err := store.GetSessionID("demo-project", "task-a", StageRequirements, "claude-code")
	require.NoError(t, err)
	assert.Equal(t, "req-sess", reqID)

	reviewID, err := store.GetSessionID("demo-project", "task-a", StageReview, "claude-code")
	require.NoError(t, err)
	assert.Equal(t, "review-sess", reviewID)
}

func TestFileStore_GetSessionID_RejectsInvalidStage(t *testing.T) {
	root := t.TempDir()
	store := NewFileStore(root)

	_, err := store.GetSessionID("demo-project", "task-a", "implementation", "claude-code")
	require.Error(t, err)
	assert.ErrorIs(t, err, ErrInvalidStage)
}

func TestFileStore_GetSessionID_RejectsPathTraversal(t *testing.T) {
	root := t.TempDir()
	store := NewFileStore(root)

	_, err := store.GetSessionID("demo-project", "../escape", StageRequirements, "claude-code")
	require.Error(t, err)
	assert.ErrorIs(t, err, ErrInvalidID)
}

func TestFileStore_GetTaskDraftSessionID_EmptyWhenNoFileYet(t *testing.T) {
	root := t.TempDir()
	store := NewFileStore(root)

	id, err := store.GetTaskDraftSessionID("demo-project", "session-1", "codex")
	require.NoError(t, err)
	assert.Empty(t, id)
}

func TestFileStore_SetTaskDraftSessionID_RoundTrips(t *testing.T) {
	root := t.TempDir()
	store := NewFileStore(root)

	require.NoError(t, store.SetTaskDraftSessionID("demo-project", "session-1", "codex", "thread-abc"))

	id, err := store.GetTaskDraftSessionID("demo-project", "session-1", "codex")
	require.NoError(t, err)
	assert.Equal(t, "thread-abc", id)

	// Untouched executor key stays empty.
	claudeID, err := store.GetTaskDraftSessionID("demo-project", "session-1", "claude-code")
	require.NoError(t, err)
	assert.Empty(t, claudeID)
}

func TestFileStore_GetTaskDraftSessionID_RejectsPathTraversal(t *testing.T) {
	root := t.TempDir()
	store := NewFileStore(root)

	_, err := store.GetTaskDraftSessionID("demo-project", "../escape", "codex")
	require.Error(t, err)
	assert.ErrorIs(t, err, ErrInvalidID)
}

package task

import (
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func writeTaskFixture(t *testing.T, root, id, yamlBody string) {
	t.Helper()
	dir := filepath.Join(root, id)
	require.NoError(t, os.MkdirAll(dir, 0o755))
	require.NoError(t, os.WriteFile(filepath.Join(dir, "task.yaml"), []byte(yamlBody), 0o644))
}

const validTaskYAML = `
id: TASK-0001
title: Do the thing
project: demo-project
stage: requirements
created_at: 2026-07-05T00:00:00Z
updated_at: 2026-07-05T00:00:00Z
objective: Ship it
constraints: []
assumptions: []
success_criteria: []
references:
  knowledge: []
  repo: []
`

func taskYAMLWithID(id string) string {
	return strings.Replace(validTaskYAML, "TASK-0001", id, 1)
}

func TestFileStore_List(t *testing.T) {
	root := t.TempDir()
	writeTaskFixture(t, root, "TASK-0001", taskYAMLWithID("TASK-0001"))
	writeTaskFixture(t, root, "TASK-0002", taskYAMLWithID("TASK-0002"))

	// Non-directory entries are skipped without becoming a LoadError.
	require.NoError(t, os.WriteFile(filepath.Join(root, "milestone1.md"), []byte("not a task"), 0o644))

	store := NewFileStore(root)
	result, err := store.List()
	require.NoError(t, err)
	require.Len(t, result.Tasks, 2)
	assert.Empty(t, result.Errors)
	assert.Equal(t, "TASK-0001", result.Tasks[0].ID)
	assert.Equal(t, "TASK-0002", result.Tasks[1].ID)
}

func TestFileStore_List_ReportsDirectoryMissingTaskYAMLAsError(t *testing.T) {
	root := t.TempDir()
	writeTaskFixture(t, root, "TASK-0001", taskYAMLWithID("TASK-0001"))
	require.NoError(t, os.MkdirAll(filepath.Join(root, "not-a-task-dir"), 0o755))

	store := NewFileStore(root)
	result, err := store.List()
	require.NoError(t, err)

	require.Len(t, result.Tasks, 1)
	assert.Equal(t, "TASK-0001", result.Tasks[0].ID)

	require.Len(t, result.Errors, 1)
	assert.Equal(t, "not-a-task-dir", result.Errors[0].ID)
}

func TestFileStore_List_MalformedYAML(t *testing.T) {
	root := t.TempDir()
	writeTaskFixture(t, root, "TASK-0001", "id: [this is not valid yaml")

	store := NewFileStore(root)
	result, err := store.List()
	require.NoError(t, err)
	assert.Empty(t, result.Tasks)
	require.Len(t, result.Errors, 1)
	assert.Equal(t, "TASK-0001", result.Errors[0].ID)
	assert.Contains(t, result.Errors[0].Error, "parsing")
}

func TestFileStore_List_SkipsMalformedButKeepsValidTasks(t *testing.T) {
	root := t.TempDir()
	writeTaskFixture(t, root, "TASK-0001", taskYAMLWithID("TASK-0001"))
	writeTaskFixture(t, root, "TASK-0002", "id: [this is not valid yaml")
	writeTaskFixture(t, root, "TASK-0003", taskYAMLWithID("TASK-0003"))

	store := NewFileStore(root)
	result, err := store.List()
	require.NoError(t, err)

	require.Len(t, result.Tasks, 2)
	assert.Equal(t, "TASK-0001", result.Tasks[0].ID)
	assert.Equal(t, "TASK-0003", result.Tasks[1].ID)

	require.Len(t, result.Errors, 1)
	assert.Equal(t, "TASK-0002", result.Errors[0].ID)
	assert.Contains(t, result.Errors[0].Error, "parsing")
}

func TestFileStore_Get(t *testing.T) {
	root := t.TempDir()
	writeTaskFixture(t, root, "TASK-0001", validTaskYAML)

	store := NewFileStore(root)
	tsk, err := store.Get("TASK-0001")
	require.NoError(t, err)
	assert.Equal(t, "TASK-0001", tsk.ID)
	assert.Equal(t, "demo-project", tsk.Project)
}

func TestFileStore_Get_NotFound(t *testing.T) {
	root := t.TempDir()
	store := NewFileStore(root)
	_, err := store.Get("TASK-9999")
	require.Error(t, err)
}

func TestFileStore_Get_RejectsPathTraversal(t *testing.T) {
	root := t.TempDir()
	store := NewFileStore(root)

	for _, id := range []string{"../etc/passwd", "..", "TASK-0001/../../etc", "a/b", `a\b`, ""} {
		_, err := store.Get(id)
		assert.Error(t, err, "expected id %q to be rejected", id)
		assert.ErrorIs(t, err, ErrInvalidID)
	}
}

func TestFileStore_Create_OK(t *testing.T) {
	root := t.TempDir()
	store := NewFileStore(root)

	created, err := store.Create(Task{ID: "fix-login-bug", Title: "Fix login bug", Project: "demo-project"})
	require.NoError(t, err)
	assert.Equal(t, "fix-login-bug", created.ID)
	assert.False(t, created.CreatedAt.IsZero())
	assert.Equal(t, created.CreatedAt, created.UpdatedAt)

	fetched, err := store.Get("fix-login-bug")
	require.NoError(t, err)
	assert.Equal(t, created.ID, fetched.ID)
	assert.Equal(t, created.Title, fetched.Title)
	assert.Equal(t, created.Project, fetched.Project)
	assert.Equal(t, created.CreatedAt, fetched.CreatedAt)
	assert.Equal(t, created.UpdatedAt, fetched.UpdatedAt)
}

func TestFileStore_Create_IgnoresClientSuppliedTimestamps(t *testing.T) {
	root := t.TempDir()
	store := NewFileStore(root)

	stale := time.Date(2000, 1, 1, 0, 0, 0, 0, time.UTC)
	created, err := store.Create(Task{ID: "task-a", Title: "A", CreatedAt: stale, UpdatedAt: stale})
	require.NoError(t, err)
	assert.NotEqual(t, stale, created.CreatedAt)
	assert.NotEqual(t, stale, created.UpdatedAt)
}

func TestFileStore_Create_DefaultsStage(t *testing.T) {
	root := t.TempDir()
	store := NewFileStore(root)

	created, err := store.Create(Task{ID: "task-a", Title: "A", Stage: "complete"})
	require.NoError(t, err)
	assert.Equal(t, StageRequirements, created.Stage)

	fetched, err := store.Get("task-a")
	require.NoError(t, err)
	assert.Equal(t, StageRequirements, fetched.Stage)
}

func TestFileStore_Create_InvalidID(t *testing.T) {
	root := t.TempDir()
	store := NewFileStore(root)

	_, err := store.Create(Task{ID: "../escape", Title: "Bad"})
	require.Error(t, err)
	assert.ErrorIs(t, err, ErrInvalidID)
}

func TestFileStore_Create_AlreadyExists(t *testing.T) {
	root := t.TempDir()
	store := NewFileStore(root)

	_, err := store.Create(Task{ID: "task-a", Title: "First"})
	require.NoError(t, err)

	_, err = store.Create(Task{ID: "task-a", Title: "Second"})
	require.Error(t, err)
	assert.ErrorIs(t, err, ErrAlreadyExists)
}

func TestFileStore_Update_PreservesCreatedAtBumpsUpdatedAt(t *testing.T) {
	root := t.TempDir()
	store := NewFileStore(root)

	created, err := store.Create(Task{ID: "task-a", Title: "First"})
	require.NoError(t, err)

	updated, err := store.Update("task-a", Task{ID: "task-a", Title: "Updated"})
	require.NoError(t, err)
	assert.Equal(t, "Updated", updated.Title)
	assert.Equal(t, created.CreatedAt, updated.CreatedAt)
	assert.True(t, updated.UpdatedAt.After(created.UpdatedAt) || updated.UpdatedAt.Equal(created.UpdatedAt))
}

func TestFileStore_Update_NotFound(t *testing.T) {
	root := t.TempDir()
	store := NewFileStore(root)

	_, err := store.Update("nonexistent", Task{Title: "X"})
	require.Error(t, err)
	assert.True(t, errors.Is(err, os.ErrNotExist))
}

func TestFileStore_Update_RejectsIDMismatch(t *testing.T) {
	root := t.TempDir()
	store := NewFileStore(root)

	_, err := store.Create(Task{ID: "task-a", Title: "First"})
	require.NoError(t, err)

	_, err = store.Update("task-a", Task{ID: "task-b", Title: "Updated"})
	require.Error(t, err)
	assert.ErrorIs(t, err, ErrIDMismatch)
}

func TestFileStore_Update_RejectsPathTraversal(t *testing.T) {
	root := t.TempDir()
	store := NewFileStore(root)

	_, err := store.Update("../escape", Task{Title: "X"})
	require.Error(t, err)
	assert.ErrorIs(t, err, ErrInvalidID)
}

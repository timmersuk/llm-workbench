package task

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

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
status: draft
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

	// Stray entries that must be skipped rather than erroring.
	require.NoError(t, os.WriteFile(filepath.Join(root, "milestone1.md"), []byte("not a task"), 0o644))
	require.NoError(t, os.MkdirAll(filepath.Join(root, "not-a-task-dir"), 0o755))

	store := NewFileStore(root)
	tasks, err := store.List()
	require.NoError(t, err)
	require.Len(t, tasks, 2)
	assert.Equal(t, "TASK-0001", tasks[0].ID)
	assert.Equal(t, "TASK-0002", tasks[1].ID)
}

func TestFileStore_List_MalformedYAML(t *testing.T) {
	root := t.TempDir()
	writeTaskFixture(t, root, "TASK-0001", "id: [this is not valid yaml")

	store := NewFileStore(root)
	_, err := store.List()
	require.Error(t, err)
	assert.ErrorContains(t, err, "parsing")
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

	for _, id := range []string{"../etc/passwd", "..", "TASK-0001/../../etc", "TASK-abc"} {
		_, err := store.Get(id)
		assert.Error(t, err, "expected id %q to be rejected", id)
	}
}

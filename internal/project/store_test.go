package project

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func writeProjectFixture(t *testing.T, root, id, yamlBody string) {
	t.Helper()
	dir := filepath.Join(root, id)
	require.NoError(t, os.MkdirAll(dir, 0o755))
	require.NoError(t, os.WriteFile(filepath.Join(dir, "project.yaml"), []byte(yamlBody), 0o644))
}

const validProjectYAML = `
id: demo-project
name: Demo Project
description: A demo project
repositories:
  - github.com/org/demo
knowledge:
  - coding-standards.md
constraints:
  - no breaking API changes
created_at: 2026-07-05T00:00:00Z
updated_at: 2026-07-05T00:00:00Z
`

func TestFileStore_List(t *testing.T) {
	root := t.TempDir()
	writeProjectFixture(t, root, "demo-project", validProjectYAML)
	writeProjectFixture(t, root, "auth-service", validProjectYAML)

	store := NewFileStore(root)
	projects, err := store.List()
	require.NoError(t, err)
	require.Len(t, projects, 2)
}

func TestFileStore_List_Empty(t *testing.T) {
	root := t.TempDir()
	store := NewFileStore(root)
	projects, err := store.List()
	require.NoError(t, err)
	assert.Empty(t, projects)
}

func TestFileStore_List_MalformedYAML(t *testing.T) {
	root := t.TempDir()
	writeProjectFixture(t, root, "demo-project", "id: [this is not valid yaml")

	store := NewFileStore(root)
	_, err := store.List()
	require.Error(t, err)
	assert.ErrorContains(t, err, "parsing")
}

func TestFileStore_Get(t *testing.T) {
	root := t.TempDir()
	writeProjectFixture(t, root, "demo-project", validProjectYAML)

	store := NewFileStore(root)
	p, err := store.Get("demo-project")
	require.NoError(t, err)
	assert.Equal(t, "demo-project", p.ID)
	assert.Equal(t, "Demo Project", p.Name)
}

func TestFileStore_Get_NotFound(t *testing.T) {
	root := t.TempDir()
	store := NewFileStore(root)
	_, err := store.Get("nonexistent")
	require.Error(t, err)
}

func TestFileStore_Get_RejectsPathTraversal(t *testing.T) {
	root := t.TempDir()
	store := NewFileStore(root)

	for _, id := range []string{"../etc/passwd", "..", "a/../../etc", "a/b", `a\b`, ""} {
		_, err := store.Get(id)
		assert.Error(t, err, "expected id %q to be rejected", id)
	}
}

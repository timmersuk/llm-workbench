package project

import (
	"os"
	"path/filepath"
	"strings"
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

func projectYAMLWithID(id string) string {
	return strings.Replace(validProjectYAML, "demo-project", id, 1)
}

func TestFileStore_List(t *testing.T) {
	root := t.TempDir()
	writeProjectFixture(t, root, "demo-project", validProjectYAML)
	writeProjectFixture(t, root, "auth-service", validProjectYAML)

	store := NewFileStore(root)
	result, err := store.List()
	require.NoError(t, err)
	require.Len(t, result.Projects, 2)
	assert.Empty(t, result.Errors)
}

func TestFileStore_List_Empty(t *testing.T) {
	root := t.TempDir()
	store := NewFileStore(root)
	result, err := store.List()
	require.NoError(t, err)
	assert.Empty(t, result.Projects)
	assert.Empty(t, result.Errors)
}

func TestFileStore_List_MalformedYAML(t *testing.T) {
	root := t.TempDir()
	writeProjectFixture(t, root, "demo-project", "id: [this is not valid yaml")

	store := NewFileStore(root)
	result, err := store.List()
	require.NoError(t, err)
	assert.Empty(t, result.Projects)
	require.Len(t, result.Errors, 1)
	assert.Equal(t, "demo-project", result.Errors[0].ID)
	assert.Contains(t, result.Errors[0].Error, "parsing")
}

func TestFileStore_List_SkipsMalformedButKeepsValidProjects(t *testing.T) {
	root := t.TempDir()
	writeProjectFixture(t, root, "auth-service", projectYAMLWithID("auth-service"))
	writeProjectFixture(t, root, "demo-project", "id: [this is not valid yaml")

	store := NewFileStore(root)
	result, err := store.List()
	require.NoError(t, err)

	require.Len(t, result.Projects, 1)
	assert.Equal(t, "auth-service", result.Projects[0].ID)

	require.Len(t, result.Errors, 1)
	assert.Equal(t, "demo-project", result.Errors[0].ID)
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

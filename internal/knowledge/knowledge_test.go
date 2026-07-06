package knowledge

import (
	"errors"
	"io/fs"
	"os"
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func writeConcept(t *testing.T, root, conceptID, body string) {
	t.Helper()
	path := filepath.Join(root, filepath.FromSlash(conceptID)+".md")
	require.NoError(t, os.MkdirAll(filepath.Dir(path), 0o755))
	require.NoError(t, os.WriteFile(path, []byte(body), 0o644))
}

func TestFileReader_Get_ParsesFrontmatterAndBody(t *testing.T) {
	root := t.TempDir()
	writeConcept(t, root, "coding-standards/logging", `---
type: Coding Standard
title: Logging
tags: [backend, observability]
---

Backend logging uses `+"`logrus`"+`.

# Citations

[1] [Engineering conventions](../engineering%20conventions.md#logging)
`)

	reader := NewFileReader(root)
	concept, err := reader.Get("coding-standards/logging")
	require.NoError(t, err)
	assert.Equal(t, "Coding Standard", concept.Type)
	assert.Equal(t, "Logging", concept.Frontmatter["title"])
	assert.Contains(t, concept.Body, "Backend logging uses `logrus`.")
	assert.Contains(t, concept.Body, "# Citations")
}

func TestFileReader_Get_ToleratesUnknownFrontmatterFields(t *testing.T) {
	root := t.TempDir()
	writeConcept(t, root, "note", "---\ntype: Domain Note\nfuture_field: something unexpected\n---\nBody text.\n")

	reader := NewFileReader(root)
	concept, err := reader.Get("note")
	require.NoError(t, err)
	assert.Equal(t, "Domain Note", concept.Type)
	assert.Equal(t, "something unexpected", concept.Frontmatter["future_field"])
}

func TestFileReader_Get_MissingTypeField(t *testing.T) {
	root := t.TempDir()
	writeConcept(t, root, "note", "---\ntitle: No type here\n---\nBody.\n")

	reader := NewFileReader(root)
	_, err := reader.Get("note")
	require.Error(t, err)
	assert.ErrorIs(t, err, ErrMissingType)
}

func TestFileReader_Get_NoFrontmatterBlockAtAll(t *testing.T) {
	root := t.TempDir()
	writeConcept(t, root, "note", "Just plain markdown, no frontmatter.\n")

	reader := NewFileReader(root)
	_, err := reader.Get("note")
	require.Error(t, err)
	assert.ErrorIs(t, err, ErrMissingType)
}

func TestFileReader_Get_EmptyBodyAfterFrontmatter(t *testing.T) {
	root := t.TempDir()
	writeConcept(t, root, "note", "---\ntype: Reference\n---\n")

	reader := NewFileReader(root)
	concept, err := reader.Get("note")
	require.NoError(t, err)
	assert.Equal(t, "Reference", concept.Type)
	assert.Equal(t, "", concept.Body)
}

func TestFileReader_Get_NestedConceptID(t *testing.T) {
	root := t.TempDir()
	writeConcept(t, root, "a/b/c", "---\ntype: Domain Note\n---\ndeeply nested\n")

	reader := NewFileReader(root)
	concept, err := reader.Get("a/b/c")
	require.NoError(t, err)
	assert.Contains(t, concept.Body, "deeply nested")
}

func TestFileReader_Get_MissingFile(t *testing.T) {
	root := t.TempDir()
	reader := NewFileReader(root)

	_, err := reader.Get("does-not-exist")
	require.Error(t, err)
	assert.True(t, errors.Is(err, fs.ErrNotExist))
}

func TestFileReader_Get_RejectsPathTraversal(t *testing.T) {
	root := t.TempDir()
	reader := NewFileReader(root)

	for _, id := range []string{"", "..", "../escape", "a/../../etc/passwd", "/absolute", "a/./b", "a/../b"} {
		_, err := reader.Get(id)
		assert.Error(t, err, "expected concept id %q to be rejected", id)
		assert.ErrorIs(t, err, ErrInvalidConceptID)
	}
}

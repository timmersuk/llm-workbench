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

func TestFileStore_Get_ParsesFrontmatterAndBody(t *testing.T) {
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

	store := NewFileStore(root)
	concept, err := store.Get("coding-standards/logging")
	require.NoError(t, err)
	assert.Equal(t, "Coding Standard", concept.Type)
	assert.Equal(t, "Logging", concept.Frontmatter["title"])
	assert.Contains(t, concept.Body, "Backend logging uses `logrus`.")
	assert.Contains(t, concept.Body, "# Citations")
}

func TestFileStore_Get_ToleratesUnknownFrontmatterFields(t *testing.T) {
	root := t.TempDir()
	writeConcept(t, root, "note", "---\ntype: Domain Note\nfuture_field: something unexpected\n---\nBody text.\n")

	store := NewFileStore(root)
	concept, err := store.Get("note")
	require.NoError(t, err)
	assert.Equal(t, "Domain Note", concept.Type)
	assert.Equal(t, "something unexpected", concept.Frontmatter["future_field"])
}

func TestFileStore_Get_MissingTypeField(t *testing.T) {
	root := t.TempDir()
	writeConcept(t, root, "note", "---\ntitle: No type here\n---\nBody.\n")

	store := NewFileStore(root)
	_, err := store.Get("note")
	require.Error(t, err)
	assert.ErrorIs(t, err, ErrMissingType)
}

func TestFileStore_Get_NoFrontmatterBlockAtAll(t *testing.T) {
	root := t.TempDir()
	writeConcept(t, root, "note", "Just plain markdown, no frontmatter.\n")

	store := NewFileStore(root)
	_, err := store.Get("note")
	require.Error(t, err)
	assert.ErrorIs(t, err, ErrMissingType)
}

func TestFileStore_Get_EmptyBodyAfterFrontmatter(t *testing.T) {
	root := t.TempDir()
	writeConcept(t, root, "note", "---\ntype: Reference\n---\n")

	store := NewFileStore(root)
	concept, err := store.Get("note")
	require.NoError(t, err)
	assert.Equal(t, "Reference", concept.Type)
	assert.Equal(t, "", concept.Body)
}

func TestFileStore_Get_NestedConceptID(t *testing.T) {
	root := t.TempDir()
	writeConcept(t, root, "a/b/c", "---\ntype: Domain Note\n---\ndeeply nested\n")

	store := NewFileStore(root)
	concept, err := store.Get("a/b/c")
	require.NoError(t, err)
	assert.Contains(t, concept.Body, "deeply nested")
}

func TestFileStore_Get_MissingFile(t *testing.T) {
	root := t.TempDir()
	store := NewFileStore(root)

	_, err := store.Get("does-not-exist")
	require.Error(t, err)
	assert.True(t, errors.Is(err, fs.ErrNotExist))
}

func TestFileStore_Get_RejectsPathTraversal(t *testing.T) {
	root := t.TempDir()
	store := NewFileStore(root)

	for _, id := range []string{"", "..", "../escape", "a/../../etc/passwd", "/absolute", "a/./b", "a/../b"} {
		_, err := store.Get(id)
		assert.Error(t, err, "expected concept id %q to be rejected", id)
		assert.ErrorIs(t, err, ErrInvalidConceptID)
	}
}

func TestFileStore_List_ReturnsSummaries(t *testing.T) {
	root := t.TempDir()
	writeConcept(t, root, "coding-standards/logging", "---\ntype: Coding Standard\ntitle: Logging\ndescription: How we log.\ntags: [backend, observability]\n---\nBody.\n")
	writeConcept(t, root, "a/b/c", "---\ntype: Domain Note\n---\ndeeply nested\n")

	store := NewFileStore(root)
	summaries, err := store.List()
	require.NoError(t, err)
	require.Len(t, summaries, 2)

	byID := make(map[string]ConceptSummary, len(summaries))
	for _, s := range summaries {
		byID[s.ConceptID] = s
	}

	logging, ok := byID["coding-standards/logging"]
	require.True(t, ok)
	assert.Equal(t, "Coding Standard", logging.Type)
	assert.Equal(t, "Logging", logging.Title)
	assert.Equal(t, "How we log.", logging.Description)
	assert.Equal(t, []string{"backend", "observability"}, logging.Tags)

	nested, ok := byID["a/b/c"]
	require.True(t, ok)
	assert.Equal(t, "Domain Note", nested.Type)
	assert.Equal(t, "", nested.Title)
}

func TestFileStore_List_SkipsReservedIndexAndLogFiles(t *testing.T) {
	root := t.TempDir()
	writeConcept(t, root, "index", "not a concept\n")
	writeConcept(t, root, "log", "not a concept\n")
	writeConcept(t, root, "real-concept", "---\ntype: Reference\n---\nBody.\n")

	store := NewFileStore(root)
	summaries, err := store.List()
	require.NoError(t, err)
	require.Len(t, summaries, 1)
	assert.Equal(t, "real-concept", summaries[0].ConceptID)
}

func TestFileStore_List_SkipsUnparseableConceptsRatherThanFailing(t *testing.T) {
	root := t.TempDir()
	writeConcept(t, root, "broken", "no frontmatter here\n")
	writeConcept(t, root, "good", "---\ntype: Reference\n---\nBody.\n")

	store := NewFileStore(root)
	summaries, err := store.List()
	require.NoError(t, err)
	require.Len(t, summaries, 1)
	assert.Equal(t, "good", summaries[0].ConceptID)
}

func TestFileStore_List_EmptyBundleReturnsNoError(t *testing.T) {
	store := NewFileStore(filepath.Join(t.TempDir(), "does-not-exist-yet"))
	summaries, err := store.List()
	require.NoError(t, err)
	assert.Empty(t, summaries)
}

func TestFileStore_Put_WritesRoundTrippableConcept(t *testing.T) {
	root := t.TempDir()
	store := NewFileStore(root)

	c := Concept{
		Type:        "Coding Standard",
		Frontmatter: map[string]any{"title": "Logging", "tags": []any{"backend"}},
		Body:        "Use structured logging.\n",
	}
	require.NoError(t, store.Put("coding-standards/logging", c))

	got, err := store.Get("coding-standards/logging")
	require.NoError(t, err)
	assert.Equal(t, "Coding Standard", got.Type)
	assert.Equal(t, "Logging", got.Frontmatter["title"])
	assert.Equal(t, "Use structured logging.\n", got.Body)
}

func TestFileStore_Put_CreatesNestedDirectories(t *testing.T) {
	root := t.TempDir()
	store := NewFileStore(root)

	require.NoError(t, store.Put("a/b/c", Concept{Type: "Domain Note", Body: "nested\n"}))

	got, err := store.Get("a/b/c")
	require.NoError(t, err)
	assert.Equal(t, "nested\n", got.Body)
}

func TestFileStore_Put_OverwritesExistingConcept(t *testing.T) {
	root := t.TempDir()
	store := NewFileStore(root)

	require.NoError(t, store.Put("note", Concept{Type: "Reference", Body: "v1\n"}))
	require.NoError(t, store.Put("note", Concept{Type: "Reference", Body: "v2\n"}))

	got, err := store.Get("note")
	require.NoError(t, err)
	assert.Equal(t, "v2\n", got.Body)
}

func TestFileStore_Put_TypeFieldIsAuthoritativeOverFrontmatter(t *testing.T) {
	root := t.TempDir()
	store := NewFileStore(root)

	c := Concept{Type: "Coding Standard", Frontmatter: map[string]any{"type": "stale value"}, Body: "Body.\n"}
	require.NoError(t, store.Put("note", c))

	got, err := store.Get("note")
	require.NoError(t, err)
	assert.Equal(t, "Coding Standard", got.Type)
	assert.Equal(t, "Coding Standard", got.Frontmatter["type"])
}

func TestFileStore_Put_RejectsMissingType(t *testing.T) {
	store := NewFileStore(t.TempDir())
	err := store.Put("note", Concept{Body: "Body.\n"})
	require.Error(t, err)
	assert.ErrorIs(t, err, ErrMissingType)
}

func TestFileStore_Put_RejectsPathTraversal(t *testing.T) {
	store := NewFileStore(t.TempDir())
	err := store.Put("../escape", Concept{Type: "Reference", Body: "x\n"})
	require.Error(t, err)
	assert.ErrorIs(t, err, ErrInvalidConceptID)
}

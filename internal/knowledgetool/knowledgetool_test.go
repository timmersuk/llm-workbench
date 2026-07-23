package knowledgetool

import (
	"errors"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/timmersuk/llm-workbench/internal/knowledge"
)

type fakeStore struct {
	summaries []knowledge.ConceptSummary
	listErr   error
	concepts  map[string]knowledge.Concept
	getErr    error
}

func (f *fakeStore) List() ([]knowledge.ConceptSummary, error) {
	if f.listErr != nil {
		return nil, f.listErr
	}
	return f.summaries, nil
}

func (f *fakeStore) Get(conceptID string) (knowledge.Concept, error) {
	if f.getErr != nil {
		return knowledge.Concept{}, f.getErr
	}
	c, ok := f.concepts[conceptID]
	if !ok {
		return knowledge.Concept{}, errors.New("not found")
	}
	return c, nil
}

func TestExecuteList_FormatsSortedSummaries(t *testing.T) {
	store := &fakeStore{summaries: []knowledge.ConceptSummary{
		{ConceptID: "b/second", Type: "Domain Note"},
		{ConceptID: "a/first", Type: "Coding Standard", Title: "Logging", Description: "How we log.", Tags: []string{"backend", "observability"}},
	}}

	out, err := ExecuteList(store)
	require.NoError(t, err)

	firstIdx := indexOf(t, out, "a/first")
	secondIdx := indexOf(t, out, "b/second")
	assert.Less(t, firstIdx, secondIdx, "results should be sorted by concept id")
	assert.Contains(t, out, "a/first [Coding Standard] — Logging: How we log. (tags: backend, observability)")
	assert.Contains(t, out, "b/second [Domain Note]")
}

func TestExecuteList_EmptyBundle(t *testing.T) {
	out, err := ExecuteList(&fakeStore{})
	require.NoError(t, err)
	assert.Equal(t, "no knowledge concepts exist yet", out)
}

func TestExecuteList_PropagatesStoreError(t *testing.T) {
	_, err := ExecuteList(&fakeStore{listErr: errors.New("disk on fire")})
	require.Error(t, err)
	assert.Contains(t, err.Error(), "disk on fire")
}

func TestExecuteGet_FormatsConceptWithFrontmatterAndBody(t *testing.T) {
	store := &fakeStore{concepts: map[string]knowledge.Concept{
		"coding-standards/logging": {
			Type:        "Coding Standard",
			Frontmatter: map[string]any{"type": "Coding Standard", "title": "Logging"},
			Body:        "Use structured logging.\n",
		},
	}}

	out, err := ExecuteGet(store, "coding-standards/logging")
	require.NoError(t, err)
	assert.Contains(t, out, "concept_id: coding-standards/logging")
	assert.Contains(t, out, "type: Coding Standard")
	assert.Contains(t, out, "title: Logging")
	assert.Contains(t, out, "Use structured logging.")
	// The "type" frontmatter key is not duplicated below the header line.
	assert.Equal(t, 1, countOccurrences(out, "type: Coding Standard"))
}

func TestExecuteGet_RequiresConceptID(t *testing.T) {
	_, err := ExecuteGet(&fakeStore{}, "")
	require.Error(t, err)
	assert.Contains(t, err.Error(), "concept_id")
}

func TestExecuteGet_PropagatesStoreError(t *testing.T) {
	_, err := ExecuteGet(&fakeStore{getErr: errors.New("not found")}, "missing")
	require.Error(t, err)
	assert.Contains(t, err.Error(), "missing")
}

func indexOf(t *testing.T, s, substr string) int {
	t.Helper()
	for i := 0; i+len(substr) <= len(s); i++ {
		if s[i:i+len(substr)] == substr {
			return i
		}
	}
	t.Fatalf("expected %q to contain %q", s, substr)
	return -1
}

func countOccurrences(s, substr string) int {
	count := 0
	for i := 0; i+len(substr) <= len(s); i++ {
		if s[i:i+len(substr)] == substr {
			count++
		}
	}
	return count
}

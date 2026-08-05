package task

import (
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestFileStore_AppendKnowledgeActivity_AppendsAndPersists(t *testing.T) {
	root := t.TempDir()
	store := NewFileStore(root)
	_, err := store.Create("demo-project", Task{ID: "task-a", Title: "A"})
	require.NoError(t, err)

	updated, err := store.AppendKnowledgeActivity("demo-project", "task-a", KnowledgeActivityEntry{
		ConceptID: "coding-standards/logging", Type: "Coding Standard", Action: KnowledgeActivityCreated,
	})
	require.NoError(t, err)
	require.Len(t, updated.KnowledgeActivity, 1)
	assert.Equal(t, "coding-standards/logging", updated.KnowledgeActivity[0].ConceptID)
	assert.Equal(t, KnowledgeActivityCreated, updated.KnowledgeActivity[0].Action)
	assert.False(t, updated.KnowledgeActivity[0].CreatedAt.IsZero(), "CreatedAt is server-stamped, not left at its zero value")

	fetched, err := store.Get("demo-project", "task-a")
	require.NoError(t, err)
	require.Len(t, fetched.KnowledgeActivity, 1, "must be persisted, not just returned in-memory")
	assert.Equal(t, "coding-standards/logging", fetched.KnowledgeActivity[0].ConceptID)
}

func TestFileStore_AppendKnowledgeActivity_AppendsWithoutOverwritingPriorEntries(t *testing.T) {
	root := t.TempDir()
	store := NewFileStore(root)
	_, err := store.Create("demo-project", Task{ID: "task-a", Title: "A"})
	require.NoError(t, err)

	_, err = store.AppendKnowledgeActivity("demo-project", "task-a", KnowledgeActivityEntry{
		ConceptID: "a", Action: KnowledgeActivityCreated,
	})
	require.NoError(t, err)
	updated, err := store.AppendKnowledgeActivity("demo-project", "task-a", KnowledgeActivityEntry{
		ConceptID: "b", Action: KnowledgeActivityRejected,
	})
	require.NoError(t, err)

	require.Len(t, updated.KnowledgeActivity, 2)
	assert.Equal(t, "a", updated.KnowledgeActivity[0].ConceptID)
	assert.Equal(t, "b", updated.KnowledgeActivity[1].ConceptID)
}

func TestFileStore_AppendKnowledgeActivity_NeverTouchesStage(t *testing.T) {
	root := t.TempDir()
	store := NewFileStore(root)
	_, err := store.Create("demo-project", Task{ID: "task-a", Title: "A"})
	require.NoError(t, err)

	updated, err := store.AppendKnowledgeActivity("demo-project", "task-a", KnowledgeActivityEntry{
		ConceptID: "x", Action: KnowledgeActivityCreated,
	})
	require.NoError(t, err)
	assert.Equal(t, StageRequirements, updated.Stage, "recording knowledge activity must never move Stage")
}

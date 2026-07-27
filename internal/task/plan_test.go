package task

import (
	"errors"
	"io/fs"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestFileStore_GetPlan_NotFinalized(t *testing.T) {
	root := t.TempDir()
	store := NewFileStore(root)
	_, err := store.Create("demo-project", Task{ID: "task-a", Title: "A"})
	require.NoError(t, err)

	_, err = store.GetPlan("demo-project", "task-a")
	require.Error(t, err)
	assert.True(t, errors.Is(err, fs.ErrNotExist))
}

func TestFileStore_GetPlan_RoundTrip(t *testing.T) {
	root := t.TempDir()
	store := NewFileStore(root)
	_, err := store.Create("demo-project", Task{ID: "task-a", Title: "A"})
	require.NoError(t, err)

	want := Plan{
		Approach:            "do it incrementally",
		Steps:               []string{"step 1", "step 2"},
		Risks:               []string{"risk 1"},
		EstimatedComplexity: "medium",
		RecommendedExecutor: "claude-code",
	}
	require.NoError(t, store.writePlan("demo-project", "task-a", want))

	got, err := store.GetPlan("demo-project", "task-a")
	require.NoError(t, err)
	assert.Equal(t, want, got)
}

func TestFileStore_GetPlan_RejectsPathTraversal(t *testing.T) {
	root := t.TempDir()
	store := NewFileStore(root)

	_, err := store.GetPlan("demo-project", "../escape")
	require.Error(t, err)
	assert.ErrorIs(t, err, ErrInvalidID)
}

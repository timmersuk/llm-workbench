package task

import (
	"errors"
	"io/fs"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestFileStore_GetContext_NotFinalized(t *testing.T) {
	root := t.TempDir()
	store := NewFileStore(root)
	_, err := store.Create(Task{ID: "task-a", Title: "A"})
	require.NoError(t, err)

	_, err = store.GetContext("task-a")
	require.Error(t, err)
	assert.True(t, errors.Is(err, fs.ErrNotExist))
}

func TestFileStore_GetContext_RoundTrip(t *testing.T) {
	root := t.TempDir()
	store := NewFileStore(root)
	_, err := store.Create(Task{ID: "task-a", Title: "A"})
	require.NoError(t, err)

	want := Context{
		Summary:       "does the thing",
		Background:    "some background",
		Files:         []string{"a.go"},
		Detail:        "detail text",
		Verification:  []VerificationStep{{Description: "run tests", Kind: VerificationKindAgentExecutable}},
		OpenQuestions: []string{"what about x?"},
	}
	require.NoError(t, store.writeContext("task-a", want))

	got, err := store.GetContext("task-a")
	require.NoError(t, err)
	assert.Equal(t, want, got)
}

func TestFileStore_GetContext_RejectsPathTraversal(t *testing.T) {
	root := t.TempDir()
	store := NewFileStore(root)

	_, err := store.GetContext("../escape")
	require.Error(t, err)
	assert.ErrorIs(t, err, ErrInvalidID)
}

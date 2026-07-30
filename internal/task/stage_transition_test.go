package task

import (
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestFileStore_ListStageTransitions_EmptyWhenNoFileYet(t *testing.T) {
	root := t.TempDir()
	store := NewFileStore(root)
	_, err := store.Create("demo-project", Task{ID: "task-a", Title: "A"})
	require.NoError(t, err)

	transitions, err := store.ListStageTransitions("demo-project", "task-a")
	require.NoError(t, err)
	assert.Empty(t, transitions)
}

func TestFileStore_AppendStageTransition_AccumulatesInOrderAndPersists(t *testing.T) {
	root := t.TempDir()
	store := NewFileStore(root)
	_, err := store.Create("demo-project", Task{ID: "task-a", Title: "A"})
	require.NoError(t, err)

	require.NoError(t, store.AppendStageTransition("demo-project", "task-a", StageTransition{
		FromStage: StageRequirements,
		ToStage:   StagePlanning,
		Trigger:   TransitionTriggerFinalizeRequirements,
	}))
	require.NoError(t, store.AppendStageTransition("demo-project", "task-a", StageTransition{
		FromStage: StageReview,
		ToStage:   StagePlanning,
		Trigger:   TransitionTriggerReviseToPlanning,
		Reason:    "I think I wanted icons for the different modes, not words",
	}))

	transitions, err := store.ListStageTransitions("demo-project", "task-a")
	require.NoError(t, err)
	require.Len(t, transitions, 2)

	assert.Equal(t, "task-a", transitions[0].TaskID)
	assert.Equal(t, StageRequirements, transitions[0].FromStage)
	assert.Equal(t, StagePlanning, transitions[0].ToStage)
	assert.Equal(t, TransitionTriggerFinalizeRequirements, transitions[0].Trigger)
	assert.False(t, transitions[0].CreatedAt.IsZero())

	assert.Equal(t, TransitionTriggerReviseToPlanning, transitions[1].Trigger)
	assert.Equal(t, "I think I wanted icons for the different modes, not words", transitions[1].Reason)

	// Re-fetching from disk reflects everything appended so far, in append
	// order — never rewritten, so a crash mid-append could only ever tear
	// the newest entry.
	reloaded, err := store.ListStageTransitions("demo-project", "task-a")
	require.NoError(t, err)
	require.Len(t, reloaded, 2)
	assert.Equal(t, transitions[0].Trigger, reloaded[0].Trigger)
	assert.Equal(t, transitions[1].Trigger, reloaded[1].Trigger)
}

func TestFileStore_AppendStageTransition_RejectsPathTraversal(t *testing.T) {
	root := t.TempDir()
	store := NewFileStore(root)

	err := store.AppendStageTransition("demo-project", "../escape", StageTransition{})
	require.Error(t, err)
	assert.ErrorIs(t, err, ErrInvalidID)
}

func TestFileStore_ListStageTransitions_RejectsPathTraversal(t *testing.T) {
	root := t.TempDir()
	store := NewFileStore(root)

	_, err := store.ListStageTransitions("demo-project", "../escape")
	require.Error(t, err)
	assert.ErrorIs(t, err, ErrInvalidID)
}

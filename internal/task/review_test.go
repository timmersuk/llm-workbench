package task

import (
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// newReviewTask drives a task all the way to StageReview (the stage
// FinalizeReview and the review store operate from) by finalizing
// requirements + plan and recording a successful execution.
func newReviewTask(t *testing.T, store *FileStore, id string) Task {
	t.Helper()
	newImplementationTask(t, store, id)
	_, err := store.RecordExecution(id, Execution{
		ExecutionID: "exec-001",
		Status:      ExecutionStatusSuccess,
		Output:      ExecutionOutput{GitBranch: "task-exec/" + id + "/exec-001", Commits: []string{"abc123"}},
	})
	require.NoError(t, err)
	tk, err := store.Get(id)
	require.NoError(t, err)
	require.Equal(t, StageReview, tk.Stage)
	return tk
}

func TestFileStore_RecordReview_RoundTripsAndListsSorted(t *testing.T) {
	root := t.TempDir()
	store := NewFileStore(root)
	newReviewTask(t, store, "task-a")

	_, err := store.RecordReview("task-a", Review{ReviewID: "review-001", Decision: ReviewDecisionNeedsChanges, Notes: "add a test"})
	require.NoError(t, err)
	_, err = store.RecordReview("task-a", Review{ReviewID: "review-002", Decision: ReviewDecisionApproved, Notes: "looks good"})
	require.NoError(t, err)

	reviews, err := store.ListReviews("task-a")
	require.NoError(t, err)
	require.Len(t, reviews, 2)

	assert.Equal(t, "review-001", reviews[0].ReviewID)
	assert.Equal(t, "task-a", reviews[0].TaskID, "TaskID is server-set")
	assert.Equal(t, ReviewDecisionNeedsChanges, reviews[0].Decision)
	assert.Equal(t, "add a test", reviews[0].Notes)
	assert.False(t, reviews[0].CreatedAt.IsZero(), "CreatedAt is server-set")
	assert.Equal(t, "review-002", reviews[1].ReviewID)
}

func TestFileStore_ListReviews_SortedNumericallyPastThreeDigits(t *testing.T) {
	root := t.TempDir()
	store := NewFileStore(root)
	newReviewTask(t, store, "task-a")

	_, err := store.RecordReview("task-a", Review{ReviewID: "review-1000", Decision: ReviewDecisionNeedsChanges, Notes: "later cycle"})
	require.NoError(t, err)
	_, err = store.RecordReview("task-a", Review{ReviewID: "review-999", Decision: ReviewDecisionApproved, Notes: "earlier cycle"})
	require.NoError(t, err)

	reviews, err := store.ListReviews("task-a")
	require.NoError(t, err)
	require.Len(t, reviews, 2)
	// Lexical string comparison ("review-1000" < "review-999", since '1' <
	// '9') would put review-1000 first — wrong, since it's chronologically
	// the later verdict. Numeric ordering must put review-999 first.
	assert.Equal(t, "review-999", reviews[0].ReviewID)
	assert.Equal(t, "review-1000", reviews[1].ReviewID)
}

func TestFileStore_NextReviewID_StartsAtReview001AndIncrements(t *testing.T) {
	root := t.TempDir()
	store := NewFileStore(root)
	newReviewTask(t, store, "task-a")

	id, err := store.NextReviewID("task-a")
	require.NoError(t, err)
	assert.Equal(t, "review-001", id)

	_, err = store.RecordReview("task-a", Review{ReviewID: "review-001", Decision: ReviewDecisionNeedsChanges})
	require.NoError(t, err)

	id, err = store.NextReviewID("task-a")
	require.NoError(t, err)
	assert.Equal(t, "review-002", id)
}

func TestFileStore_ListReviews_EmptyWhenNoneRecorded(t *testing.T) {
	root := t.TempDir()
	store := NewFileStore(root)
	newReviewTask(t, store, "task-a")

	reviews, err := store.ListReviews("task-a")
	require.NoError(t, err)
	assert.Empty(t, reviews)
}

func TestFileStore_FinalizeReview_ApprovedAdvancesToComplete(t *testing.T) {
	root := t.TempDir()
	store := NewFileStore(root)
	newReviewTask(t, store, "task-a")

	tk, err := store.FinalizeReview("task-a", ReviewDraft{Decision: ReviewDecisionApproved, Notes: "ship it"})
	require.NoError(t, err)
	assert.Equal(t, StageComplete, tk.Stage)

	// The verdict is recorded append-only as review-001.
	reviews, err := store.ListReviews("task-a")
	require.NoError(t, err)
	require.Len(t, reviews, 1)
	assert.Equal(t, "review-001", reviews[0].ReviewID)
	assert.Equal(t, "exec-001", reviews[0].ExecutionID, "the execution this verdict is about is captured at Finalize time")
	assert.Equal(t, ReviewDecisionApproved, reviews[0].Decision)
	assert.Equal(t, "ship it", reviews[0].Notes)
}

func TestFileStore_FinalizeReview_NeedsChangesReturnsToImplementation(t *testing.T) {
	root := t.TempDir()
	store := NewFileStore(root)
	newReviewTask(t, store, "task-a")

	tk, err := store.FinalizeReview("task-a", ReviewDraft{Decision: ReviewDecisionNeedsChanges, Notes: "handle the empty case"})
	require.NoError(t, err)
	assert.Equal(t, StageImplementation, tk.Stage)

	reviews, err := store.ListReviews("task-a")
	require.NoError(t, err)
	require.Len(t, reviews, 1)
	assert.Equal(t, "exec-001", reviews[0].ExecutionID, "the execution this verdict is about is captured at Finalize time")
	assert.Equal(t, ReviewDecisionNeedsChanges, reviews[0].Decision)
	assert.Equal(t, "handle the empty case", reviews[0].Notes, "notes preserved for the execute-retrigger path")
}

func TestFileStore_FinalizeReview_RejectedReturnsToRequirements(t *testing.T) {
	root := t.TempDir()
	store := NewFileStore(root)
	newReviewTask(t, store, "task-a")

	tk, err := store.FinalizeReview("task-a", ReviewDraft{Decision: ReviewDecisionRejected, Notes: "requirements were wrong"})
	require.NoError(t, err)
	assert.Equal(t, StageRequirements, tk.Stage)

	reviews, err := store.ListReviews("task-a")
	require.NoError(t, err)
	require.Len(t, reviews, 1)
	assert.Equal(t, ReviewDecisionRejected, reviews[0].Decision)
}

func TestFileStore_FinalizeReview_WrongStageErrorsAndRecordsNothing(t *testing.T) {
	root := t.TempDir()
	store := NewFileStore(root)
	newImplementationTask(t, store, "task-a") // at implementation, not review

	_, err := store.FinalizeReview("task-a", ReviewDraft{Decision: ReviewDecisionApproved})
	require.ErrorIs(t, err, ErrWrongStage)

	tk, err := store.Get("task-a")
	require.NoError(t, err)
	assert.Equal(t, StageImplementation, tk.Stage, "stage unchanged")
	reviews, err := store.ListReviews("task-a")
	require.NoError(t, err)
	assert.Empty(t, reviews, "no verdict recorded on a wrong-stage finalize")
}

func TestFileStore_FinalizeReview_UnknownDecisionErrorsAndRecordsNothing(t *testing.T) {
	root := t.TempDir()
	store := NewFileStore(root)
	newReviewTask(t, store, "task-a")

	_, err := store.FinalizeReview("task-a", ReviewDraft{Decision: "maybe"})
	require.Error(t, err)

	tk, err := store.Get("task-a")
	require.NoError(t, err)
	assert.Equal(t, StageReview, tk.Stage, "stage unchanged on an invalid decision")
	reviews, err := store.ListReviews("task-a")
	require.NoError(t, err)
	assert.Empty(t, reviews, "no verdict recorded on an invalid decision")
}

func TestFileStore_RecordReview_AppendOnlyRejectsDuplicateID(t *testing.T) {
	root := t.TempDir()
	store := NewFileStore(root)
	newReviewTask(t, store, "task-a")

	_, err := store.RecordReview("task-a", Review{ReviewID: "review-001", Decision: ReviewDecisionApproved})
	require.NoError(t, err)

	_, err = store.RecordReview("task-a", Review{ReviewID: "review-001", Decision: ReviewDecisionApproved})
	require.ErrorIs(t, err, ErrReviewAlreadyExists)
}

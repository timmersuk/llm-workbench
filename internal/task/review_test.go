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
	require.NoError(t, store.CreateExecutionLog("demo-project", id, "exec-001"))
	_, err := store.RecordExecution("demo-project", id, Execution{
		ExecutionID: "exec-001",
		Status:      ExecutionStatusSuccess,
		Output:      ExecutionOutput{GitBranch: "task-exec/" + id + "/exec-001", Commits: []string{"abc123"}},
	})
	require.NoError(t, err)
	tk, err := store.Get("demo-project", id)
	require.NoError(t, err)
	require.Equal(t, StageReview, tk.Stage)
	return tk
}

// newPRReviewTask drives a task to StagePRReview via direct mutation rather
// than a real FinalizeReview(approved) call — equivalent since Milestone 7
// PR 2, but avoids coupling these fixtures to FinalizeReview's own behavior
// (already covered by TestFileStore_FinalizeReview_ApprovedAdvancesToPRReview).
func newPRReviewTask(t *testing.T, store *FileStore, id string) Task {
	t.Helper()
	newReviewTask(t, store, id)
	tk, err := store.Get("demo-project", id)
	require.NoError(t, err)
	tk.Stage = StagePRReview
	require.NoError(t, store.writeTask("demo-project", tk))
	tk, err = store.Get("demo-project", id)
	require.NoError(t, err)
	require.Equal(t, StagePRReview, tk.Stage)
	return tk
}

func TestFileStore_RecordReview_RoundTripsAndListsSorted(t *testing.T) {
	root := t.TempDir()
	store := NewFileStore(root)
	newReviewTask(t, store, "task-a")

	_, err := store.RecordReview("demo-project", "task-a", Review{ReviewID: "review-001", Decision: ReviewDecisionNeedsChanges, Notes: "add a test"})
	require.NoError(t, err)
	_, err = store.RecordReview("demo-project", "task-a", Review{ReviewID: "review-002", Decision: ReviewDecisionApproved, Notes: "looks good"})
	require.NoError(t, err)

	reviews, err := store.ListReviews("demo-project", "task-a")
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

	_, err := store.RecordReview("demo-project", "task-a", Review{ReviewID: "review-1000", Decision: ReviewDecisionNeedsChanges, Notes: "later cycle"})
	require.NoError(t, err)
	_, err = store.RecordReview("demo-project", "task-a", Review{ReviewID: "review-999", Decision: ReviewDecisionApproved, Notes: "earlier cycle"})
	require.NoError(t, err)

	reviews, err := store.ListReviews("demo-project", "task-a")
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

	id, err := store.NextReviewID("demo-project", "task-a")
	require.NoError(t, err)
	assert.Equal(t, "review-001", id)

	_, err = store.RecordReview("demo-project", "task-a", Review{ReviewID: "review-001", Decision: ReviewDecisionNeedsChanges})
	require.NoError(t, err)

	id, err = store.NextReviewID("demo-project", "task-a")
	require.NoError(t, err)
	assert.Equal(t, "review-002", id)
}

func TestFileStore_ListReviews_EmptyWhenNoneRecorded(t *testing.T) {
	root := t.TempDir()
	store := NewFileStore(root)
	newReviewTask(t, store, "task-a")

	reviews, err := store.ListReviews("demo-project", "task-a")
	require.NoError(t, err)
	assert.Empty(t, reviews)
}

func TestFileStore_FinalizeReview_ApprovedAdvancesToPRReview(t *testing.T) {
	root := t.TempDir()
	store := NewFileStore(root)
	newReviewTask(t, store, "task-a")

	tk, err := store.FinalizeReview("demo-project", "task-a", ReviewDraft{Decision: ReviewDecisionApproved, Notes: "ship it"})
	require.NoError(t, err)
	assert.Equal(t, StagePRReview, tk.Stage)

	// The verdict is recorded append-only as review-001.
	reviews, err := store.ListReviews("demo-project", "task-a")
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

	tk, err := store.FinalizeReview("demo-project", "task-a", ReviewDraft{Decision: ReviewDecisionNeedsChanges, Notes: "handle the empty case"})
	require.NoError(t, err)
	assert.Equal(t, StageImplementation, tk.Stage)

	reviews, err := store.ListReviews("demo-project", "task-a")
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

	tk, err := store.FinalizeReview("demo-project", "task-a", ReviewDraft{Decision: ReviewDecisionRejected, Notes: "requirements were wrong"})
	require.NoError(t, err)
	assert.Equal(t, StageRequirements, tk.Stage)

	reviews, err := store.ListReviews("demo-project", "task-a")
	require.NoError(t, err)
	require.Len(t, reviews, 1)
	assert.Equal(t, ReviewDecisionRejected, reviews[0].Decision)
}

func TestFileStore_FinalizeReview_WrongStageErrorsAndRecordsNothing(t *testing.T) {
	root := t.TempDir()
	store := NewFileStore(root)
	newImplementationTask(t, store, "task-a") // at implementation, not review

	_, err := store.FinalizeReview("demo-project", "task-a", ReviewDraft{Decision: ReviewDecisionApproved})
	require.ErrorIs(t, err, ErrWrongStage)

	tk, err := store.Get("demo-project", "task-a")
	require.NoError(t, err)
	assert.Equal(t, StageImplementation, tk.Stage, "stage unchanged")
	reviews, err := store.ListReviews("demo-project", "task-a")
	require.NoError(t, err)
	assert.Empty(t, reviews, "no verdict recorded on a wrong-stage finalize")
}

func TestFileStore_FinalizeReview_UnknownDecisionErrorsAndRecordsNothing(t *testing.T) {
	root := t.TempDir()
	store := NewFileStore(root)
	newReviewTask(t, store, "task-a")

	_, err := store.FinalizeReview("demo-project", "task-a", ReviewDraft{Decision: "maybe"})
	require.Error(t, err)

	tk, err := store.Get("demo-project", "task-a")
	require.NoError(t, err)
	assert.Equal(t, StageReview, tk.Stage, "stage unchanged on an invalid decision")
	reviews, err := store.ListReviews("demo-project", "task-a")
	require.NoError(t, err)
	assert.Empty(t, reviews, "no verdict recorded on an invalid decision")
}

func TestFileStore_FinalizeReview_NeedsChangesFromPRReviewReturnsToImplementation(t *testing.T) {
	root := t.TempDir()
	store := NewFileStore(root)
	newPRReviewTask(t, store, "task-a")

	tk, err := store.FinalizeReview("demo-project", "task-a", ReviewDraft{Decision: ReviewDecisionNeedsChanges, Notes: "changes requested on the PR"})
	require.NoError(t, err)
	assert.Equal(t, StageImplementation, tk.Stage)

	reviews, err := store.ListReviews("demo-project", "task-a")
	require.NoError(t, err)
	require.Len(t, reviews, 1)
	assert.Equal(t, ReviewDecisionNeedsChanges, reviews[0].Decision)
}

func TestFileStore_FinalizeReview_RejectedFromPRReviewReturnsToRequirements(t *testing.T) {
	root := t.TempDir()
	store := NewFileStore(root)
	newPRReviewTask(t, store, "task-a")

	tk, err := store.FinalizeReview("demo-project", "task-a", ReviewDraft{Decision: ReviewDecisionRejected, Notes: "rejected on the PR"})
	require.NoError(t, err)
	assert.Equal(t, StageRequirements, tk.Stage)

	reviews, err := store.ListReviews("demo-project", "task-a")
	require.NoError(t, err)
	require.Len(t, reviews, 1)
	assert.Equal(t, ReviewDecisionRejected, reviews[0].Decision)
}

func TestFileStore_FinalizeReview_ApprovedFromPRReviewErrorsAndRecordsNothing(t *testing.T) {
	root := t.TempDir()
	store := NewFileStore(root)
	newPRReviewTask(t, store, "task-a")

	_, err := store.FinalizeReview("demo-project", "task-a", ReviewDraft{Decision: ReviewDecisionApproved, Notes: "should not be reachable"})
	require.ErrorIs(t, err, ErrWrongStage)

	tk, err := store.Get("demo-project", "task-a")
	require.NoError(t, err)
	assert.Equal(t, StagePRReview, tk.Stage, "stage unchanged")
	reviews, err := store.ListReviews("demo-project", "task-a")
	require.NoError(t, err)
	assert.Empty(t, reviews, "no verdict recorded when approved is sent from pr_review")
}

func TestFileStore_MarkPRMerged_AdvancesToCleanup(t *testing.T) {
	root := t.TempDir()
	store := NewFileStore(root)
	tk := newPRReviewTask(t, store, "task-a")
	tk.PullRequest = &PullRequest{URL: "https://github.com/org/repo/pull/1", Number: 1, Branch: "task-exec/task-a/exec-001"}
	require.NoError(t, store.writeTask("demo-project", tk))

	got, err := store.MarkPRMerged("demo-project", "task-a")
	require.NoError(t, err)
	assert.Equal(t, StageCleanup, got.Stage, "mark-merged now lands on cleanup, not merged directly")

	transitions, err := store.ListStageTransitions("demo-project", "task-a")
	require.NoError(t, err)
	last := transitions[len(transitions)-1]
	assert.Equal(t, StagePRReview, last.FromStage)
	assert.Equal(t, StageCleanup, last.ToStage)
	assert.Equal(t, TransitionTriggerMarkPRMerged, last.Trigger)
}

func TestFileStore_CompleteCleanup_AdvancesToMerged(t *testing.T) {
	root := t.TempDir()
	store := NewFileStore(root)
	tk := newPRReviewTask(t, store, "task-a")
	tk.PullRequest = &PullRequest{URL: "https://github.com/org/repo/pull/1", Number: 1, Branch: "task-exec/task-a/exec-001"}
	require.NoError(t, store.writeTask("demo-project", tk))
	_, err := store.MarkPRMerged("demo-project", "task-a")
	require.NoError(t, err)

	got, err := store.CompleteCleanup("demo-project", "task-a")
	require.NoError(t, err)
	assert.Equal(t, StageMerged, got.Stage)

	transitions, err := store.ListStageTransitions("demo-project", "task-a")
	require.NoError(t, err)
	last := transitions[len(transitions)-1]
	assert.Equal(t, StageCleanup, last.FromStage)
	assert.Equal(t, StageMerged, last.ToStage)
	assert.Equal(t, TransitionTriggerCleanupComplete, last.Trigger)
}

func TestFileStore_CompleteCleanup_WrongStageErrors(t *testing.T) {
	root := t.TempDir()
	store := NewFileStore(root)
	newPRReviewTask(t, store, "task-a") // still at pr_review, never marked merged

	_, err := store.CompleteCleanup("demo-project", "task-a")
	require.ErrorIs(t, err, ErrWrongStage)
}

func TestFileStore_SetCleanupStatus_PersistsWithoutChangingStage(t *testing.T) {
	root := t.TempDir()
	store := NewFileStore(root)
	tk := newPRReviewTask(t, store, "task-a")
	tk.PullRequest = &PullRequest{URL: "https://github.com/org/repo/pull/1", Number: 1, Branch: "task-exec/task-a/exec-001"}
	require.NoError(t, store.writeTask("demo-project", tk))
	_, err := store.MarkPRMerged("demo-project", "task-a")
	require.NoError(t, err)

	status := []CleanupWorktreeStatus{
		{ExecutionID: "exec-001", Outcome: CleanupOutcomeRemoved},
		{ExecutionID: "exec-002", Outcome: CleanupOutcomeSkipped, Reason: "worktree has uncommitted changes"},
	}
	got, err := store.SetCleanupStatus("demo-project", "task-a", status)
	require.NoError(t, err)
	assert.Equal(t, StageCleanup, got.Stage, "not a stage transition")
	assert.Equal(t, status, got.CleanupStatus)

	reloaded, err := store.Get("demo-project", "task-a")
	require.NoError(t, err)
	assert.Equal(t, status, reloaded.CleanupStatus)
}

func TestFileStore_MarkPRMerged_WrongStageErrors(t *testing.T) {
	root := t.TempDir()
	store := NewFileStore(root)
	newReviewTask(t, store, "task-a") // at review, not pr_review

	_, err := store.MarkPRMerged("demo-project", "task-a")
	require.ErrorIs(t, err, ErrWrongStage)
}

func TestFileStore_MarkPRMerged_RequiresPullRequestSet(t *testing.T) {
	root := t.TempDir()
	store := NewFileStore(root)
	newPRReviewTask(t, store, "task-a") // no PullRequest set

	_, err := store.MarkPRMerged("demo-project", "task-a")
	require.Error(t, err)
	// Wraps ErrWrongStage (Milestone 7 PR 3) like the stage guard above it —
	// the task isn't in a state this action can be taken from either way, so
	// both should map to the same 409 over HTTP rather than this one falling
	// through to a 500.
	require.ErrorIs(t, err, ErrWrongStage)

	tk, err := store.Get("demo-project", "task-a")
	require.NoError(t, err)
	assert.Equal(t, StagePRReview, tk.Stage, "stage unchanged")
}

func TestFileStore_RecordPullRequest_SetsFieldWithoutChangingStage(t *testing.T) {
	root := t.TempDir()
	store := NewFileStore(root)
	newPRReviewTask(t, store, "task-a")

	pr := PullRequest{URL: "https://github.com/org/repo/pull/1", Number: 1, Branch: "task-exec/task-a/exec-001"}
	tk, err := store.RecordPullRequest("demo-project", "task-a", pr)
	require.NoError(t, err)
	assert.Equal(t, StagePRReview, tk.Stage, "stage unchanged — this is not a stage transition")
	require.NotNil(t, tk.PullRequest)
	assert.Equal(t, pr, *tk.PullRequest)

	reloaded, err := store.Get("demo-project", "task-a")
	require.NoError(t, err)
	require.NotNil(t, reloaded.PullRequest)
	assert.Equal(t, pr, *reloaded.PullRequest)
}

func TestFileStore_RecordPullRequest_CalledAgainOverwritesPriorRecord(t *testing.T) {
	root := t.TempDir()
	store := NewFileStore(root)
	newPRReviewTask(t, store, "task-a")

	_, err := store.RecordPullRequest("demo-project", "task-a", PullRequest{URL: "https://github.com/org/repo/pull/1", Number: 1, Branch: "task-exec/task-a/exec-001"})
	require.NoError(t, err)

	second := PullRequest{URL: "https://github.com/org/repo/pull/2", Number: 2, Branch: "task-exec/task-a/exec-002"}
	tk, err := store.RecordPullRequest("demo-project", "task-a", second)
	require.NoError(t, err)
	require.NotNil(t, tk.PullRequest)
	assert.Equal(t, second, *tk.PullRequest, "the refspec-continuity path calls this again with the same PR reused or a fresh one after a close")
}

func TestFileStore_RecordPullRequest_WrongStageErrors(t *testing.T) {
	root := t.TempDir()
	store := NewFileStore(root)
	newReviewTask(t, store, "task-a") // at review, not pr_review

	_, err := store.RecordPullRequest("demo-project", "task-a", PullRequest{URL: "https://github.com/org/repo/pull/1", Number: 1, Branch: "b"})
	require.ErrorIs(t, err, ErrWrongStage)

	tk, err := store.Get("demo-project", "task-a")
	require.NoError(t, err)
	assert.Nil(t, tk.PullRequest, "no PR recorded on a wrong-stage call")
}

func TestFileStore_RecordReview_AppendOnlyRejectsDuplicateID(t *testing.T) {
	root := t.TempDir()
	store := NewFileStore(root)
	newReviewTask(t, store, "task-a")

	_, err := store.RecordReview("demo-project", "task-a", Review{ReviewID: "review-001", Decision: ReviewDecisionApproved})
	require.NoError(t, err)

	_, err = store.RecordReview("demo-project", "task-a", Review{ReviewID: "review-001", Decision: ReviewDecisionApproved})
	require.ErrorIs(t, err, ErrReviewAlreadyExists)
}

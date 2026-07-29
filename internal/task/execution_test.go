package task

import (
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func newImplementationTask(t *testing.T, store *FileStore, id string) Task {
	t.Helper()
	_, err := store.Create("demo-project", Task{ID: id, Title: "A"})
	require.NoError(t, err)
	_, err = store.FinalizeRequirements("demo-project", id, RequirementsDraft{Objective: "ship it"})
	require.NoError(t, err)
	_, err = store.FinalizePlan("demo-project", id, Plan{Approach: "do it"})
	require.NoError(t, err)
	tk, err := store.Get("demo-project", id)
	require.NoError(t, err)
	require.Equal(t, StageImplementation, tk.Stage)
	return tk
}

func TestFileStore_NextExecutionID_StartsAtExec001AndIncrements(t *testing.T) {
	root := t.TempDir()
	store := NewFileStore(root)
	newImplementationTask(t, store, "task-a")

	id, err := store.NextExecutionID("demo-project", "task-a")
	require.NoError(t, err)
	assert.Equal(t, "exec-001", id)

	require.NoError(t, store.CreateExecutionLog("demo-project", "task-a", "exec-001"))
	_, err = store.RecordExecution("demo-project", "task-a", Execution{ExecutionID: "exec-001", Status: ExecutionStatusFailure, Failure: &ExecutionFailure{Type: FailureTypeExecution}})
	require.NoError(t, err)

	id, err = store.NextExecutionID("demo-project", "task-a")
	require.NoError(t, err)
	assert.Equal(t, "exec-002", id)
}

// TestFileStore_NextExecutionID_SkipsPastAnOrphanedLogWithNoRecord is a
// regression test for a real deadlock: internal/api/execution.go's
// handleStartExecution calls CreateExecutionLog before it can possibly
// fail (deliberately, so an attempt is provably recorded as having begun),
// but several of its own later steps (resolving the execution workspace,
// fetching PR review comments) can still abort the request before
// RecordExecution ever runs — leaving a log file with no matching
// execution.yaml. Before this fix, NextExecutionID only looked at real
// records, so it kept proposing that same id forever, and
// CreateExecutionLog kept rejecting it with ErrExecutionLogAlreadyExists —
// unrecoverable without deleting the stray log file by hand.
func TestFileStore_NextExecutionID_SkipsPastAnOrphanedLogWithNoRecord(t *testing.T) {
	root := t.TempDir()
	store := NewFileStore(root)
	newImplementationTask(t, store, "task-a")

	// exec-001 completes normally.
	require.NoError(t, store.CreateExecutionLog("demo-project", "task-a", "exec-001"))
	_, err := store.RecordExecution("demo-project", "task-a", Execution{ExecutionID: "exec-001", Status: ExecutionStatusSuccess})
	require.NoError(t, err)

	// exec-002's log gets created, but the attempt aborts before
	// RecordExecution — simulating handleStartExecution's early-return
	// failure paths.
	id, err := store.NextExecutionID("demo-project", "task-a")
	require.NoError(t, err)
	require.Equal(t, "exec-002", id)
	require.NoError(t, store.CreateExecutionLog("demo-project", "task-a", id))
	// (no RecordExecution call for exec-002 — the simulated abort)

	// A retry must not be handed "exec-002" again — CreateExecutionLog
	// would reject it outright — and ListExecutions must still report only
	// the one real, completed execution, not a bogus second entry parsed
	// from the orphaned log file.
	id, err = store.NextExecutionID("demo-project", "task-a")
	require.NoError(t, err)
	assert.Equal(t, "exec-003", id)
	require.NoError(t, store.CreateExecutionLog("demo-project", "task-a", id))

	executions, err := store.ListExecutions("demo-project", "task-a")
	require.NoError(t, err)
	require.Len(t, executions, 1)
	assert.Equal(t, "exec-001", executions[0].ExecutionID)
}

func TestFileStore_RecordExecution_AppendOnlyRejectsDuplicateID(t *testing.T) {
	root := t.TempDir()
	store := NewFileStore(root)
	newImplementationTask(t, store, "task-a")
	require.NoError(t, store.CreateExecutionLog("demo-project", "task-a", "exec-001"))

	_, err := store.RecordExecution("demo-project", "task-a", Execution{ExecutionID: "exec-001", Status: ExecutionStatusFailure, Failure: &ExecutionFailure{Type: FailureTypeExecution}})
	require.NoError(t, err)

	_, err = store.RecordExecution("demo-project", "task-a", Execution{ExecutionID: "exec-001", Status: ExecutionStatusFailure, Failure: &ExecutionFailure{Type: FailureTypeExecution}})
	require.ErrorIs(t, err, ErrExecutionAlreadyExists)
}

func TestFileStore_RecordExecution_SuccessAdvancesStageToReview(t *testing.T) {
	root := t.TempDir()
	store := NewFileStore(root)
	newImplementationTask(t, store, "task-a")
	require.NoError(t, store.CreateExecutionLog("demo-project", "task-a", "exec-001"))

	_, err := store.RecordExecution("demo-project", "task-a", Execution{
		ExecutionID: "exec-001",
		Status:      ExecutionStatusSuccess,
		Output:      ExecutionOutput{GitBranch: "task-exec/task-a/exec-001", Commits: []string{"abc123"}},
	})
	require.NoError(t, err)

	tk, err := store.Get("demo-project", "task-a")
	require.NoError(t, err)
	assert.Equal(t, StageReview, tk.Stage)
}

func TestFileStore_RecordExecution_FailurePartialNeverAdvancesStage(t *testing.T) {
	root := t.TempDir()
	store := NewFileStore(root)
	newImplementationTask(t, store, "task-a")
	require.NoError(t, store.CreateExecutionLog("demo-project", "task-a", "exec-001"))

	_, err := store.RecordExecution("demo-project", "task-a", Execution{ExecutionID: "exec-001", Status: ExecutionStatusFailure, Failure: &ExecutionFailure{Type: FailureTypeExecution}})
	require.NoError(t, err)

	tk, err := store.Get("demo-project", "task-a")
	require.NoError(t, err)
	assert.Equal(t, StageImplementation, tk.Stage)
}

// TestFileStore_RecordExecution_SuccessWrongStageErrorsAndWritesNothing
// deliberately does not create an execution log — a wrong-stage call must
// report ErrWrongStage, not be masked by the (also true here)
// ErrExecutionLogMissing condition; see RecordExecution's doc comment on
// why the stage guard runs first.
func TestFileStore_RecordExecution_SuccessWrongStageErrorsAndWritesNothing(t *testing.T) {
	root := t.TempDir()
	store := NewFileStore(root)
	_, err := store.Create("demo-project", Task{ID: "task-a", Title: "A"}) // still at requirements stage

	require.NoError(t, err)

	_, err = store.RecordExecution("demo-project", "task-a", Execution{ExecutionID: "exec-001", Status: ExecutionStatusSuccess})
	require.ErrorIs(t, err, ErrWrongStage)

	executions, err := store.ListExecutions("demo-project", "task-a")
	require.NoError(t, err)
	assert.Empty(t, executions, "a rejected success attempt must not leave an orphaned execution record")
}

// TestFileStore_RecordExecution_MissingLogErrorsAndWritesNothing exercises
// the log-existence gate on its own: a task correctly at StageImplementation
// (so the stage guard passes) with no exec-NNN.log.yaml ever created for
// this execution_id must be rejected, for every status — success, failure,
// and partial alike, since the whole point is that a crash with no
// evidence at all must never silently pass as recorded.
func TestFileStore_RecordExecution_MissingLogErrorsAndWritesNothing(t *testing.T) {
	root := t.TempDir()
	store := NewFileStore(root)
	newImplementationTask(t, store, "task-a")

	_, err := store.RecordExecution("demo-project", "task-a", Execution{ExecutionID: "exec-001", Status: ExecutionStatusSuccess})
	require.ErrorIs(t, err, ErrExecutionLogMissing)

	_, err = store.RecordExecution("demo-project", "task-a", Execution{ExecutionID: "exec-001", Status: ExecutionStatusFailure, Failure: &ExecutionFailure{Type: FailureTypeExecution}})
	require.ErrorIs(t, err, ErrExecutionLogMissing)

	_, err = store.RecordExecution("demo-project", "task-a", Execution{ExecutionID: "exec-001", Status: ExecutionStatusPartial, Failure: &ExecutionFailure{Type: FailureTypeExecution}})
	require.ErrorIs(t, err, ErrExecutionLogMissing)

	executions, err := store.ListExecutions("demo-project", "task-a")
	require.NoError(t, err)
	assert.Empty(t, executions, "a rejected attempt must not leave an orphaned execution record")

	tk, err := store.Get("demo-project", "task-a")
	require.NoError(t, err)
	assert.Equal(t, StageImplementation, tk.Stage, "a rejected success attempt must not advance Stage either")
}

func TestFileStore_ListExecutions_EmptyWhenNoAttemptsRecorded(t *testing.T) {
	root := t.TempDir()
	store := NewFileStore(root)
	newImplementationTask(t, store, "task-a")

	executions, err := store.ListExecutions("demo-project", "task-a")
	require.NoError(t, err)
	assert.Empty(t, executions)
}

func TestFileStore_ListExecutions_SortedByExecutionID(t *testing.T) {
	root := t.TempDir()
	store := NewFileStore(root)
	newImplementationTask(t, store, "task-a")
	require.NoError(t, store.CreateExecutionLog("demo-project", "task-a", "exec-002"))
	require.NoError(t, store.CreateExecutionLog("demo-project", "task-a", "exec-001"))

	_, err := store.RecordExecution("demo-project", "task-a", Execution{ExecutionID: "exec-002", Status: ExecutionStatusFailure, Failure: &ExecutionFailure{Type: FailureTypeExecution}})
	require.NoError(t, err)
	_, err = store.RecordExecution("demo-project", "task-a", Execution{ExecutionID: "exec-001", Status: ExecutionStatusFailure, Failure: &ExecutionFailure{Type: FailureTypeExecution}})
	require.NoError(t, err)

	executions, err := store.ListExecutions("demo-project", "task-a")
	require.NoError(t, err)
	require.Len(t, executions, 2)
	assert.Equal(t, "exec-001", executions[0].ExecutionID)
	assert.Equal(t, "exec-002", executions[1].ExecutionID)
}

func TestFileStore_ListExecutions_SortedNumericallyPastThreeDigits(t *testing.T) {
	root := t.TempDir()
	store := NewFileStore(root)
	newImplementationTask(t, store, "task-a")
	require.NoError(t, store.CreateExecutionLog("demo-project", "task-a", "exec-1000"))
	require.NoError(t, store.CreateExecutionLog("demo-project", "task-a", "exec-999"))

	_, err := store.RecordExecution("demo-project", "task-a", Execution{ExecutionID: "exec-1000", Status: ExecutionStatusFailure, Failure: &ExecutionFailure{Type: FailureTypeExecution}})
	require.NoError(t, err)
	_, err = store.RecordExecution("demo-project", "task-a", Execution{ExecutionID: "exec-999", Status: ExecutionStatusFailure, Failure: &ExecutionFailure{Type: FailureTypeExecution}})
	require.NoError(t, err)

	executions, err := store.ListExecutions("demo-project", "task-a")
	require.NoError(t, err)
	require.Len(t, executions, 2)
	// Lexical string comparison ("exec-1000" < "exec-999", since '1' < '9')
	// would put exec-1000 first — wrong, since it's chronologically the
	// later attempt. Numeric ordering must put exec-999 first.
	assert.Equal(t, "exec-999", executions[0].ExecutionID)
	assert.Equal(t, "exec-1000", executions[1].ExecutionID)
}

func TestFileStore_RecordExecution_SetsTaskIDAndCreatedAtServerSide(t *testing.T) {
	root := t.TempDir()
	store := NewFileStore(root)
	newImplementationTask(t, store, "task-a")
	require.NoError(t, store.CreateExecutionLog("demo-project", "task-a", "exec-001"))

	recorded, err := store.RecordExecution("demo-project", "task-a", Execution{
		ExecutionID: "exec-001",
		TaskID:      "someone-elses-id",
		Status:      ExecutionStatusFailure,
		Failure:     &ExecutionFailure{Type: FailureTypeExecution},
	})
	require.NoError(t, err)
	assert.Equal(t, "task-a", recorded.TaskID)
	assert.False(t, recorded.CreatedAt.IsZero())
}

// TestFileStore_RecordExecution_ReviewFeedbackRoundTrips confirms
// ExecutionInput.ReviewFeedback (docs/adr/0012) survives a real
// write-then-read cycle through execution.yaml, the same way PlanRef
// already does.
func TestFileStore_RecordExecution_ReviewFeedbackRoundTrips(t *testing.T) {
	root := t.TempDir()
	store := NewFileStore(root)
	newImplementationTask(t, store, "task-a")
	require.NoError(t, store.CreateExecutionLog("demo-project", "task-a", "exec-001"))

	_, err := store.RecordExecution("demo-project", "task-a", Execution{
		ExecutionID: "exec-001",
		Status:      ExecutionStatusSuccess,
		Input:       ExecutionInput{PlanRef: "plan.yaml", ReviewFeedback: "fix the widget"},
	})
	require.NoError(t, err)

	executions, err := store.ListExecutions("demo-project", "task-a")
	require.NoError(t, err)
	require.Len(t, executions, 1)
	assert.Equal(t, "fix the widget", executions[0].Input.ReviewFeedback)
}

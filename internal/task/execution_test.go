package task

import (
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func newImplementationTask(t *testing.T, store *FileStore, id string) Task {
	t.Helper()
	_, err := store.Create(Task{ID: id, Title: "A"})
	require.NoError(t, err)
	_, err = store.FinalizeRequirements(id, RequirementsDraft{Objective: "ship it"})
	require.NoError(t, err)
	_, err = store.FinalizePlan(id, Plan{Approach: "do it"})
	require.NoError(t, err)
	tk, err := store.Get(id)
	require.NoError(t, err)
	require.Equal(t, StageImplementation, tk.Stage)
	return tk
}

func TestFileStore_NextExecutionID_StartsAtExec001AndIncrements(t *testing.T) {
	root := t.TempDir()
	store := NewFileStore(root)
	newImplementationTask(t, store, "task-a")

	id, err := store.NextExecutionID("task-a")
	require.NoError(t, err)
	assert.Equal(t, "exec-001", id)

	_, err = store.RecordExecution("task-a", Execution{ExecutionID: "exec-001", Status: ExecutionStatusFailure, Failure: &ExecutionFailure{Type: FailureTypeExecution}})
	require.NoError(t, err)

	id, err = store.NextExecutionID("task-a")
	require.NoError(t, err)
	assert.Equal(t, "exec-002", id)
}

func TestFileStore_RecordExecution_AppendOnlyRejectsDuplicateID(t *testing.T) {
	root := t.TempDir()
	store := NewFileStore(root)
	newImplementationTask(t, store, "task-a")

	_, err := store.RecordExecution("task-a", Execution{ExecutionID: "exec-001", Status: ExecutionStatusFailure, Failure: &ExecutionFailure{Type: FailureTypeExecution}})
	require.NoError(t, err)

	_, err = store.RecordExecution("task-a", Execution{ExecutionID: "exec-001", Status: ExecutionStatusFailure, Failure: &ExecutionFailure{Type: FailureTypeExecution}})
	require.ErrorIs(t, err, ErrExecutionAlreadyExists)
}

func TestFileStore_RecordExecution_SuccessAdvancesStageToReview(t *testing.T) {
	root := t.TempDir()
	store := NewFileStore(root)
	newImplementationTask(t, store, "task-a")

	_, err := store.RecordExecution("task-a", Execution{
		ExecutionID: "exec-001",
		Status:      ExecutionStatusSuccess,
		Output:      ExecutionOutput{GitBranch: "task-exec/task-a/exec-001", Commits: []string{"abc123"}},
	})
	require.NoError(t, err)

	tk, err := store.Get("task-a")
	require.NoError(t, err)
	assert.Equal(t, StageReview, tk.Stage)
}

func TestFileStore_RecordExecution_FailurePartialNeverAdvancesStage(t *testing.T) {
	root := t.TempDir()
	store := NewFileStore(root)
	newImplementationTask(t, store, "task-a")

	_, err := store.RecordExecution("task-a", Execution{ExecutionID: "exec-001", Status: ExecutionStatusFailure, Failure: &ExecutionFailure{Type: FailureTypeExecution}})
	require.NoError(t, err)

	tk, err := store.Get("task-a")
	require.NoError(t, err)
	assert.Equal(t, StageImplementation, tk.Stage)
}

func TestFileStore_RecordExecution_SuccessWrongStageErrorsAndWritesNothing(t *testing.T) {
	root := t.TempDir()
	store := NewFileStore(root)
	_, err := store.Create(Task{ID: "task-a", Title: "A"}) // still at requirements stage

	require.NoError(t, err)

	_, err = store.RecordExecution("task-a", Execution{ExecutionID: "exec-001", Status: ExecutionStatusSuccess})
	require.ErrorIs(t, err, ErrWrongStage)

	executions, err := store.ListExecutions("task-a")
	require.NoError(t, err)
	assert.Empty(t, executions, "a rejected success attempt must not leave an orphaned execution record")
}

func TestFileStore_ListExecutions_EmptyWhenNoAttemptsRecorded(t *testing.T) {
	root := t.TempDir()
	store := NewFileStore(root)
	newImplementationTask(t, store, "task-a")

	executions, err := store.ListExecutions("task-a")
	require.NoError(t, err)
	assert.Empty(t, executions)
}

func TestFileStore_ListExecutions_SortedByExecutionID(t *testing.T) {
	root := t.TempDir()
	store := NewFileStore(root)
	newImplementationTask(t, store, "task-a")

	_, err := store.RecordExecution("task-a", Execution{ExecutionID: "exec-002", Status: ExecutionStatusFailure, Failure: &ExecutionFailure{Type: FailureTypeExecution}})
	require.NoError(t, err)
	_, err = store.RecordExecution("task-a", Execution{ExecutionID: "exec-001", Status: ExecutionStatusFailure, Failure: &ExecutionFailure{Type: FailureTypeExecution}})
	require.NoError(t, err)

	executions, err := store.ListExecutions("task-a")
	require.NoError(t, err)
	require.Len(t, executions, 2)
	assert.Equal(t, "exec-001", executions[0].ExecutionID)
	assert.Equal(t, "exec-002", executions[1].ExecutionID)
}

func TestFileStore_ListExecutions_SortedNumericallyPastThreeDigits(t *testing.T) {
	root := t.TempDir()
	store := NewFileStore(root)
	newImplementationTask(t, store, "task-a")

	_, err := store.RecordExecution("task-a", Execution{ExecutionID: "exec-1000", Status: ExecutionStatusFailure, Failure: &ExecutionFailure{Type: FailureTypeExecution}})
	require.NoError(t, err)
	_, err = store.RecordExecution("task-a", Execution{ExecutionID: "exec-999", Status: ExecutionStatusFailure, Failure: &ExecutionFailure{Type: FailureTypeExecution}})
	require.NoError(t, err)

	executions, err := store.ListExecutions("task-a")
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

	recorded, err := store.RecordExecution("task-a", Execution{
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

	_, err := store.RecordExecution("task-a", Execution{
		ExecutionID: "exec-001",
		Status:      ExecutionStatusSuccess,
		Input:       ExecutionInput{PlanRef: "plan.yaml", ReviewFeedback: "fix the widget"},
	})
	require.NoError(t, err)

	executions, err := store.ListExecutions("task-a")
	require.NoError(t, err)
	require.Len(t, executions, 1)
	assert.Equal(t, "fix the widget", executions[0].Input.ReviewFeedback)
}

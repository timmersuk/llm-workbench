package task

import (
	"os"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestFileStore_CreateExecutionLog_StartsEmptyAndRejectsDuplicate(t *testing.T) {
	root := t.TempDir()
	store := NewFileStore(root)
	newImplementationTask(t, store, "task-a")

	require.NoError(t, store.CreateExecutionLog("demo-project", "task-a", "exec-001"))

	log, err := store.GetExecutionLog("demo-project", "task-a", "exec-001")
	require.NoError(t, err)
	assert.Equal(t, "exec-001", log.ExecutionID)
	assert.Empty(t, log.Events)

	err = store.CreateExecutionLog("demo-project", "task-a", "exec-001")
	require.Error(t, err)
	assert.ErrorIs(t, err, ErrExecutionLogAlreadyExists)
}

func TestFileStore_AppendExecutionLogEvent_RoundTripsInOrder(t *testing.T) {
	root := t.TempDir()
	store := NewFileStore(root)
	newImplementationTask(t, store, "task-a")
	require.NoError(t, store.CreateExecutionLog("demo-project", "task-a", "exec-001"))

	events := []ExecutionLogEvent{
		{Kind: ExecutionLogEventText, Text: "starting the task"},
		{Kind: ExecutionLogEventToolCall, ToolName: "Bash", ToolInput: `go test ./...`},
		{Kind: ExecutionLogEventToolResult, ToolResult: "ok  \tpackage/foo\t0.01s", IsError: false},
		{Kind: ExecutionLogEventToolResult, ToolResult: "exit status 1", IsError: true},
	}
	for _, ev := range events {
		require.NoError(t, store.AppendExecutionLogEvent("demo-project", "task-a", "exec-001", ev))
	}

	log, err := store.GetExecutionLog("demo-project", "task-a", "exec-001")
	require.NoError(t, err)
	require.Len(t, log.Events, len(events))
	for i, ev := range events {
		got := log.Events[i]
		assert.Equal(t, ev.Kind, got.Kind)
		assert.Equal(t, ev.Text, got.Text)
		assert.Equal(t, ev.ToolName, got.ToolName)
		assert.Equal(t, ev.ToolInput, got.ToolInput)
		assert.Equal(t, ev.ToolResult, got.ToolResult)
		assert.Equal(t, ev.IsError, got.IsError)
		assert.False(t, got.CreatedAt.IsZero())
	}
}

// TestFileStore_AppendExecutionLogEvent_PreservesRawContentUntruncated
// exercises exactly the content shape that already broke yaml.v3 once in
// this codebase (ConversationToolActivity's leading-space/leading-newline
// bug) plus a payload larger than ConversationToolActivity's 2KB
// persistence cap, to prove the execution log survives both: real
// tool-output formatting and no truncation.
func TestFileStore_AppendExecutionLogEvent_PreservesRawContentUntruncated(t *testing.T) {
	root := t.TempDir()
	store := NewFileStore(root)
	newImplementationTask(t, store, "task-a")
	require.NoError(t, store.CreateExecutionLog("demo-project", "task-a", "exec-001"))

	leadingSpaceOutput := " M internal/api/foo.go\n M internal/api/bar.go\n"
	leadingNewlineOutput := "\nBuild started\nBuild finished"
	largePayload := ""
	for i := 0; i < 5000; i++ {
		largePayload += "line of build output\n"
	}
	require.Greater(t, len(largePayload), 2*1024, "payload must exceed ConversationToolActivity's 2KB cap to be a meaningful test")

	require.NoError(t, store.AppendExecutionLogEvent("demo-project", "task-a", "exec-001",
		ExecutionLogEvent{Kind: ExecutionLogEventToolResult, ToolResult: leadingSpaceOutput}))
	require.NoError(t, store.AppendExecutionLogEvent("demo-project", "task-a", "exec-001",
		ExecutionLogEvent{Kind: ExecutionLogEventText, Text: leadingNewlineOutput}))
	require.NoError(t, store.AppendExecutionLogEvent("demo-project", "task-a", "exec-001",
		ExecutionLogEvent{Kind: ExecutionLogEventToolResult, ToolResult: largePayload}))

	log, err := store.GetExecutionLog("demo-project", "task-a", "exec-001")
	require.NoError(t, err)
	require.Len(t, log.Events, 3)
	assert.Equal(t, leadingSpaceOutput, log.Events[0].ToolResult)
	assert.Equal(t, leadingNewlineOutput, log.Events[1].Text)
	assert.Equal(t, largePayload, log.Events[2].ToolResult, "must be preserved in full, not truncated")
}

// TestFileStore_AppendExecutionLogEvent_DoesNotTouchPriorBytes proves the
// write is genuinely append-only: bytes already on disk from an earlier
// call never change when a later event is appended, the property that
// makes a crash mid-write only ever risk the newest entry.
func TestFileStore_AppendExecutionLogEvent_DoesNotTouchPriorBytes(t *testing.T) {
	root := t.TempDir()
	store := NewFileStore(root)
	newImplementationTask(t, store, "task-a")
	require.NoError(t, store.CreateExecutionLog("demo-project", "task-a", "exec-001"))

	require.NoError(t, store.AppendExecutionLogEvent("demo-project", "task-a", "exec-001",
		ExecutionLogEvent{Kind: ExecutionLogEventText, Text: "first"}))

	path := executionLogPath(store.taskDir("demo-project", "task-a"), "exec-001")
	before, err := os.ReadFile(path)
	require.NoError(t, err)

	require.NoError(t, store.AppendExecutionLogEvent("demo-project", "task-a", "exec-001",
		ExecutionLogEvent{Kind: ExecutionLogEventText, Text: "second"}))

	after, err := os.ReadFile(path)
	require.NoError(t, err)
	assert.True(t, len(after) > len(before))
	assert.Equal(t, before, after[:len(before)], "bytes written by the first append must be untouched by the second")
}

func TestFileStore_ExecutionLogExists(t *testing.T) {
	root := t.TempDir()
	store := NewFileStore(root)
	newImplementationTask(t, store, "task-a")

	exists, err := store.ExecutionLogExists("demo-project", "task-a", "exec-001")
	require.NoError(t, err)
	assert.False(t, exists)

	require.NoError(t, store.CreateExecutionLog("demo-project", "task-a", "exec-001"))

	exists, err = store.ExecutionLogExists("demo-project", "task-a", "exec-001")
	require.NoError(t, err)
	assert.True(t, exists)
}

// TestFileStore_ListExecutions_IgnoresSiblingLogFile is a regression test:
// exec-NNN.log.yaml lives in the same executions/ directory as
// exec-NNN.yaml and also ends in .yaml, so a naive "every .yaml file in
// this directory is an Execution record" filter would parse it as a
// second, bogus, mostly-empty Execution (it has its own execution_id
// field, which is enough for yaml.v3 to decode it as one without error).
func TestFileStore_ListExecutions_IgnoresSiblingLogFile(t *testing.T) {
	root := t.TempDir()
	store := NewFileStore(root)
	newImplementationTask(t, store, "task-a")

	require.NoError(t, store.CreateExecutionLog("demo-project", "task-a", "exec-001"))
	require.NoError(t, store.AppendExecutionLogEvent("demo-project", "task-a", "exec-001",
		ExecutionLogEvent{Kind: ExecutionLogEventText, Text: "hello"}))

	_, err := store.RecordExecution("demo-project", "task-a", Execution{
		ExecutionID: "exec-001",
		Status:      ExecutionStatusFailure,
		Failure:     &ExecutionFailure{Type: FailureTypeExecution},
	})
	require.NoError(t, err)

	executions, err := store.ListExecutions("demo-project", "task-a")
	require.NoError(t, err)
	require.Len(t, executions, 1, "the sibling .log.yaml file must not be counted as a second execution")
	assert.Equal(t, "exec-001", executions[0].ExecutionID)
}

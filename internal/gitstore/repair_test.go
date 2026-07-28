package gitstore

import (
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/timmersuk/llm-workbench/internal/task"
	"github.com/timmersuk/llm-workbench/internal/yamlutil"
)

func TestRepairTornYAML_TrimsTornTrailingEntry(t *testing.T) {
	data := []byte("execution_id: exec-001\n" +
		"events:\n" +
		"    - kind: \"text\"\n" +
		"      text: \"first\"\n" +
		"      created_at: 2026-01-01T00:00:00Z\n" +
		"    - kind: \"tool_call\"\n" +
		"      tool_name: \"Bas") // torn mid-value, no closing quote or created_at

	repaired, dropped, ok := repairTornYAML(data)
	require.True(t, ok)
	assert.Greater(t, dropped, 0)

	var probe interface{}
	require.NoError(t, yamlutil.Unmarshal(repaired, &probe))

	var log task.ExecutionLog
	require.NoError(t, yamlutil.Unmarshal(repaired, &log))
	require.Len(t, log.Events, 1, "the torn second entry must be dropped, the complete first entry kept")
	assert.Equal(t, "first", log.Events[0].Text)
}

func TestRepairTornYAML_ValidInputNeedsNoRepair(t *testing.T) {
	data := []byte("execution_id: exec-001\n" +
		"events:\n" +
		"    - kind: \"text\"\n" +
		"      text: \"first\"\n")

	// repairTornYAML is only ever called after a probe parse already
	// failed (repairTornYAMLFile), but it should still behave sanely if
	// handed already-valid input: the loop just finds the trim that
	// happens to reproduce (a subset of) valid content.
	_, _, ok := repairTornYAML(data)
	assert.True(t, ok)
}

func TestRepairTornYAML_NonListDocumentCannotBeRepaired(t *testing.T) {
	// task.yaml-shaped: a plain mapping, no top-level list at all -- a
	// torn write here has no "drop the last list entry" recovery.
	data := []byte("id: task-a\ntitle: \"Unterminated stri")

	_, _, ok := repairTornYAML(data)
	assert.False(t, ok)
}

func TestRepairTornYAML_EmptyEventsListCannotBeFurtherTrimmed(t *testing.T) {
	// A tear inside the very first entry: nothing complete to fall back
	// to except dropping every entry, which the loop does try (i=0), but
	// with zero entries left "events:" is still present with nothing
	// after it, which does parse -- confirm that degenerate case works
	// too rather than erroring out.
	data := []byte("execution_id: exec-001\n" +
		"events:\n" +
		"    - kind: \"tool_resu")

	repaired, dropped, ok := repairTornYAML(data)
	require.True(t, ok)
	assert.Greater(t, dropped, 0)

	var log task.ExecutionLog
	require.NoError(t, yamlutil.Unmarshal(repaired, &log))
	assert.Empty(t, log.Events)
}

// TestAddAndCommit_RepairsTornExecutionLogBeforeCommitting is an
// integration test through the real commit path: a genuinely torn
// exec-NNN.log.yaml (simulating a crash mid-append) must be repaired in
// place and still get committed, with the complete entries preserved and
// the torn one gone.
func TestAddAndCommit_RepairsTornExecutionLogBeforeCommitting(t *testing.T) {
	remote := newBareRemote(t)
	workspaceRoot := filepath.Join(t.TempDir(), "workspace")
	store, err := Open(workspaceRoot, remote)
	require.NoError(t, err)

	_, err = store.Projects.Create(newProjectCreateInput("Demo Project"))
	require.NoError(t, err)
	projects, err := store.Projects.List()
	require.NoError(t, err)
	require.Len(t, projects.Projects, 1)
	projectID := projects.Projects[0].ID

	_, err = store.Tasks.Create(projectID, newTask("task-a"))
	require.NoError(t, err)
	_, err = store.Tasks.FinalizeRequirements(projectID, "task-a", task.RequirementsDraft{Objective: "ship it"})
	require.NoError(t, err)
	_, err = store.Tasks.FinalizePlan(projectID, "task-a", task.Plan{Approach: "do it"})
	require.NoError(t, err)

	require.NoError(t, store.Tasks.CreateExecutionLog(projectID, "task-a", "exec-001"))
	require.NoError(t, store.Tasks.AppendExecutionLogEvent(projectID, "task-a", "exec-001",
		task.ExecutionLogEvent{Kind: task.ExecutionLogEventText, Text: "first"}))
	// Commit what's pending so far (a clean baseline with just "first"),
	// then add a second, real, fully-written event -- so the eventual
	// repair below has to trim back to a state (first+second) that
	// genuinely differs from the last commit, not accidentally reproduce
	// it exactly (which would correctly produce no new commit at all,
	// defeating this test's own assertion that a commit happens).
	require.NoError(t, store.core.commitPending(time.Hour))
	require.NoError(t, store.Tasks.AppendExecutionLogEvent(projectID, "task-a", "exec-001",
		task.ExecutionLogEvent{Kind: task.ExecutionLogEventText, Text: "second"}))
	require.NoError(t, store.core.commitPending(time.Hour))

	// Simulate a crash mid-write of a third event by directly appending a
	// torn fragment to the file's on-disk bytes.
	logPath := filepath.Join(workspaceRoot, "projects", projectID, "tasks", "task-a", "executions", "exec-001.log.yaml")
	before, err := os.ReadFile(logPath)
	require.NoError(t, err)
	torn := append(append([]byte{}, before...), []byte("    - kind: \"tool_call\"\n      tool_name: \"Ba")...)
	require.NoError(t, os.WriteFile(logPath, torn, 0o644))

	// Enqueue a pending change for this task dir the same way a real
	// write does, then drain it -- this is the exact path AppendExecutionLogEvent
	// would have taken had the crash not interrupted it.
	require.NoError(t, store.core.withPending(
		"Append execution log event for task-a execution exec-001",
		func() string { return store.core.taskDir(projectID, "task-a") },
		func() error { return nil },
	))
	require.NoError(t, store.core.commitPending(time.Hour))

	after, err := os.ReadFile(logPath)
	require.NoError(t, err)

	var log task.ExecutionLog
	require.NoError(t, yamlutil.Unmarshal(after, &log), "the committed file must be valid YAML after repair")
	require.Len(t, log.Events, 2, "the torn third event must be dropped, the first two kept")
	assert.Equal(t, "first", log.Events[0].Text)
	assert.Equal(t, "second", log.Events[1].Text)

	out, err := runGit(workspaceRoot, "status", "--porcelain")
	require.NoError(t, err)
	assert.Empty(t, out, "the repaired file must actually be committed, not left dirty")

	out, err = runGit(workspaceRoot, "log", "--pretty=%s")
	require.NoError(t, err)
	assert.Contains(t, out, "Append execution log event")
}

// TestAddAndCommit_RepairsTornConversationBeforeCommitting confirms the
// same repair mechanism works uniformly on conversation-*.yaml too, not
// just execution logs — the whole point of retrofitting
// AppendConversationMessages onto the same real-append/four-space-indent
// convention was that one repair mechanism covers both file kinds.
func TestAddAndCommit_RepairsTornConversationBeforeCommitting(t *testing.T) {
	remote := newBareRemote(t)
	workspaceRoot := filepath.Join(t.TempDir(), "workspace")
	store, err := Open(workspaceRoot, remote)
	require.NoError(t, err)

	_, err = store.Projects.Create(newProjectCreateInput("Demo Project"))
	require.NoError(t, err)
	projects, err := store.Projects.List()
	require.NoError(t, err)
	require.Len(t, projects.Projects, 1)
	projectID := projects.Projects[0].ID

	_, err = store.Tasks.Create(projectID, newTask("task-a"))
	require.NoError(t, err)

	_, err = store.Tasks.AppendConversationMessages(projectID, "task-a", task.StageRequirements,
		task.ConversationMessage{Role: "user", Content: "first"})
	require.NoError(t, err)
	require.NoError(t, store.core.commitPending(time.Hour))

	_, err = store.Tasks.AppendConversationMessages(projectID, "task-a", task.StageRequirements,
		task.ConversationMessage{Role: "assistant", Content: "second"})
	require.NoError(t, err)
	require.NoError(t, store.core.commitPending(time.Hour))

	convPath := filepath.Join(workspaceRoot, "projects", projectID, "tasks", "task-a", "conversation-requirements.yaml")
	before, err := os.ReadFile(convPath)
	require.NoError(t, err)
	torn := append(append([]byte{}, before...), []byte("    - role: \"user\"\n      content: \"unfinis")...)
	require.NoError(t, os.WriteFile(convPath, torn, 0o644))

	require.NoError(t, store.core.withPending(
		"Append conversation message for task-a",
		func() string { return store.core.taskDir(projectID, "task-a") },
		func() error { return nil },
	))
	require.NoError(t, store.core.commitPending(time.Hour))

	after, err := os.ReadFile(convPath)
	require.NoError(t, err)

	var conv task.Conversation
	require.NoError(t, yamlutil.Unmarshal(after, &conv), "the committed conversation file must be valid YAML after repair")
	require.Len(t, conv.Messages, 2, "the torn third message must be dropped, the first two kept")
	assert.Equal(t, "first", conv.Messages[0].Content)
	assert.Equal(t, "second", conv.Messages[1].Content)

	out, err := runGit(workspaceRoot, "status", "--porcelain")
	require.NoError(t, err)
	assert.Empty(t, out, "the repaired file must actually be committed, not left dirty")
}

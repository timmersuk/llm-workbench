package task

import (
	"os"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestFileStore_GetTaskDraftConversation_EmptyWhenNoFileYet(t *testing.T) {
	root := t.TempDir()
	store := NewFileStore(root)

	conv, err := store.GetTaskDraftConversation("demo-project", "session-1")
	require.NoError(t, err)
	assert.Empty(t, conv.Messages)
}

func TestFileStore_GetTaskDraftConversation_RejectsPathTraversal(t *testing.T) {
	root := t.TempDir()
	store := NewFileStore(root)

	_, err := store.GetTaskDraftConversation("demo-project", "../escape")
	require.Error(t, err)
	assert.ErrorIs(t, err, ErrInvalidID)

	_, err = store.GetTaskDraftConversation("../escape", "session-1")
	require.Error(t, err)
	assert.ErrorIs(t, err, ErrInvalidID)
}

func TestFileStore_AppendTaskDraftConversationMessages_AccumulatesAndPersists(t *testing.T) {
	root := t.TempDir()
	store := NewFileStore(root)

	conv, err := store.AppendTaskDraftConversationMessages("demo-project", "session-1",
		ConversationMessage{Role: "user", Content: "I want to build a login page"})
	require.NoError(t, err)
	require.Len(t, conv.Messages, 1)
	assert.False(t, conv.Messages[0].CreatedAt.IsZero())

	conv, err = store.AppendTaskDraftConversationMessages("demo-project", "session-1",
		ConversationMessage{
			Role: "assistant",
			ToolCall: &ConversationToolCall{
				ID:        "call-1",
				Name:      "propose_task",
				Arguments: `{"id":"fix-login-bug","title":"Fix login bug"}`,
			},
		})
	require.NoError(t, err)
	require.Len(t, conv.Messages, 2)
	require.NotNil(t, conv.Messages[1].ToolCall)
	assert.Equal(t, "propose_task", conv.Messages[1].ToolCall.Name)

	reloaded, err := store.GetTaskDraftConversation("demo-project", "session-1")
	require.NoError(t, err)
	assert.Len(t, reloaded.Messages, 2)
	assert.Equal(t, "I want to build a login page", reloaded.Messages[0].Content)
}

func TestFileStore_AppendTaskDraftConversationMessages_IgnoresClientSuppliedCreatedAt(t *testing.T) {
	root := t.TempDir()
	store := NewFileStore(root)

	stale := ConversationMessage{Role: "user", Content: "hi"}
	stale.CreatedAt = stale.CreatedAt.AddDate(-10, 0, 0)

	conv, err := store.AppendTaskDraftConversationMessages("demo-project", "session-1", stale)
	require.NoError(t, err)
	assert.NotEqual(t, stale.CreatedAt, conv.Messages[0].CreatedAt)
}

// TestFileStore_AppendTaskDraftConversationMessages_SeparateFilesPerSession
// mirrors AppendConversationMessages' per-stage isolation: two sessions
// (even within the same project) never share a conversation.yaml.
func TestFileStore_AppendTaskDraftConversationMessages_SeparateFilesPerSession(t *testing.T) {
	root := t.TempDir()
	store := NewFileStore(root)

	_, err := store.AppendTaskDraftConversationMessages("demo-project", "session-a",
		ConversationMessage{Role: "user", Content: "session a chat"})
	require.NoError(t, err)
	_, err = store.AppendTaskDraftConversationMessages("demo-project", "session-b",
		ConversationMessage{Role: "user", Content: "session b chat"})
	require.NoError(t, err)

	convA, err := store.GetTaskDraftConversation("demo-project", "session-a")
	require.NoError(t, err)
	require.Len(t, convA.Messages, 1)
	assert.Equal(t, "session a chat", convA.Messages[0].Content)

	convB, err := store.GetTaskDraftConversation("demo-project", "session-b")
	require.NoError(t, err)
	require.Len(t, convB.Messages, 1)
	assert.Equal(t, "session b chat", convB.Messages[0].Content)
}

// TestFileStore_AppendTaskDraftConversationMessages_DoesNotRewritePriorMessages
// mirrors AppendConversationMessages' own append-only regression guard: a
// crash mid-write can only ever risk the newest message, never anything
// written before it.
func TestFileStore_AppendTaskDraftConversationMessages_DoesNotRewritePriorMessages(t *testing.T) {
	root := t.TempDir()
	store := NewFileStore(root)

	_, err := store.AppendTaskDraftConversationMessages("demo-project", "session-1",
		ConversationMessage{Role: "user", Content: "first"})
	require.NoError(t, err)

	path := taskDraftConversationPath(store.taskDraftDir("demo-project", "session-1"))
	before, err := os.ReadFile(path)
	require.NoError(t, err)

	_, err = store.AppendTaskDraftConversationMessages("demo-project", "session-1",
		ConversationMessage{Role: "assistant", Content: "second"})
	require.NoError(t, err)

	after, err := os.ReadFile(path)
	require.NoError(t, err)
	assert.True(t, len(after) > len(before))
	assert.Equal(t, before, after[:len(before)], "bytes written by the first append must be untouched by the second")
}

func TestFileStore_ReplaceTaskDraftConversationMessages_OverwritesAndPersists(t *testing.T) {
	root := t.TempDir()
	store := NewFileStore(root)

	_, err := store.AppendTaskDraftConversationMessages("demo-project", "session-1",
		ConversationMessage{Role: "user", Content: "first"},
		ConversationMessage{Role: "assistant", Content: "first reply"},
		ConversationMessage{Role: "user", Content: "second"},
	)
	require.NoError(t, err)

	existing, err := store.GetTaskDraftConversation("demo-project", "session-1")
	require.NoError(t, err)
	replacement := []ConversationMessage{existing.Messages[0], existing.Messages[2]}

	conv, err := store.ReplaceTaskDraftConversationMessages("demo-project", "session-1", replacement)
	require.NoError(t, err)
	require.Len(t, conv.Messages, 2)
	assert.Equal(t, "first", conv.Messages[0].Content)
	assert.Equal(t, "second", conv.Messages[1].Content)

	reloaded, err := store.GetTaskDraftConversation("demo-project", "session-1")
	require.NoError(t, err)
	assert.Len(t, reloaded.Messages, 2)
}

func TestFileStore_ReplaceTaskDraftConversationMessages_RejectsPathTraversal(t *testing.T) {
	root := t.TempDir()
	store := NewFileStore(root)

	_, err := store.ReplaceTaskDraftConversationMessages("demo-project", "../escape", nil)
	require.Error(t, err)
	assert.ErrorIs(t, err, ErrInvalidID)
}

// TestFileStore_TaskDraftConversation_RootedOutsideTasksDirectory locks in
// docs/task schema v0.md §1's placement: task-drafts/ is a sibling of
// tasks/ within the project directory, not nested inside it — a session
// exists before any task does, so it can't live under a specific task's own
// directory.
func TestFileStore_TaskDraftConversation_RootedOutsideTasksDirectory(t *testing.T) {
	root := t.TempDir()
	store := NewFileStore(root)

	_, err := store.AppendTaskDraftConversationMessages("demo-project", "session-1",
		ConversationMessage{Role: "user", Content: "hi"})
	require.NoError(t, err)

	dir := store.taskDraftDir("demo-project", "session-1")
	assert.Contains(t, dir, "task-drafts")
	assert.NotContains(t, dir, "tasks"+string(os.PathSeparator)+"session-1")
}

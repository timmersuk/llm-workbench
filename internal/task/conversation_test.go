package task

import (
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestFileStore_GetConversation_EmptyWhenNoFileYet(t *testing.T) {
	root := t.TempDir()
	store := NewFileStore(root)
	_, err := store.Create(Task{ID: "task-a", Title: "A"})
	require.NoError(t, err)

	conv, err := store.GetConversation("task-a", StageRequirements)
	require.NoError(t, err)
	assert.Equal(t, StageRequirements, conv.Stage)
	assert.Empty(t, conv.Messages)
}

func TestFileStore_Conversation_ReviewStageIsValid(t *testing.T) {
	root := t.TempDir()
	store := NewFileStore(root)
	_, err := store.Create(Task{ID: "task-a", Title: "A"})
	require.NoError(t, err)

	// Review (Milestone 6) persists its conversation the same way
	// requirements/planning do — conversation-review.yaml round-trips.
	_, err = store.AppendConversationMessages("task-a", StageReview, ConversationMessage{Role: "assistant", Content: "starting review"})
	require.NoError(t, err)

	conv, err := store.GetConversation("task-a", StageReview)
	require.NoError(t, err)
	assert.Equal(t, StageReview, conv.Stage)
	require.Len(t, conv.Messages, 1)
	assert.Equal(t, "starting review", conv.Messages[0].Content)
}

func TestFileStore_GetConversation_RejectsInvalidStage(t *testing.T) {
	root := t.TempDir()
	store := NewFileStore(root)
	_, err := store.Create(Task{ID: "task-a", Title: "A"})
	require.NoError(t, err)

	_, err = store.GetConversation("task-a", "implementation")
	require.Error(t, err)
	assert.ErrorIs(t, err, ErrInvalidStage)
}

func TestFileStore_GetConversation_RejectsPathTraversal(t *testing.T) {
	root := t.TempDir()
	store := NewFileStore(root)

	_, err := store.GetConversation("../escape", StageRequirements)
	require.Error(t, err)
	assert.ErrorIs(t, err, ErrInvalidID)
}

func TestFileStore_AppendConversationMessages_AccumulatesAndPersists(t *testing.T) {
	root := t.TempDir()
	store := NewFileStore(root)
	_, err := store.Create(Task{ID: "task-a", Title: "A"})
	require.NoError(t, err)

	conv, err := store.AppendConversationMessages("task-a", StageRequirements,
		ConversationMessage{Role: "user", Content: "let's build a login page"})
	require.NoError(t, err)
	require.Len(t, conv.Messages, 1)
	assert.False(t, conv.Messages[0].CreatedAt.IsZero())

	conv, err = store.AppendConversationMessages("task-a", StageRequirements,
		ConversationMessage{
			Role: "assistant",
			ToolCall: &ConversationToolCall{
				ID:        "call-1",
				Name:      "propose_context",
				Arguments: `{"objective":"ship login"}`,
			},
		})
	require.NoError(t, err)
	require.Len(t, conv.Messages, 2)
	require.NotNil(t, conv.Messages[1].ToolCall)
	assert.Equal(t, "propose_context", conv.Messages[1].ToolCall.Name)

	// Re-fetching from disk reflects everything appended so far.
	reloaded, err := store.GetConversation("task-a", StageRequirements)
	require.NoError(t, err)
	assert.Len(t, reloaded.Messages, 2)
	assert.Equal(t, "let's build a login page", reloaded.Messages[0].Content)
}

func TestFileStore_AppendConversationMessages_IgnoresClientSuppliedCreatedAt(t *testing.T) {
	root := t.TempDir()
	store := NewFileStore(root)
	_, err := store.Create(Task{ID: "task-a", Title: "A"})
	require.NoError(t, err)

	stale := ConversationMessage{Role: "user", Content: "hi"}
	stale.CreatedAt = stale.CreatedAt.AddDate(-10, 0, 0) // some clearly-stale non-zero time

	conv, err := store.AppendConversationMessages("task-a", StageRequirements, stale)
	require.NoError(t, err)
	assert.NotEqual(t, stale.CreatedAt, conv.Messages[0].CreatedAt)
}

func TestFileStore_AppendConversationMessages_SeparateFilesPerStage(t *testing.T) {
	root := t.TempDir()
	store := NewFileStore(root)
	_, err := store.Create(Task{ID: "task-a", Title: "A"})
	require.NoError(t, err)

	_, err = store.AppendConversationMessages("task-a", StageRequirements,
		ConversationMessage{Role: "user", Content: "requirements chat"})
	require.NoError(t, err)
	_, err = store.AppendConversationMessages("task-a", StagePlanning,
		ConversationMessage{Role: "user", Content: "planning chat"})
	require.NoError(t, err)

	reqConv, err := store.GetConversation("task-a", StageRequirements)
	require.NoError(t, err)
	require.Len(t, reqConv.Messages, 1)
	assert.Equal(t, "requirements chat", reqConv.Messages[0].Content)

	planConv, err := store.GetConversation("task-a", StagePlanning)
	require.NoError(t, err)
	require.Len(t, planConv.Messages, 1)
	assert.Equal(t, "planning chat", planConv.Messages[0].Content)
}

// gopkg.in/yaml.v3 (v3.0.1, no fix available) fails to round-trip a string
// whose first character is a newline: it marshals without error but the
// resulting file errors on Unmarshal. Raw LLM output commonly starts with
// a blank line, so content must be trimmed before it's persisted.
func TestFileStore_AppendConversationMessages_TrimsContentToAvoidYAMLRoundTripBug(t *testing.T) {
	root := t.TempDir()
	store := NewFileStore(root)
	_, err := store.Create(Task{ID: "task-a", Title: "A"})
	require.NoError(t, err)

	_, err = store.AppendConversationMessages("task-a", StageRequirements,
		ConversationMessage{Role: "assistant", Content: "\n\nHere are some questions:\n1. What?\n"})
	require.NoError(t, err)

	reloaded, err := store.GetConversation("task-a", StageRequirements)
	require.NoError(t, err, "the persisted file must itself be readable back")
	require.Len(t, reloaded.Messages, 1)
	assert.Equal(t, "Here are some questions:\n1. What?", reloaded.Messages[0].Content)
}

func TestFileStore_AppendConversationMessages_RejectsInvalidStage(t *testing.T) {
	root := t.TempDir()
	store := NewFileStore(root)
	_, err := store.Create(Task{ID: "task-a", Title: "A"})
	require.NoError(t, err)

	_, err = store.AppendConversationMessages("task-a", "complete", ConversationMessage{Role: "user", Content: "x"})
	require.Error(t, err)
	assert.ErrorIs(t, err, ErrInvalidStage)
}

func TestFileStore_ReplaceConversationMessages_OverwritesAndPersists(t *testing.T) {
	root := t.TempDir()
	store := NewFileStore(root)
	_, err := store.Create(Task{ID: "task-a", Title: "A"})
	require.NoError(t, err)

	_, err = store.AppendConversationMessages("task-a", StageRequirements,
		ConversationMessage{Role: "user", Content: "first"},
		ConversationMessage{Role: "assistant", Content: "first reply"},
		ConversationMessage{Role: "user", Content: "second"},
	)
	require.NoError(t, err)

	// A delete-style replace: drop the middle message.
	existing, err := store.GetConversation("task-a", StageRequirements)
	require.NoError(t, err)
	replacement := []ConversationMessage{existing.Messages[0], existing.Messages[2]}

	conv, err := store.ReplaceConversationMessages("task-a", StageRequirements, replacement)
	require.NoError(t, err)
	require.Len(t, conv.Messages, 2)
	assert.Equal(t, "first", conv.Messages[0].Content)
	assert.Equal(t, "second", conv.Messages[1].Content)

	reloaded, err := store.GetConversation("task-a", StageRequirements)
	require.NoError(t, err)
	assert.Len(t, reloaded.Messages, 2)
}

func TestFileStore_ReplaceConversationMessages_AllowsEmptyList(t *testing.T) {
	root := t.TempDir()
	store := NewFileStore(root)
	_, err := store.Create(Task{ID: "task-a", Title: "A"})
	require.NoError(t, err)
	_, err = store.AppendConversationMessages("task-a", StageRequirements, ConversationMessage{Role: "user", Content: "x"})
	require.NoError(t, err)

	conv, err := store.ReplaceConversationMessages("task-a", StageRequirements, []ConversationMessage{})
	require.NoError(t, err)
	assert.Empty(t, conv.Messages)

	reloaded, err := store.GetConversation("task-a", StageRequirements)
	require.NoError(t, err)
	assert.Empty(t, reloaded.Messages)
}

func TestFileStore_ReplaceConversationMessages_RejectsInvalidStage(t *testing.T) {
	root := t.TempDir()
	store := NewFileStore(root)
	_, err := store.Create(Task{ID: "task-a", Title: "A"})
	require.NoError(t, err)

	_, err = store.ReplaceConversationMessages("task-a", "complete", nil)
	require.Error(t, err)
	assert.ErrorIs(t, err, ErrInvalidStage)
}

func TestFileStore_ReplaceConversationMessages_RejectsPathTraversal(t *testing.T) {
	root := t.TempDir()
	store := NewFileStore(root)

	_, err := store.ReplaceConversationMessages("../escape", StageRequirements, nil)
	require.Error(t, err)
	assert.ErrorIs(t, err, ErrInvalidID)
}

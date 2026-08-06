package task

import (
	"os"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestTruncateForPersistence_PassesThroughShortStrings(t *testing.T) {
	assert.Equal(t, "short result", TruncateForPersistence("short result"))
}

// TestTruncateForPersistence_CapsAtSmallerThanModelFacingLimit locks in
// docs/adr/0018's decision: the persisted cap (2KB) is deliberately smaller
// than internal/toolloop/tool.go's 16KB model-facing maxToolResultBytes,
// since this is for a human glancing at "what did it do," not a model's
// context window.
func TestTruncateForPersistence_CapsAtSmallerThanModelFacingLimit(t *testing.T) {
	oversized := strings.Repeat("x", 3*1024)
	got := TruncateForPersistence(oversized)
	assert.Less(t, len(got), 3*1024)
	assert.Contains(t, got, "[truncated:")
}

func TestFileStore_GetConversation_EmptyWhenNoFileYet(t *testing.T) {
	root := t.TempDir()
	store := NewFileStore(root)
	_, err := store.Create("demo-project", Task{ID: "task-a", Title: "A"})
	require.NoError(t, err)

	conv, err := store.GetConversation("demo-project", "task-a", StageRequirements)
	require.NoError(t, err)
	assert.Equal(t, StageRequirements, conv.Stage)
	assert.Empty(t, conv.Messages)
}

func TestFileStore_Conversation_ReviewStageIsValid(t *testing.T) {
	root := t.TempDir()
	store := NewFileStore(root)
	_, err := store.Create("demo-project", Task{ID: "task-a", Title: "A"})
	require.NoError(t, err)

	// Review (Milestone 6) persists its conversation the same way
	// requirements/planning do — conversation-review.yaml round-trips.
	_, err = store.AppendConversationMessages("demo-project", "task-a", StageReview, ConversationMessage{Role: "assistant", Content: "starting review"})
	require.NoError(t, err)

	conv, err := store.GetConversation("demo-project", "task-a", StageReview)
	require.NoError(t, err)
	assert.Equal(t, StageReview, conv.Stage)
	require.Len(t, conv.Messages, 1)
	assert.Equal(t, "starting review", conv.Messages[0].Content)
}

func TestFileStore_GetConversation_RejectsInvalidStage(t *testing.T) {
	root := t.TempDir()
	store := NewFileStore(root)
	_, err := store.Create("demo-project", Task{ID: "task-a", Title: "A"})
	require.NoError(t, err)

	_, err = store.GetConversation("demo-project", "task-a", "implementation")
	require.Error(t, err)
	assert.ErrorIs(t, err, ErrInvalidStage)
}

func TestFileStore_GetConversation_RejectsPathTraversal(t *testing.T) {
	root := t.TempDir()
	store := NewFileStore(root)

	_, err := store.GetConversation("demo-project", "../escape", StageRequirements)
	require.Error(t, err)
	assert.ErrorIs(t, err, ErrInvalidID)
}

func TestFileStore_AppendConversationMessages_AccumulatesAndPersists(t *testing.T) {
	root := t.TempDir()
	store := NewFileStore(root)
	_, err := store.Create("demo-project", Task{ID: "task-a", Title: "A"})
	require.NoError(t, err)

	conv, err := store.AppendConversationMessages("demo-project", "task-a", StageRequirements,
		ConversationMessage{Role: "user", Content: "let's build a login page"})
	require.NoError(t, err)
	require.Len(t, conv.Messages, 1)
	assert.False(t, conv.Messages[0].CreatedAt.IsZero())

	conv, err = store.AppendConversationMessages("demo-project", "task-a", StageRequirements,
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
	reloaded, err := store.GetConversation("demo-project", "task-a", StageRequirements)
	require.NoError(t, err)
	assert.Len(t, reloaded.Messages, 2)
	assert.Equal(t, "let's build a login page", reloaded.Messages[0].Content)
}

func TestFileStore_AppendConversationMessages_IgnoresClientSuppliedCreatedAt(t *testing.T) {
	root := t.TempDir()
	store := NewFileStore(root)
	_, err := store.Create("demo-project", Task{ID: "task-a", Title: "A"})
	require.NoError(t, err)

	stale := ConversationMessage{Role: "user", Content: "hi"}
	stale.CreatedAt = stale.CreatedAt.AddDate(-10, 0, 0) // some clearly-stale non-zero time

	conv, err := store.AppendConversationMessages("demo-project", "task-a", StageRequirements, stale)
	require.NoError(t, err)
	assert.NotEqual(t, stale.CreatedAt, conv.Messages[0].CreatedAt)
}

func TestFileStore_AppendConversationMessages_SeparateFilesPerStage(t *testing.T) {
	root := t.TempDir()
	store := NewFileStore(root)
	_, err := store.Create("demo-project", Task{ID: "task-a", Title: "A"})
	require.NoError(t, err)

	_, err = store.AppendConversationMessages("demo-project", "task-a", StageRequirements,
		ConversationMessage{Role: "user", Content: "requirements chat"})
	require.NoError(t, err)
	_, err = store.AppendConversationMessages("demo-project", "task-a", StagePlanning,
		ConversationMessage{Role: "user", Content: "planning chat"})
	require.NoError(t, err)

	reqConv, err := store.GetConversation("demo-project", "task-a", StageRequirements)
	require.NoError(t, err)
	require.Len(t, reqConv.Messages, 1)
	assert.Equal(t, "requirements chat", reqConv.Messages[0].Content)

	planConv, err := store.GetConversation("demo-project", "task-a", StagePlanning)
	require.NoError(t, err)
	require.Len(t, planConv.Messages, 1)
	assert.Equal(t, "planning chat", planConv.Messages[0].Content)
}

// Originally a regression test for a gopkg.in/yaml.v3 bug (fixed by the
// migration to yamlutil/goccy, internal/yamlutil): a string whose first
// character is a newline used to marshal without error but fail on
// Unmarshal. The trim itself is kept as deliberate hygiene now (Raw LLM
// output commonly starts with a blank line), and this still locks in that
// trimmed content round-trips correctly.
func TestFileStore_AppendConversationMessages_TrimsContentToAvoidYAMLRoundTripBug(t *testing.T) {
	root := t.TempDir()
	store := NewFileStore(root)
	_, err := store.Create("demo-project", Task{ID: "task-a", Title: "A"})
	require.NoError(t, err)

	_, err = store.AppendConversationMessages("demo-project", "task-a", StageRequirements,
		ConversationMessage{Role: "assistant", Content: "\n\nHere are some questions:\n1. What?\n"})
	require.NoError(t, err)

	reloaded, err := store.GetConversation("demo-project", "task-a", StageRequirements)
	require.NoError(t, err, "the persisted file must itself be readable back")
	require.Len(t, reloaded.Messages, 1)
	assert.Equal(t, "Here are some questions:\n1. What?", reloaded.Messages[0].Content)
}

// TestFileStore_AppendConversationMessages_ToolActivityWithLeadingSpaceLinesRoundTrips
// was originally a regression test for a second, distinct gopkg.in/yaml.v3
// round-trip bug from the one above (fixed by the migration to
// yamlutil/goccy): raw tool output whose lines themselves start with a
// leading space (git diff --stat, test runner summaries, ls -l, ...) used
// to force yaml.v3's block-literal encoder into a self-inconsistent
// encoding that failed to parse back. Kept as a permanent regression
// guard on this exact content shape.
func TestFileStore_AppendConversationMessages_ToolActivityWithLeadingSpaceLinesRoundTrips(t *testing.T) {
	root := t.TempDir()
	store := NewFileStore(root)
	_, err := store.Create("demo-project", Task{ID: "task-a", Title: "A"})
	require.NoError(t, err)

	diffStatOutput := " frontend/src/GrillMePanel.test.tsx      | 169 ++++++++++++++++++++++++++++++++\n" +
		" frontend/src/PlanningModePanel.test.tsx |  86 +++++++++++++++-\n" +
		" 7 files changed, 505 insertions(+), 20 deletions(-)"

	_, err = store.AppendConversationMessages("demo-project", "task-a", StageReview, ConversationMessage{
		Role:    "assistant",
		Content: "ran the checks",
		ToolActivity: []ConversationToolActivity{
			{Name: "Bash", Arguments: `{"command":"git diff --stat"}`, Result: diffStatOutput},
		},
	})
	require.NoError(t, err)

	reloaded, err := store.GetConversation("demo-project", "task-a", StageReview)
	require.NoError(t, err, "the persisted file must itself be readable back")
	require.Len(t, reloaded.Messages, 1)
	require.Len(t, reloaded.Messages[0].ToolActivity, 1)
	assert.Equal(t, diffStatOutput, reloaded.Messages[0].ToolActivity[0].Result)
}

// TestFileStore_AppendConversationMessages_ContentWithLeadingSpaceLinesRoundTrips
// covers the same leading-space-lines shape as
// TestFileStore_AppendConversationMessages_ToolActivityWithLeadingSpaceLinesRoundTrips,
// except this is ordinary LLM prose, not tool output: any reply that
// quotes something already-indented (a git diff --stat, an ls -l, a
// fenced code block) has lines starting with a leading space. Under
// yaml.v3 this was a live, un-guarded bug in Content's encoding — unlike
// ToolActivity, Content had no double-quoted-style workaround at the
// time. The migration to yamlutil/goccy fixes it at the encoder level;
// this test is the permanent regression guard.
func TestFileStore_AppendConversationMessages_ContentWithLeadingSpaceLinesRoundTrips(t *testing.T) {
	root := t.TempDir()
	store := NewFileStore(root)
	_, err := store.Create("demo-project", Task{ID: "task-a", Title: "A"})
	require.NoError(t, err)

	quotedDiffStat := "Here's what changed:\n\n" +
		" internal/api/foo.go | 2 +-\n" +
		" internal/api/bar.go | 4 ++--"

	_, err = store.AppendConversationMessages("demo-project", "task-a", StageReview,
		ConversationMessage{Role: "assistant", Content: quotedDiffStat + "\n"})
	require.NoError(t, err)

	reloaded, err := store.GetConversation("demo-project", "task-a", StageReview)
	require.NoError(t, err, "the persisted file must itself be readable back")
	require.Len(t, reloaded.Messages, 1)
	assert.Equal(t, quotedDiffStat, reloaded.Messages[0].Content, "trailing whitespace is trimmed by design (existing TrimSpace behavior); the leading-space content lines must still round-trip intact")
}

// TestFileStore_AppendConversationMessages_SegmentsRoundTrip proves
// ConversationMessage.MarshalYAML actually persists Segments (docs/adr/0023)
// — the field is added to MarshalYAML's explicit allowlist struct, so
// without that this would silently write nothing and GetConversation would
// come back with an empty Segments regardless of what was appended. Also
// covers the leading-space-lines shape (same regression class as the two
// tests above) for a Text segment specifically, since it carries its own
// yamlutil.Quoted treatment separate from Content's.
func TestFileStore_AppendConversationMessages_SegmentsRoundTrip(t *testing.T) {
	root := t.TempDir()
	store := NewFileStore(root)
	_, err := store.Create("demo-project", Task{ID: "task-a", Title: "A"})
	require.NoError(t, err)

	narration := "Here's the diff:\n\n" +
		" internal/api/foo.go | 2 +-\n" +
		" internal/api/bar.go | 4 ++--"

	_, err = store.AppendConversationMessages("demo-project", "task-a", StageReview, ConversationMessage{
		Role:    "assistant",
		Content: narration + " ok, now testing all green",
		ToolActivity: []ConversationToolActivity{
			{Name: "Bash", Arguments: `{"command":"go test ./..."}`, Result: "ok"},
		},
		Segments: []ConversationSegment{
			{Kind: SegmentKindText, Text: narration},
			{Kind: SegmentKindTools, ToolActivity: []ConversationToolActivity{
				{Name: "Bash", Arguments: `{"command":"go test ./..."}`, Result: "ok"},
			}},
			{Kind: SegmentKindText, Text: " ok, now testing all green"},
		},
	})
	require.NoError(t, err)

	reloaded, err := store.GetConversation("demo-project", "task-a", StageReview)
	require.NoError(t, err, "the persisted file must itself be readable back")
	require.Len(t, reloaded.Messages, 1)

	segments := reloaded.Messages[0].Segments
	require.Len(t, segments, 3)
	assert.Equal(t, SegmentKindText, segments[0].Kind)
	assert.Equal(t, narration, segments[0].Text, "leading-space diff lines inside a Text segment must round-trip intact")
	assert.Equal(t, SegmentKindTools, segments[1].Kind)
	require.Len(t, segments[1].ToolActivity, 1)
	assert.Equal(t, "ok", segments[1].ToolActivity[0].Result)
	assert.Equal(t, SegmentKindText, segments[2].Kind)
	assert.Equal(t, " ok, now testing all green", segments[2].Text)
}

// TestFileStore_AppendConversationMessages_DoesNotRewritePriorMessages proves
// the append is genuinely append-only at the byte level (no read-modify-
// rewrite of the whole file): bytes already on disk from an earlier call
// are untouched by a later one, the same property
// AppendExecutionLogEvent's tests lock in — a crash mid-write can only
// ever risk the newest message, never anything written before it.
// TestFileStore_AppendConversationMessages_ListItemsIndentFourSpaces is a
// regression test for a real production incident (2026-08-06): every
// AppendConversationMessages call writes each message's `- role: ...` list
// marker indented 8 spaces instead of 4. yamlutil.Marshal's
// yaml.IndentSequence(true) option already indents a bare top-level
// `[]ConversationMessage` sequence by one level (4 spaces) on its own —
// indentBlock's own unconditional 4-space prefix on top of that produces 8,
// not the 4 a sibling of the file's `messages:` key needs. A file whose
// early entries were written before this drift (a different indent
// baseline, e.g. across a process restart) and later entries after it ends
// up with inconsistent sibling indentation: the deeper-indented entries
// parse as nested content of the previous entry rather than new `messages:`
// list items, so GetConversation silently stops returning anything past
// the last consistently-indented entry — no error, just missing history.
func TestFileStore_AppendConversationMessages_ListItemsIndentFourSpaces(t *testing.T) {
	root := t.TempDir()
	store := NewFileStore(root)
	_, err := store.Create("demo-project", Task{ID: "task-a", Title: "A"})
	require.NoError(t, err)

	_, err = store.AppendConversationMessages("demo-project", "task-a", StagePlanning,
		ConversationMessage{Role: "user", Content: "first"})
	require.NoError(t, err)
	_, err = store.AppendConversationMessages("demo-project", "task-a", StagePlanning,
		ConversationMessage{Role: "assistant", Content: "second"})
	require.NoError(t, err)

	path := conversationPath(store.taskDir("demo-project", "task-a"), StagePlanning)
	raw, err := os.ReadFile(path)
	require.NoError(t, err)

	for _, line := range strings.Split(string(raw), "\n") {
		if strings.Contains(line, "- role:") {
			indent := len(line) - len(strings.TrimLeft(line, " "))
			assert.Equal(t, 4, indent, "every `- role:` list item must sit at the same 4-space indent as a sibling of `messages:`, got: %q", line)
		}
	}

	reloaded, err := store.GetConversation("demo-project", "task-a", StagePlanning)
	require.NoError(t, err, "the persisted file must itself be readable back")
	require.Len(t, reloaded.Messages, 2)
	assert.Equal(t, "second", reloaded.Messages[1].Content)
}

func TestFileStore_AppendConversationMessages_DoesNotRewritePriorMessages(t *testing.T) {
	root := t.TempDir()
	store := NewFileStore(root)
	_, err := store.Create("demo-project", Task{ID: "task-a", Title: "A"})
	require.NoError(t, err)

	_, err = store.AppendConversationMessages("demo-project", "task-a", StageRequirements,
		ConversationMessage{Role: "user", Content: "first"})
	require.NoError(t, err)

	path := conversationPath(store.taskDir("demo-project", "task-a"), StageRequirements)
	before, err := os.ReadFile(path)
	require.NoError(t, err)

	_, err = store.AppendConversationMessages("demo-project", "task-a", StageRequirements,
		ConversationMessage{Role: "assistant", Content: "second"})
	require.NoError(t, err)

	after, err := os.ReadFile(path)
	require.NoError(t, err)
	assert.True(t, len(after) > len(before))
	assert.Equal(t, before, after[:len(before)], "bytes written by the first append must be untouched by the second")
}

func TestFileStore_AppendConversationMessages_RejectsInvalidStage(t *testing.T) {
	root := t.TempDir()
	store := NewFileStore(root)
	_, err := store.Create("demo-project", Task{ID: "task-a", Title: "A"})
	require.NoError(t, err)

	_, err = store.AppendConversationMessages("demo-project", "task-a", "complete", ConversationMessage{Role: "user", Content: "x"})
	require.Error(t, err)
	assert.ErrorIs(t, err, ErrInvalidStage)
}

func TestFileStore_ReplaceConversationMessages_OverwritesAndPersists(t *testing.T) {
	root := t.TempDir()
	store := NewFileStore(root)
	_, err := store.Create("demo-project", Task{ID: "task-a", Title: "A"})
	require.NoError(t, err)

	_, err = store.AppendConversationMessages("demo-project", "task-a", StageRequirements,
		ConversationMessage{Role: "user", Content: "first"},
		ConversationMessage{Role: "assistant", Content: "first reply"},
		ConversationMessage{Role: "user", Content: "second"},
	)
	require.NoError(t, err)

	// A delete-style replace: drop the middle message.
	existing, err := store.GetConversation("demo-project", "task-a", StageRequirements)
	require.NoError(t, err)
	replacement := []ConversationMessage{existing.Messages[0], existing.Messages[2]}

	conv, err := store.ReplaceConversationMessages("demo-project", "task-a", StageRequirements, replacement)
	require.NoError(t, err)
	require.Len(t, conv.Messages, 2)
	assert.Equal(t, "first", conv.Messages[0].Content)
	assert.Equal(t, "second", conv.Messages[1].Content)

	reloaded, err := store.GetConversation("demo-project", "task-a", StageRequirements)
	require.NoError(t, err)
	assert.Len(t, reloaded.Messages, 2)
}

func TestFileStore_ReplaceConversationMessages_AllowsEmptyList(t *testing.T) {
	root := t.TempDir()
	store := NewFileStore(root)
	_, err := store.Create("demo-project", Task{ID: "task-a", Title: "A"})
	require.NoError(t, err)
	_, err = store.AppendConversationMessages("demo-project", "task-a", StageRequirements, ConversationMessage{Role: "user", Content: "x"})
	require.NoError(t, err)

	conv, err := store.ReplaceConversationMessages("demo-project", "task-a", StageRequirements, []ConversationMessage{})
	require.NoError(t, err)
	assert.Empty(t, conv.Messages)

	reloaded, err := store.GetConversation("demo-project", "task-a", StageRequirements)
	require.NoError(t, err)
	assert.Empty(t, reloaded.Messages)
}

func TestFileStore_ReplaceConversationMessages_RejectsInvalidStage(t *testing.T) {
	root := t.TempDir()
	store := NewFileStore(root)
	_, err := store.Create("demo-project", Task{ID: "task-a", Title: "A"})
	require.NoError(t, err)

	_, err = store.ReplaceConversationMessages("demo-project", "task-a", "complete", nil)
	require.Error(t, err)
	assert.ErrorIs(t, err, ErrInvalidStage)
}

func TestFileStore_ReplaceConversationMessages_RejectsPathTraversal(t *testing.T) {
	root := t.TempDir()
	store := NewFileStore(root)

	_, err := store.ReplaceConversationMessages("demo-project", "../escape", StageRequirements, nil)
	require.Error(t, err)
	assert.ErrorIs(t, err, ErrInvalidID)
}

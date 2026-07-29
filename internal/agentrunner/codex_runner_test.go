package agentrunner

import (
	"testing"
	"time"

	"github.com/hishamkaram/codex-agent-sdk-go/types"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/timmersuk/llm-workbench/internal/chat"
)

func TestCodexRunner_TryLockRejectsConcurrentSameKey(t *testing.T) {
	r := NewCodexRunner(time.Minute, time.Minute, "", "", "")

	assert.True(t, r.tryLock("task-a:planning"))
	assert.False(t, r.tryLock("task-a:planning"), "a second lock for the same key must be rejected")
	assert.True(t, r.tryLock("task-b:planning"), "a different key must not be blocked")

	r.unlock("task-a:planning")
	assert.True(t, r.tryLock("task-a:planning"), "unlocking must free the key for reuse")
}

func TestCodexRunner_RunTimeout_UsesExecuteTimeoutWhenBashEnabled(t *testing.T) {
	r := NewCodexRunner(5*time.Minute, 30*time.Minute, "", "", "")
	assert.Equal(t, 30*time.Minute, r.runTimeout(RunInput{EnableBashTool: true}))
}

func TestCodexRunner_RunTimeout_UsesRunTimeoutWhenBashDisabled(t *testing.T) {
	r := NewCodexRunner(5*time.Minute, 30*time.Minute, "", "", "")
	assert.Equal(t, 5*time.Minute, r.runTimeout(RunInput{EnableBashTool: false}))
}

func TestCodexRunner_CheckHealth(t *testing.T) {
	origLookPath := lookPath
	defer func() { lookPath = origLookPath }()

	t.Run("missing reposRoot", func(t *testing.T) {
		r := NewCodexRunner(time.Minute, time.Minute, "", "/bin/draftmcp", "")
		assert.Error(t, r.CheckHealth(t.Context()))
	})

	t.Run("missing draftMCPPath", func(t *testing.T) {
		r := NewCodexRunner(time.Minute, time.Minute, "/repos", "", "")
		assert.Error(t, r.CheckHealth(t.Context()))
	})

	t.Run("codex not on PATH", func(t *testing.T) {
		lookPath = func(string) (string, error) { return "", assert.AnError }
		r := NewCodexRunner(time.Minute, time.Minute, "/repos", "/bin/draftmcp", "")
		assert.Error(t, r.CheckHealth(t.Context()))
	})

	t.Run("healthy", func(t *testing.T) {
		lookPath = func(string) (string, error) { return "/usr/bin/codex", nil }
		r := NewCodexRunner(time.Minute, time.Minute, "/repos", "/bin/draftmcp", "")
		assert.NoError(t, r.CheckHealth(t.Context()))
	})
}

func TestCodexRunner_ListModels_ReturnsNil(t *testing.T) {
	r := NewCodexRunner(time.Minute, time.Minute, "/repos", "/bin/draftmcp", "")
	models, err := r.ListModels(t.Context())
	assert.NoError(t, err)
	assert.Nil(t, models)
}

func TestCodexRunner_CloseSession_NoopSafeForUnknownKey(t *testing.T) {
	r := NewCodexRunner(time.Minute, time.Minute, "/repos", "/bin/draftmcp", "")
	r.CloseSession("never-existed")
}

func TestTomlQuote(t *testing.T) {
	cases := []struct {
		name string
		in   string
		want string
	}{
		{"plain", "hello", `"hello"`},
		{"quote", `say "hi"`, `"say \"hi\""`},
		{"backslash", `a\b`, `"a\\b"`},
		{"newline", "line1\nline2", `"line1\nline2"`},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			assert.Equal(t, tc.want, tomlQuote(tc.in))
		})
	}
}

func TestDeveloperInstructionsArgs(t *testing.T) {
	assert.Nil(t, developerInstructionsArgs(""))

	args := developerInstructionsArgs("be terse")
	require.Len(t, args, 2)
	assert.Equal(t, "-c", args[0])
	assert.Equal(t, `developer_instructions="be terse"`, args[1])
}

func TestDraftToolInstruction_NamesToolAndServer(t *testing.T) {
	instr := draftToolInstruction([]string{"propose_plan"})
	assert.Contains(t, instr, "propose_plan")
	assert.Contains(t, instr, codexDraftServerName)
}

func TestProcessCodexRunEvent_StreamsTextDelta(t *testing.T) {
	var content assistantText
	var out RunOutput
	var streamed string

	ev := &types.ItemUpdated{Delta: &types.AgentMessageDelta{TextChunk: "hel"}}
	done, err := processCodexRunEvent(ev, []string{"propose_plan"}, &content, &out, func(d chat.Delta) error {
		streamed += d.Content
		return nil
	}, nil, nil)
	require.NoError(t, err)
	assert.False(t, done)
	assert.Equal(t, "hel", streamed)
}

func TestProcessCodexRunEvent_AccumulatesAgentMessageText(t *testing.T) {
	var content assistantText
	var out RunOutput

	ev := &types.ItemUpdated{Delta: &types.AgentMessageDelta{TextChunk: "hello "}}
	done, err := processCodexRunEvent(ev, []string{"propose_plan"}, &content, &out, nil, nil, nil)
	require.NoError(t, err)
	assert.False(t, done)
	assert.Equal(t, "hello ", content.String())
}

func TestProcessCodexRunEvent_SeparatesTextFromDistinctAgentMessages(t *testing.T) {
	var content assistantText
	var out RunOutput

	_, err := processCodexRunEvent(&types.ItemStarted{Item: &types.AgentMessage{}}, []string{"propose_plan"}, &content, &out, nil, nil, nil)
	require.NoError(t, err)
	first := &types.ItemUpdated{Delta: &types.AgentMessageDelta{TextChunk: "Running the test suite first."}}
	_, err = processCodexRunEvent(first, []string{"propose_plan"}, &content, &out, nil, nil, nil)
	require.NoError(t, err)

	_, err = processCodexRunEvent(&types.ItemStarted{Item: &types.AgentMessage{}}, []string{"propose_plan"}, &content, &out, nil, nil, nil)
	require.NoError(t, err)
	second := &types.ItemUpdated{Delta: &types.AgentMessageDelta{TextChunk: "Tests pass, moving on."}}
	_, err = processCodexRunEvent(second, []string{"propose_plan"}, &content, &out, nil, nil, nil)
	require.NoError(t, err)

	assert.Equal(t, "Running the test suite first.\n\nTests pass, moving on.", content.String())
}

func TestProcessCodexRunEvent_CapturesMatchingMCPToolCall(t *testing.T) {
	var content assistantText
	var out RunOutput

	ev := &types.ItemCompleted{Item: &types.MCPToolCall{
		ID:       "call-1",
		ToolName: "propose_plan",
		Input:    []byte(`{"approach":"do it"}`),
	}}
	done, err := processCodexRunEvent(ev, []string{"propose_plan"}, &content, &out, nil, nil, nil)
	require.NoError(t, err)
	assert.False(t, done)
	require.NotNil(t, out.ToolCall)
	assert.Equal(t, "call-1", out.ToolCall.ID)
	assert.Equal(t, "propose_plan", out.ToolCall.Function.Name)
	assert.JSONEq(t, `{"approach":"do it"}`, out.ToolCall.Function.Arguments)
}

// TestProcessCodexRunEvent_MatchesAnyOfSeveralOfferedTools covers Review's
// shape (docs/milestones/done/milestone9.md): a session can offer more than one
// Draft tool at once, and a call to either is recognized as the proposal.
func TestProcessCodexRunEvent_MatchesAnyOfSeveralOfferedTools(t *testing.T) {
	var content assistantText
	var out RunOutput

	ev := &types.ItemCompleted{Item: &types.MCPToolCall{
		ID:       "call-1",
		ToolName: "propose_knowledge",
		Input:    []byte(`{"concept_id":"x"}`),
	}}
	done, err := processCodexRunEvent(ev, []string{"propose_review", "propose_knowledge"}, &content, &out, nil, nil, nil)
	require.NoError(t, err)
	assert.False(t, done)
	require.NotNil(t, out.ToolCall)
	assert.Equal(t, "propose_knowledge", out.ToolCall.Function.Name)
}

func TestProcessCodexRunEvent_IgnoresNonMatchingMCPToolCall(t *testing.T) {
	var content assistantText
	var out RunOutput

	ev := &types.ItemCompleted{Item: &types.MCPToolCall{ToolName: "activate_project", Input: []byte(`{}`)}}
	done, err := processCodexRunEvent(ev, []string{"propose_plan"}, &content, &out, nil, nil, nil)
	require.NoError(t, err)
	assert.False(t, done)
	assert.Nil(t, out.ToolCall)
}

// TestProcessCodexRunEvent_CommandExecutionForwardsAsToolActivity locks in
// the docs/adr/0018 fix: CodexRunner.Run had no OnToolCall/OnToolResult
// wiring at all before this ADR — a shell command run during a stage
// conversation (e.g. reading a file via cat/grep, since codex has no
// separate Read/Grep/Glob tools) is now surfaced the same way
// processCodexExecuteEvent already does for Execute.
func TestProcessCodexRunEvent_CommandExecutionForwardsAsToolActivity(t *testing.T) {
	var content assistantText
	var out RunOutput
	var calls []string
	var results []string

	ev := &types.ItemCompleted{Item: &types.CommandExecution{
		Command: "grep -r TODO .", Status: "success", AggregatedOutput: "no matches",
	}}
	done, err := processCodexRunEvent(ev, []string{"propose_plan"}, &content, &out, nil,
		func(name, args string) { calls = append(calls, name+":"+args) },
		func(name, result string, isError bool) { results = append(results, name+":"+result) },
	)
	require.NoError(t, err)
	assert.False(t, done)
	assert.Equal(t, []string{"Bash:grep -r TODO ."}, calls)
	assert.Equal(t, []string{"Bash:no matches"}, results)
}

// TestProcessCodexRunEvent_NonMatchingMCPToolCallForwardsAsToolActivity
// covers the knowledge-query tool case: an MCPToolCall that isn't the
// turn's Draft (e.g. list_knowledge_concepts) must still surface as tool
// activity, not be silently dropped the way ClaudeRunner's processMessage
// used to drop everything but the Draft match.
func TestProcessCodexRunEvent_NonMatchingMCPToolCallForwardsAsToolActivity(t *testing.T) {
	var content assistantText
	var out RunOutput
	var calls, results int

	ev := &types.ItemCompleted{Item: &types.MCPToolCall{
		ToolName: "list_knowledge_concepts", Input: []byte(`{}`), Result: []byte(`["a"]`), Status: "completed",
	}}
	done, err := processCodexRunEvent(ev, []string{"propose_plan"}, &content, &out, nil,
		func(string, string) { calls++ },
		func(string, string, bool) { results++ },
	)
	require.NoError(t, err)
	assert.False(t, done)
	assert.Nil(t, out.ToolCall, "a non-Draft MCP call must never populate out.ToolCall")
	assert.Equal(t, 1, calls)
	assert.Equal(t, 1, results)
}

// TestProcessCodexRunEvent_DraftMCPToolCallNeverForwardedAsActivity mirrors
// claude_runner_test.go's equivalent: the Draft proposal itself must not
// also fire onToolCall/onToolResult — Tool Activity is explicitly distinct
// from a Draft (CONTEXT.md).
func TestProcessCodexRunEvent_DraftMCPToolCallNeverForwardedAsActivity(t *testing.T) {
	var content assistantText
	var out RunOutput
	var calls int

	ev := &types.ItemCompleted{Item: &types.MCPToolCall{
		ID: "call-1", ToolName: "propose_plan", Input: []byte(`{}`),
	}}
	done, err := processCodexRunEvent(ev, []string{"propose_plan"}, &content, &out, nil, func(string, string) { calls++ }, nil)
	require.NoError(t, err)
	assert.False(t, done)
	require.NotNil(t, out.ToolCall)
	assert.Zero(t, calls, "the Draft's own proposal call must never also surface as tool activity")
}

func TestProcessCodexRunEvent_TurnCompletedSuccess(t *testing.T) {
	var content assistantText
	content.WriteString("final answer")
	var out RunOutput

	done, err := processCodexRunEvent(&types.TurnCompleted{Status: "completed"}, []string{"propose_plan"}, &content, &out, nil, nil, nil)
	require.NoError(t, err)
	assert.True(t, done)
	assert.Equal(t, "final answer", out.Content)
}

func TestProcessCodexRunEvent_TurnCompletedFailedStatus(t *testing.T) {
	var content assistantText
	var out RunOutput

	done, err := processCodexRunEvent(&types.TurnCompleted{Status: "failed"}, []string{"propose_plan"}, &content, &out, nil, nil, nil)
	assert.True(t, done)
	assert.Error(t, err)
}

func TestProcessCodexRunEvent_TurnFailed(t *testing.T) {
	var content assistantText
	var out RunOutput

	done, err := processCodexRunEvent(&types.TurnFailed{Message: "boom"}, []string{"propose_plan"}, &content, &out, nil, nil, nil)
	assert.True(t, done)
	require.Error(t, err)
	assert.Contains(t, err.Error(), "boom")
}

func TestProcessCodexExecuteEvent_SeparatesTextFromDistinctAgentMessages(t *testing.T) {
	var content assistantText
	var out ExecuteOutput

	_, err := processCodexExecuteEvent(&types.ItemStarted{Item: &types.AgentMessage{}}, &content, &out, func(ExecuteEvent) error { return nil })
	require.NoError(t, err)
	first := &types.ItemUpdated{Delta: &types.AgentMessageDelta{TextChunk: "Running the test suite first."}}
	_, err = processCodexExecuteEvent(first, &content, &out, func(ExecuteEvent) error { return nil })
	require.NoError(t, err)

	_, err = processCodexExecuteEvent(&types.ItemStarted{Item: &types.AgentMessage{}}, &content, &out, func(ExecuteEvent) error { return nil })
	require.NoError(t, err)
	second := &types.ItemUpdated{Delta: &types.AgentMessageDelta{TextChunk: "Tests pass, moving on."}}
	_, err = processCodexExecuteEvent(second, &content, &out, func(ExecuteEvent) error { return nil })
	require.NoError(t, err)

	assert.Equal(t, "Running the test suite first.\n\nTests pass, moving on.", content.String())
}

func TestProcessCodexExecuteEvent_SeparatesTextFromDistinctAgentMessagesWhenOnEventNil(t *testing.T) {
	var content assistantText
	var out ExecuteOutput

	_, err := processCodexExecuteEvent(&types.ItemStarted{Item: &types.AgentMessage{}}, &content, &out, nil)
	require.NoError(t, err)
	first := &types.ItemUpdated{Delta: &types.AgentMessageDelta{TextChunk: "Running the test suite first."}}
	_, err = processCodexExecuteEvent(first, &content, &out, nil)
	require.NoError(t, err)

	_, err = processCodexExecuteEvent(&types.ItemStarted{Item: &types.AgentMessage{}}, &content, &out, nil)
	require.NoError(t, err)
	second := &types.ItemUpdated{Delta: &types.AgentMessageDelta{TextChunk: "Tests pass, moving on."}}
	_, err = processCodexExecuteEvent(second, &content, &out, nil)
	require.NoError(t, err)

	assert.Equal(t, "Running the test suite first.\n\nTests pass, moving on.", content.String())
}

func TestProcessCodexExecuteEvent_CommandExecutionEmitsCallAndResult(t *testing.T) {
	var content assistantText
	var out ExecuteOutput
	var events []ExecuteEvent

	ev := &types.ItemCompleted{Item: &types.CommandExecution{
		Command:          "echo hi",
		Status:           "success",
		AggregatedOutput: "hi\n",
	}}
	done, err := processCodexExecuteEvent(ev, &content, &out, func(e ExecuteEvent) error {
		events = append(events, e)
		return nil
	})
	require.NoError(t, err)
	assert.False(t, done)
	require.Len(t, events, 2)
	assert.Equal(t, "tool_call", events[0].Kind)
	assert.Equal(t, "Bash", events[0].ToolName)
	assert.Equal(t, "echo hi", events[0].ToolInput)
	assert.Equal(t, "tool_result", events[1].Kind)
	assert.Equal(t, "hi\n", events[1].ToolResult)
	assert.False(t, events[1].IsError)
}

func TestProcessCodexExecuteEvent_CommandExecutionFailureMarksError(t *testing.T) {
	var content assistantText
	var out ExecuteOutput
	var events []ExecuteEvent

	ev := &types.ItemCompleted{Item: &types.CommandExecution{Command: "false", Status: "failed"}}
	_, err := processCodexExecuteEvent(ev, &content, &out, func(e ExecuteEvent) error {
		events = append(events, e)
		return nil
	})
	require.NoError(t, err)
	require.Len(t, events, 2)
	assert.True(t, events[1].IsError)
}

func TestProcessCodexExecuteEvent_FileChangeEmitsCallAndResult(t *testing.T) {
	var content assistantText
	var out ExecuteOutput
	var events []ExecuteEvent

	ev := &types.ItemCompleted{Item: &types.FileChange{
		Status:  "completed",
		Changes: []types.FileChangePart{{Path: "a.go", Operation: "create"}},
	}}
	done, err := processCodexExecuteEvent(ev, &content, &out, func(e ExecuteEvent) error {
		events = append(events, e)
		return nil
	})
	require.NoError(t, err)
	assert.False(t, done)
	require.Len(t, events, 2)
	assert.Equal(t, "FileChange", events[0].ToolName)
	assert.Contains(t, events[0].ToolInput, "a.go")
	assert.False(t, events[1].IsError)
}

func TestProcessCodexExecuteEvent_MCPToolCallEmitsCallAndResult(t *testing.T) {
	var content assistantText
	var out ExecuteOutput
	var events []ExecuteEvent

	ev := &types.ItemCompleted{Item: &types.MCPToolCall{
		ToolName: "propose_plan",
		Input:    []byte(`{"approach":"x"}`),
		Result:   []byte(`{"ok":true}`),
		Status:   "completed",
	}}
	done, err := processCodexExecuteEvent(ev, &content, &out, func(e ExecuteEvent) error {
		events = append(events, e)
		return nil
	})
	require.NoError(t, err)
	assert.False(t, done)
	require.Len(t, events, 2)
	assert.Equal(t, "propose_plan", events[0].ToolName)
	assert.False(t, events[1].IsError)
}

func TestProcessCodexExecuteEvent_TurnCompletedSetsMetrics(t *testing.T) {
	var content assistantText
	content.WriteString("done")
	var out ExecuteOutput

	done, err := processCodexExecuteEvent(&types.TurnCompleted{
		Status: "completed",
		Usage:  types.TokenUsage{TotalTokens: 42},
	}, &content, &out, nil)
	require.NoError(t, err)
	assert.True(t, done)
	assert.Equal(t, "done", out.Content)
	assert.Equal(t, 42, out.TokensUsed)
	assert.Equal(t, 1, out.NumTurns)
}

func TestProcessCodexExecuteEvent_TurnFailed(t *testing.T) {
	var content assistantText
	var out ExecuteOutput

	done, err := processCodexExecuteEvent(&types.TurnFailed{Message: "worktree gone"}, &content, &out, nil)
	assert.True(t, done)
	require.Error(t, err)
	assert.Contains(t, err.Error(), "worktree gone")
}

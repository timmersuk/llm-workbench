package toolloop

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/sirupsen/logrus"
	"github.com/sirupsen/logrus/hooks/test"

	"github.com/timmersuk/llm-workbench/internal/chat"
)

// fakeClient replays a scripted sequence of assistant turns. Each turn is a
// function that emits deltas (text and/or tool calls) via onDelta, exactly as
// StreamChatCompletion would. It records the messages it was called with so a
// test can assert on the conversation the engine built.
type fakeClient struct {
	turns    []func(onDelta func(chat.Delta) error) error
	call     int
	requests [][]chat.Message
}

func (f *fakeClient) StreamChatCompletion(_ context.Context, req chat.CompletionRequest, onDelta func(chat.Delta) error) error {
	f.requests = append(f.requests, req.Messages)
	turn := f.turns[f.call]
	f.call++
	return turn(onDelta)
}

func textTurn(s string) func(func(chat.Delta) error) error {
	return func(onDelta func(chat.Delta) error) error {
		return onDelta(chat.Delta{Content: s})
	}
}

func toolTurn(calls ...chat.ToolCall) func(func(chat.Delta) error) error {
	return func(onDelta func(chat.Delta) error) error {
		for i := range calls {
			c := calls[i]
			if err := onDelta(chat.Delta{ToolCall: &c}); err != nil {
				return err
			}
		}
		return nil
	}
}

func call(id, name, args string) chat.ToolCall {
	return chat.ToolCall{ID: id, Type: "function", Function: chat.ToolCallFunction{Name: name, Arguments: args}}
}

func baseMessages() []chat.Message {
	return []chat.Message{{Role: "user", Content: "go"}}
}

func TestStopsOnText(t *testing.T) {
	f := &fakeClient{turns: []func(func(chat.Delta) error) error{textTurn("done")}}
	res, err := New(f).Run(context.Background(), Config{MaxTurns: 5}, baseMessages(), nil)
	if err != nil {
		t.Fatal(err)
	}
	if res.Content != "done" || res.Turns != 1 || res.StopCall != nil || res.Exhausted {
		t.Fatalf("unexpected result: %+v", res)
	}
}

func TestExecutesToolThenAnswers(t *testing.T) {
	dir := t.TempDir()
	os.WriteFile(filepath.Join(dir, "a.txt"), []byte("hello\nworld"), 0o644)

	f := &fakeClient{turns: []func(func(chat.Delta) error) error{
		toolTurn(call("c1", "read_file", `{"path":"a.txt"}`)),
		textTurn("the file says hello"),
	}}
	res, err := New(f).Run(context.Background(), Config{
		Workspace: dir, Tools: ReadOnlyTools(), MaxTurns: 5,
	}, baseMessages(), nil)
	if err != nil {
		t.Fatal(err)
	}
	if res.Turns != 2 || res.Content != "the file says hello" {
		t.Fatalf("unexpected result: %+v", res)
	}
	// The second request must carry the tool result so the model could ground
	// its answer: user, assistant(tool_calls), tool, then the model answers.
	second := f.requests[1]
	if len(second) != 3 || second[1].Role != "assistant" || second[2].Role != "tool" {
		t.Fatalf("unexpected conversation shape: %+v", second)
	}
	if !strings.Contains(second[2].Content, "hello") {
		t.Fatalf("tool result not fed back: %q", second[2].Content)
	}
	if len(second[1].ToolCalls) != 1 {
		t.Fatalf("assistant message should record the executed call, got %d", len(second[1].ToolCalls))
	}
}

func TestStopToolTakesPrecedence(t *testing.T) {
	stop := chat.Tool{Type: "function", Function: chat.ToolSchema{Name: "propose_draft"}}
	f := &fakeClient{turns: []func(func(chat.Delta) error) error{
		// Model calls a read tool AND the stop tool in one turn.
		toolTurn(
			call("c1", "read_file", `{"path":"x"}`),
			call("c2", "propose_draft", `{"title":"t"}`),
		),
	}}
	res, err := New(f).Run(context.Background(), Config{
		Tools: ReadOnlyTools(), StopTools: []chat.Tool{stop}, MaxTurns: 5,
	}, baseMessages(), nil)
	if err != nil {
		t.Fatal(err)
	}
	if res.StopCall == nil || res.StopCall.Function.Name != "propose_draft" {
		t.Fatalf("expected stop on propose_draft, got %+v", res)
	}
	if res.Turns != 1 {
		t.Fatalf("should stop on turn 1, got %d", res.Turns)
	}
}

// TestMultipleStopToolsEitherOneStops covers Review's shape
// (docs/milestones/done/milestone9.md): two stop tools offered at once, and the
// model calling the second one in the list still stops the loop.
func TestMultipleStopToolsEitherOneStops(t *testing.T) {
	review := chat.Tool{Type: "function", Function: chat.ToolSchema{Name: "propose_review"}}
	knowledgeTool := chat.Tool{Type: "function", Function: chat.ToolSchema{Name: "propose_knowledge"}}
	f := &fakeClient{turns: []func(func(chat.Delta) error) error{
		toolTurn(call("c1", "propose_knowledge", `{"concept_id":"x"}`)),
	}}
	res, err := New(f).Run(context.Background(), Config{
		StopTools: []chat.Tool{review, knowledgeTool}, MaxTurns: 5,
	}, baseMessages(), nil)
	if err != nil {
		t.Fatal(err)
	}
	if res.StopCall == nil || res.StopCall.Function.Name != "propose_knowledge" {
		t.Fatalf("expected stop on propose_knowledge, got %+v", res)
	}
}

// planStopTool mirrors internal/drafttool's ProposePlan schema closely
// enough to exercise validateStopCall: "steps" is required.
func planStopTool() chat.Tool {
	return chat.Tool{Type: "function", Function: chat.ToolSchema{
		Name:       "propose_plan",
		Parameters: json.RawMessage(`{"type":"object","properties":{"approach":{"type":"string"},"steps":{"type":"array"}},"required":["approach","steps"]}`),
	}}
}

// TestInvalidStopCallRetriesInsteadOfStopping locks in the fix for the
// local-model tool-loop pathology that produced a real corrupted plan.yaml
// in production: a StopTool call missing a schema-required field (here
// "steps") must not end the loop as if it were a valid proposal — the
// error is fed back like any other tool result, and a later, valid retry
// is what actually stops the loop.
func TestInvalidStopCallRetriesInsteadOfStopping(t *testing.T) {
	f := &fakeClient{turns: []func(func(chat.Delta) error) error{
		toolTurn(call("c1", "propose_plan", `{"approach":"missing steps"}`)),
		toolTurn(call("c2", "propose_plan", `{"approach":"fixed","steps":["a"]}`)),
	}}
	res, err := New(f).Run(context.Background(), Config{
		StopTools: []chat.Tool{planStopTool()}, MaxTurns: 5,
	}, baseMessages(), nil)
	if err != nil {
		t.Fatal(err)
	}
	if res.StopCall == nil || res.StopCall.ID != "c2" {
		t.Fatalf("expected the second, valid call to stop the loop, got %+v", res)
	}
	if res.Turns != 2 {
		t.Fatalf("expected 2 turns (one rejected retry), got %d", res.Turns)
	}
	// The rejected call's turn must have fed an error back, the same
	// protocol shape an executed tool call's failure would.
	second := f.requests[1]
	if len(second) != 3 || second[1].Role != "assistant" || second[2].Role != "tool" {
		t.Fatalf("unexpected conversation shape after a rejected stop call: %+v", second)
	}
	if !strings.Contains(second[2].Content, "steps") {
		t.Fatalf("expected the missing-field error fed back, got %q", second[2].Content)
	}
}

// TestInvalidStopCallExhaustsIfNeverCorrected covers a model that never
// self-corrects: the retry loop must still respect MaxTurns rather than
// looping forever.
func TestInvalidStopCallExhaustsIfNeverCorrected(t *testing.T) {
	turns := make([]func(func(chat.Delta) error) error, 3)
	for i := range turns {
		turns[i] = toolTurn(call("c", "propose_plan", `{"approach":"still missing steps"}`))
	}
	f := &fakeClient{turns: turns}
	res, err := New(f).Run(context.Background(), Config{
		StopTools: []chat.Tool{planStopTool()}, MaxTurns: 3,
	}, baseMessages(), nil)
	if err != nil {
		t.Fatal(err)
	}
	if !res.Exhausted || res.StopCall != nil {
		t.Fatalf("expected exhaustion with no stop call, got %+v", res)
	}
}

func TestValidateStopCall(t *testing.T) {
	stop := planStopTool()
	if err := validateStopCall(stop, &chat.ToolCall{Function: chat.ToolCallFunction{Arguments: `{"approach":"a","steps":["x"]}`}}); err != nil {
		t.Fatalf("expected valid args to pass, got %v", err)
	}
	if err := validateStopCall(stop, &chat.ToolCall{Function: chat.ToolCallFunction{Arguments: `{"approach":"a"}`}}); err == nil {
		t.Fatal("expected missing \"steps\" to fail validation")
	}
	// A StopTool with no declared schema is unconstrained.
	noSchema := chat.Tool{Type: "function", Function: chat.ToolSchema{Name: "propose_draft"}}
	if err := validateStopCall(noSchema, &chat.ToolCall{Function: chat.ToolCallFunction{Arguments: `{}`}}); err != nil {
		t.Fatalf("expected a schema-less stop tool to always validate, got %v", err)
	}
}

func TestDuplicateCallsDedupedAndMatched(t *testing.T) {
	dir := t.TempDir()
	os.WriteFile(filepath.Join(dir, "a.txt"), []byte("hi"), 0o644)

	f := &fakeClient{turns: []func(func(chat.Delta) error) error{
		// Same call three times in one turn (the qwen duplicate-call pathology).
		toolTurn(
			call("c1", "read_file", `{"path":"a.txt"}`),
			call("c2", "read_file", `{"path":"a.txt"}`),
			call("c3", "read_file", `{"path":"a.txt"}`),
		),
		textTurn("ok"),
	}}
	_, err := New(f).Run(context.Background(), Config{Workspace: dir, Tools: ReadOnlyTools(), MaxTurns: 5}, baseMessages(), nil)
	if err != nil {
		t.Fatal(err)
	}
	// Assistant message must record exactly one call, with exactly one tool
	// result following — no dangling tool_calls.
	second := f.requests[1]
	var toolMsgs int
	for _, m := range second {
		if m.Role == "tool" {
			toolMsgs++
		}
	}
	assistant := second[1]
	if len(assistant.ToolCalls) != 1 || toolMsgs != 1 {
		t.Fatalf("expected 1 recorded call + 1 tool result, got %d calls / %d results", len(assistant.ToolCalls), toolMsgs)
	}
}

func TestExhaustion(t *testing.T) {
	// A model that calls a tool forever; the turn budget must stop it.
	dir := t.TempDir()
	os.WriteFile(filepath.Join(dir, "a.txt"), []byte("x"), 0o644)
	turns := make([]func(func(chat.Delta) error) error, 10)
	for i := range turns {
		turns[i] = toolTurn(call("c", "read_file", `{"path":"a.txt"}`))
	}
	f := &fakeClient{turns: turns}
	res, err := New(f).Run(context.Background(), Config{Workspace: dir, Tools: ReadOnlyTools(), MaxTurns: 3}, baseMessages(), nil)
	if err != nil {
		t.Fatal(err)
	}
	if !res.Exhausted || res.Turns != 3 {
		t.Fatalf("expected exhaustion at turn 3, got %+v", res)
	}
}

func TestUnknownToolIsRecoverable(t *testing.T) {
	f := &fakeClient{turns: []func(func(chat.Delta) error) error{
		toolTurn(call("c1", "nonexistent", `{}`)),
		textTurn("recovered"),
	}}
	res, err := New(f).Run(context.Background(), Config{Tools: ReadOnlyTools(), MaxTurns: 5}, baseMessages(), nil)
	if err != nil {
		t.Fatalf("unknown tool should not abort the run: %v", err)
	}
	if res.Content != "recovered" {
		t.Fatalf("expected recovery, got %+v", res)
	}
	second := f.requests[1]
	if !strings.Contains(second[len(second)-1].Content, "unknown tool") {
		t.Fatalf("expected an error result fed back, got %q", second[len(second)-1].Content)
	}
}

func TestReadPagingAndConfinement(t *testing.T) {
	dir := t.TempDir()
	var lines []string
	for i := 1; i <= 50; i++ {
		lines = append(lines, "line"+itoa(i))
	}
	os.WriteFile(filepath.Join(dir, "big.txt"), []byte(strings.Join(lines, "\n")), 0o644)

	r := readTool{}
	out, err := r.Execute(context.Background(), dir, `{"path":"big.txt","offset":10,"limit":5}`)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(out, "line10") || !strings.Contains(out, "line14") || strings.Contains(out, "line15") {
		t.Fatalf("paging wrong: %q", out)
	}
	// Path escape must be rejected.
	if _, err := r.Execute(context.Background(), dir, `{"path":"../../etc/passwd"}`); err == nil {
		t.Fatal("expected path-escape rejection")
	}
}

func TestGrepAndGlob(t *testing.T) {
	dir := t.TempDir()
	os.MkdirAll(filepath.Join(dir, "sub"), 0o755)
	os.WriteFile(filepath.Join(dir, "sub", "x.go"), []byte("package x\nfunc Target() {}"), 0o644)
	os.WriteFile(filepath.Join(dir, "y.md"), []byte("# doc\nTarget here"), 0o644)

	g, _ := grepTool{}.Execute(context.Background(), dir, `{"pattern":"Target"}`)
	if !strings.Contains(g, "sub/x.go") || !strings.Contains(g, "y.md") {
		t.Fatalf("grep missed matches: %q", g)
	}

	gl, _ := globTool{}.Execute(context.Background(), dir, `{"pattern":"**/*.go"}`)
	if !strings.Contains(gl, "sub/x.go") || strings.Contains(gl, "y.md") {
		t.Fatalf("glob wrong: %q", gl)
	}
}

func TestOnToolCallAndOnToolResultFire(t *testing.T) {
	dir := t.TempDir()
	os.WriteFile(filepath.Join(dir, "a.txt"), []byte("hello"), 0o644)

	f := &fakeClient{turns: []func(func(chat.Delta) error) error{
		toolTurn(call("c1", "read_file", `{"path":"a.txt"}`)),
		textTurn("done"),
	}}

	var calls []string
	var results []string
	var callIDs, resultIDs []string
	res, err := New(f).Run(context.Background(), Config{
		Workspace: dir, Tools: ReadOnlyTools(), MaxTurns: 5,
		OnToolCall: func(id, name, args string) error {
			calls = append(calls, name+":"+args)
			callIDs = append(callIDs, id)
			return nil
		},
		OnToolResult: func(id, name, result string, isError bool) error {
			results = append(results, fmt.Sprintf("%s:%v:%s", name, isError, result))
			resultIDs = append(resultIDs, id)
			return nil
		},
	}, baseMessages(), nil)
	if err != nil {
		t.Fatal(err)
	}
	if res.Content != "done" {
		t.Fatalf("unexpected: %+v", res)
	}
	if len(calls) != 1 || calls[0] != `read_file:{"path":"a.txt"}` {
		t.Fatalf("OnToolCall not fired correctly: %v", calls)
	}
	if len(results) != 1 || !strings.Contains(results[0], "hello") || strings.Contains(results[0], "true") {
		t.Fatalf("OnToolResult not fired correctly: %v", results)
	}
	if len(callIDs) != 1 || callIDs[0] != "c1" || len(resultIDs) != 1 || resultIDs[0] != "c1" {
		t.Fatalf("expected the call and its result to share the call's real id \"c1\", got callIDs=%v resultIDs=%v", callIDs, resultIDs)
	}
}

// TestToolExecutionIsLogged guards the observability contract: every tool the
// model runs is recorded server-side at the engine's single execution point,
// independent of whether a caller wired the OnToolCall/OnToolResult hooks (Run
// leaves them nil). Without this record the only evidence a review's bash/tests
// ran is the model's own prose.
func TestToolExecutionIsLogged(t *testing.T) {
	dir := t.TempDir()
	os.WriteFile(filepath.Join(dir, "a.txt"), []byte("hello"), 0o644)

	hook := test.NewGlobal()
	defer hook.Reset()
	prev := logrus.GetLevel()
	logrus.SetLevel(logrus.InfoLevel)
	defer logrus.SetLevel(prev)

	f := &fakeClient{turns: []func(func(chat.Delta) error) error{
		toolTurn(call("c1", "read_file", `{"path":"a.txt"}`)),
		textTurn("done"),
	}}
	// No OnToolCall/OnToolResult wired — mirrors ChatClientRunner.Run.
	if _, err := New(f).Run(context.Background(), Config{
		Workspace: dir, Tools: ReadOnlyTools(), MaxTurns: 5,
	}, baseMessages(), nil); err != nil {
		t.Fatal(err)
	}

	var found bool
	for _, e := range hook.AllEntries() {
		if e.Message == "toolloop: executing tool call" && e.Data["tool"] == "read_file" {
			found = true
			if ws, _ := e.Data["workspace"].(string); ws != dir {
				t.Fatalf("expected workspace %q in log entry, got %q", dir, ws)
			}
		}
	}
	if !found {
		t.Fatalf("expected an Info audit log for the executed tool call, entries: %v", hook.AllEntries())
	}
}

func TestOnToolCallErrorAbortsLoop(t *testing.T) {
	f := &fakeClient{turns: []func(func(chat.Delta) error) error{
		toolTurn(call("c1", "read_file", `{"path":"x"}`)),
	}}
	wantErr := errors.New("nope")
	_, err := New(f).Run(context.Background(), Config{
		Tools: ReadOnlyTools(), MaxTurns: 5,
		OnToolCall: func(string, string, string) error { return wantErr },
	}, baseMessages(), nil)
	if !errors.Is(err, wantErr) {
		t.Fatalf("expected OnToolCall error to abort, got %v", err)
	}
}

func TestTokensUsedAccumulatesAcrossTurns(t *testing.T) {
	dir := t.TempDir()
	os.WriteFile(filepath.Join(dir, "a.txt"), []byte("x"), 0o644)

	f := &fakeClient{turns: []func(func(chat.Delta) error) error{
		func(onDelta func(chat.Delta) error) error {
			if err := onDelta(chat.Delta{ToolCall: &chat.ToolCall{ID: "c1", Type: "function", Function: chat.ToolCallFunction{Name: "read_file", Arguments: `{"path":"a.txt"}`}}}); err != nil {
				return err
			}
			return onDelta(chat.Delta{Usage: &chat.Usage{TotalTokens: 10}})
		},
		func(onDelta func(chat.Delta) error) error {
			if err := onDelta(chat.Delta{Content: "done"}); err != nil {
				return err
			}
			return onDelta(chat.Delta{Usage: &chat.Usage{TotalTokens: 15}})
		},
	}}
	res, err := New(f).Run(context.Background(), Config{Workspace: dir, Tools: ReadOnlyTools(), MaxTurns: 5}, baseMessages(), nil)
	if err != nil {
		t.Fatal(err)
	}
	if res.TokensUsed != 25 {
		t.Fatalf("expected summed usage 25, got %d", res.TokensUsed)
	}
}

// Ensure tool specs carry valid JSON-schema parameters (a malformed schema
// would be rejected by the model server).
func TestToolSpecsValid(t *testing.T) {
	for _, tl := range ReadOnlyTools() {
		var schema map[string]any
		if err := json.Unmarshal(tl.Spec().Function.Parameters, &schema); err != nil {
			t.Fatalf("%s has invalid parameter JSON: %v", toolName(tl), err)
		}
	}
}

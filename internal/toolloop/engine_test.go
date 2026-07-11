package toolloop

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"

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
		Tools: ReadOnlyTools(), StopTool: &stop, MaxTurns: 5,
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

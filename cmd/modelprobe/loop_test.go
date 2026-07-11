package main

import (
	"strings"
	"testing"
)

func TestIsSpiral(t *testing.T) {
	spiral := strings.Repeat("Let me just answer it once more. ", 20)
	if !isSpiral(spiral) {
		t.Error("expected repeated-sentence spiral to be detected")
	}
	// The degenerate "definition of definition of…" form seen in a real run.
	if !isSpiral(strings.Repeat("definition of ", 60)) {
		t.Error("expected word-level spiral to be detected")
	}
	normal := "The AgentRunner interface defines Run, Execute, CheckHealth, " +
		"ListModels and CloseSession. It lives in internal/agentrunner/runner.go " +
		"and is implemented by ClaudeRunner, CodexRunner and ChatClientRunner."
	if isSpiral(normal) {
		t.Error("normal prose flagged as a spiral")
	}
	if isSpiral("") {
		t.Error("empty string flagged as a spiral")
	}
}

func TestHasDuplicateCalls(t *testing.T) {
	dup := []toolCall{
		{Function: functionCall{Name: "read_file", Arguments: `{"path":"README.md"}`}},
		{Function: functionCall{Name: "read_file", Arguments: `{"path":"README.md"}`}},
	}
	if !hasDuplicateCalls(dup) {
		t.Error("expected identical repeated calls to be flagged")
	}
	distinct := []toolCall{
		{Function: functionCall{Name: "read_file", Arguments: `{"path":"README.md"}`}},
		{Function: functionCall{Name: "read_file", Arguments: `{"path":"go.mod"}`}},
		{Function: functionCall{Name: "grep_search", Arguments: `{"pattern":"AgentRunner"}`}},
	}
	if hasDuplicateCalls(distinct) {
		t.Error("distinct calls flagged as duplicates")
	}
}

func TestDedupeCalls(t *testing.T) {
	in := []toolCall{
		{Function: functionCall{Name: "read_file", Arguments: `{"path":"a"}`}},
		{Function: functionCall{Name: "read_file", Arguments: `{"path":"a"}`}},
		{Function: functionCall{Name: "read_file", Arguments: `{"path":"b"}`}},
	}
	if got := dedupeCalls(in); len(got) != 2 {
		t.Errorf("expected 2 distinct calls, got %d", len(got))
	}
}

func TestContainsToolXML(t *testing.T) {
	cases := map[string]bool{
		"<tool_call>\n<function=grep_search>":      true,
		"I'll call <function=read_file>{...}":      true,
		"Here is the answer: AgentRunner has 5...": false,
		"The struct uses interface{} as a field":   false, // must not false-positive on Go generics-ish text
	}
	for in, want := range cases {
		if got := containsToolXML(in); got != want {
			t.Errorf("containsToolXML(%q) = %v, want %v", in, got, want)
		}
	}
}

func TestAnnouncesIntent(t *testing.T) {
	if !announcesIntent("Let me search for the interface definition first.") {
		t.Error("expected intent phrase to be detected")
	}
	if !announcesIntent("I'll read runner.go to confirm.") {
		t.Error("expected contraction intent phrase to be detected")
	}
	if announcesIntent("The AgentRunner interface has five methods.") {
		t.Error("a plain answer was flagged as announced intent")
	}
}

package toolloop

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"

	"github.com/sirupsen/logrus"

	"github.com/timmersuk/llm-workbench/internal/chat"
	"github.com/timmersuk/llm-workbench/internal/jsonschema"
)

// completer is the slice of chat.ChatClient the engine needs: a single
// stateless streamed completion. Narrowing to this makes the engine trivial
// to unit-test with a fake and keeps it ignorant of session state (the
// engine holds none — the caller owns the message history).
type completer interface {
	StreamChatCompletion(ctx context.Context, req chat.CompletionRequest, onDelta func(chat.Delta) error) error
}

// Engine drives a bounded tool-call loop over a chat completion client. It is
// stateless: every call takes the full message list in and returns a Result;
// the caller owns conversation history.
type Engine struct {
	client completer
}

// New returns an Engine backed by client (a chat.ChatClient satisfies the
// narrow completer interface).
func New(client completer) *Engine {
	return &Engine{client: client}
}

// Config parameterizes one loop instantiation. Run and Execute differ only in
// these values, per docs/adr/0009-shared-tool-loop-engine-for-run-and-execute.md.
type Config struct {
	Model           string
	ReasoningEffort string
	// Workspace is the resolved, confinement-checked root every tool operates
	// within (ResolveWorkspace for Run, ResolveExecutionWorkspace for Execute).
	Workspace string
	// Tools the engine executes during the loop (ReadOnlyTools for Run; the
	// write-enabled set for Execute). May be empty — then the loop is a single
	// completion, preserving the plain-chat path for callers with no workspace.
	Tools []Tool
	// StopTools, if non-empty, are offered to the model but never executed:
	// when the model calls any one of them, the loop stops and returns that
	// call as Result.StopCall. Run uses this for the Draft-proposing tool(s)
	// — more than one only when a stage offers a human more than one kind of
	// proposal at once (e.g. Review's propose_review alongside
	// propose_knowledge, docs/milestones/done/milestone9.md) — Execute leaves it
	// empty and stops only when the model finishes without a tool call.
	StopTools []chat.Tool
	// MaxTurns bounds tool-call round-trips; reaching it returns Exhausted.
	// Zero (or negative) means no turn-count limit — the caller is relying
	// on ctx's own deadline instead.
	MaxTurns int
	// MaxTokens caps each response so a spiralling model can't generate
	// without limit. Zero uses the provider default (not recommended for
	// local models).
	MaxTokens int
	// MaxToolCallsPerTurn caps how many tool calls one assistant turn may
	// execute, after de-duplication, bounding a model that emits the same or
	// many calls at once. Zero applies defaultMaxToolCallsPerTurn.
	MaxToolCallsPerTurn int
	// OnToolCall, if set, is invoked just before each de-duplicated,
	// executed call runs — both Run and Execute (agentrunner.ChatClientRunner)
	// wire this through to surface intermediate tool activity live. An error
	// aborts the loop, matching onDelta's contract. id is call.ID, the same
	// per-call correlation key threaded through agentrunner.RunInput.OnToolCall/
	// ExecuteEvent.ID — a call and its result share it. Unlike a provider that
	// can declare several calls before returning any of their results (see
	// agentrunner.RunInput.OnToolCall's doc comment), this loop always
	// executes one call and reports its result before moving to the next, so
	// id is never actually needed for correlation here — it's threaded
	// through purely because the callback signature is shared across every
	// caller of RunInput.OnToolCall/OnToolResult.
	OnToolCall func(id, name, argumentsJSON string) error
	// OnToolResult mirrors OnToolCall for the result of executing that call.
	OnToolResult func(id, name, result string, isError bool) error
}

const defaultMaxToolCallsPerTurn = 8

// Result is the outcome of a loop.
type Result struct {
	// Content is the assistant's final text (the last turn's text when the
	// loop stopped on text, or the partial text of the turn that called
	// StopTool / exhausted the turn budget).
	Content string
	// StopCall is the StopTool call the model made, if the loop stopped for
	// that reason; nil otherwise.
	StopCall *chat.ToolCall
	// Turns is how many model turns ran.
	Turns int
	// Exhausted is true when the loop hit MaxTurns without a natural stop.
	// The caller decides how to surface this (Run degrades gracefully with
	// whatever Content accumulated; Execute treats it as a failure).
	Exhausted bool
	// TokensUsed sums every turn's completion usage (chat.Delta.Usage,
	// requires internal/chat's stream_options.include_usage). Zero if the
	// upstream server never sent a usage chunk.
	TokensUsed int
}

// Run drives the loop. messages is the full starting conversation (the caller
// includes the system prompt and any history); the engine appends assistant
// tool-call turns and tool-result turns to a copy as it iterates. Content and
// reasoning deltas are forwarded to onDelta as they stream so a UI can render
// them live; onDelta may be nil.
func (e *Engine) Run(ctx context.Context, cfg Config, messages []chat.Message, onDelta func(chat.Delta) error) (Result, error) {
	specs := toolSpecs(cfg.Tools, cfg.StopTools)
	byName := toolsByName(cfg.Tools)
	maxCalls := cfg.MaxToolCallsPerTurn
	if maxCalls <= 0 {
		maxCalls = defaultMaxToolCallsPerTurn
	}

	initialCap := len(messages)
	if cfg.MaxTurns > 0 {
		initialCap += 2 * cfg.MaxTurns
	}
	msgs := make([]chat.Message, 0, initialCap)
	msgs = append(msgs, messages...)

	var lastText string
	var totalTokens int
turnLoop:
	for turn := 1; cfg.MaxTurns <= 0 || turn <= cfg.MaxTurns; turn++ {
		var text strings.Builder
		var calls []chat.ToolCall

		err := e.client.StreamChatCompletion(ctx, chat.CompletionRequest{
			Model:           cfg.Model,
			ReasoningEffort: cfg.ReasoningEffort,
			Messages:        msgs,
			Tools:           specs,
			MaxTokens:       cfg.MaxTokens,
		}, func(d chat.Delta) error {
			if d.Usage != nil {
				totalTokens += d.Usage.TotalTokens
				return nil
			}
			if d.ToolCall != nil {
				calls = append(calls, *d.ToolCall)
				return nil
			}
			text.WriteString(d.Content)
			if onDelta != nil {
				return onDelta(d)
			}
			return nil
		})
		if err != nil {
			return Result{Content: text.String(), Turns: turn, TokensUsed: totalTokens}, err
		}
		lastText = text.String()

		// No tool calls: the model answered. Terminal for both instantiations.
		if len(calls) == 0 {
			return Result{Content: lastText, Turns: turn, TokensUsed: totalTokens}, nil
		}

		// A StopTool takes precedence: if the model proposed a Draft (or
		// whatever stop tool is configured), that ends the loop even if it
		// also called a read tool in the same turn. Checked in cfg.StopTools'
		// order; a model that somehow calls more than one stop tool in the
		// same turn resolves to whichever is listed first.
		for _, stop := range cfg.StopTools {
			sc := findCall(calls, stop.Function.Name)
			if sc == nil {
				continue
			}
			// A schema-invalid call (e.g. a required field the model's JSON
			// generation dropped mid-value — the local-model tool-loop
			// pathology this guards against) isn't trusted as a real stop:
			// feed the error back like any other executed tool call and let
			// the model retry, bounded by the same MaxTurns as everything
			// else, rather than ending the loop with a malformed proposal.
			if err := validateStopCall(stop, sc); err != nil {
				msgs = append(msgs, chat.Message{Role: "assistant", Content: lastText, ToolCalls: []chat.ToolCall{*sc}})
				msgs = append(msgs, chat.Message{Role: "tool", ToolCallID: sc.ID, Content: "error: " + err.Error()})
				continue turnLoop
			}
			return Result{Content: lastText, StopCall: sc, Turns: turn, TokensUsed: totalTokens}, nil
		}

		// Otherwise execute the tool calls and feed the results back. The
		// assistant message records exactly the calls we respond to (the
		// de-duplicated, capped set), so every tool_call has a matching
		// tool result — a protocol requirement OpenAI-compatible servers
		// enforce — while still bounding a model that over-emits.
		executed := capCalls(dedupeCalls(calls), maxCalls)
		msgs = append(msgs, chat.Message{Role: "assistant", Content: lastText, ToolCalls: executed})
		for _, call := range executed {
			if cfg.OnToolCall != nil {
				if err := cfg.OnToolCall(call.ID, call.Function.Name, call.Function.Arguments); err != nil {
					return Result{Content: lastText, Turns: turn, TokensUsed: totalTokens}, err
				}
			}
			result, isError := executeCall(ctx, byName, cfg.Workspace, call)
			if cfg.OnToolResult != nil {
				if err := cfg.OnToolResult(call.ID, call.Function.Name, result, isError); err != nil {
					return Result{Content: lastText, Turns: turn, TokensUsed: totalTokens}, err
				}
			}
			msgs = append(msgs, chat.Message{Role: "tool", ToolCallID: call.ID, Content: result})
		}
	}

	// Hit the turn budget without a natural stop.
	return Result{Content: lastText, Turns: cfg.MaxTurns, Exhausted: true, TokensUsed: totalTokens}, nil
}

// validateStopCall checks a StopTool call's arguments against that tool's
// own declared JSON Schema (stop.Function.Parameters). A StopTool with no
// schema (nil/empty Parameters, as several callers' hand-built chat.Tool
// values in tests do) is unconstrained and always valid — schema
// enforcement is opt-in via whatever the caller actually declared.
func validateStopCall(stop chat.Tool, call *chat.ToolCall) error {
	if len(stop.Function.Parameters) == 0 {
		return nil
	}
	var schema map[string]any
	if err := json.Unmarshal(stop.Function.Parameters, &schema); err != nil {
		return nil // an unparsable schema can't be enforced; fail open
	}
	var args map[string]any
	if err := json.Unmarshal([]byte(call.Function.Arguments), &args); err != nil {
		return fmt.Errorf("arguments are not valid JSON: %w", err)
	}
	return jsonschema.RequiredFieldsPresent(schema, args)
}

// executeCall dispatches one tool call, returning result text and whether it
// represents an error. A call to an unknown tool or a tool that errors comes
// back as an error string the model can read and recover from — never a Go
// error that aborts the loop.
func executeCall(ctx context.Context, byName map[string]Tool, workspace string, call chat.ToolCall) (result string, isError bool) {
	name := call.Function.Name
	// Log every tool the model actually runs at the one point every caller's
	// tool calls funnel through, so there is an independent server-side record
	// of what a Run or Execute did — a bash `go test`, a read, a grep — rather
	// than only the model's own prose claiming it (the "no hidden state"
	// invariant). The OnToolCall/OnToolResult hooks stay a caller concern for
	// surfacing UI events; this is the ops record, on by default.
	logrus.WithFields(logrus.Fields{
		"tool": name, "workspace": workspace, "args": logPreview(call.Function.Arguments),
	}).Info("toolloop: executing tool call")

	tool, ok := byName[name]
	if !ok {
		logrus.WithField("tool", name).Warn("toolloop: model called an unknown tool")
		return "error: unknown tool " + name, true
	}
	out, err := tool.Execute(ctx, workspace, call.Function.Arguments)
	if err != nil {
		logrus.WithFields(logrus.Fields{"tool": name, "error": err.Error()}).Warn("toolloop: tool call failed")
		return "error: " + err.Error(), true
	}
	logrus.WithFields(logrus.Fields{"tool": name, "result": logPreview(out)}).Debug("toolloop: tool call result")
	return out, false
}

// logPreview flattens and trims a tool's arguments or result to a single short
// line for structured logging — enough to see what ran without spilling a full
// file read or command output into the logs (truncateResult already bounds
// what the model sees; this is a much tighter cap for humans).
func logPreview(s string) string {
	const max = 200
	s = strings.ReplaceAll(strings.ReplaceAll(s, "\n", " "), "\r", "")
	if len(s) > max {
		return s[:max] + "…"
	}
	return s
}

// toolSpecs assembles the OpenAI-compatible tool declarations offered to the
// model: the executable tools plus the non-executed StopTools, if any.
func toolSpecs(tools []Tool, stopTools []chat.Tool) []chat.Tool {
	if len(tools) == 0 && len(stopTools) == 0 {
		return nil
	}
	specs := make([]chat.Tool, 0, len(tools)+len(stopTools))
	for _, t := range tools {
		specs = append(specs, t.Spec())
	}
	specs = append(specs, stopTools...)
	return specs
}

func toolsByName(tools []Tool) map[string]Tool {
	m := make(map[string]Tool, len(tools))
	for _, t := range tools {
		m[toolName(t)] = t
	}
	return m
}

func findCall(calls []chat.ToolCall, name string) *chat.ToolCall {
	for i := range calls {
		if calls[i].Function.Name == name {
			return &calls[i]
		}
	}
	return nil
}

// dedupeCalls drops exact-duplicate (name, arguments) calls within one turn —
// the duplicate-call pathology the Phase 0 spike saw local models emit (the
// same read_file requested several times at once).
func dedupeCalls(calls []chat.ToolCall) []chat.ToolCall {
	seen := make(map[string]bool, len(calls))
	out := make([]chat.ToolCall, 0, len(calls))
	for _, c := range calls {
		key := c.Function.Name + "\x00" + c.Function.Arguments
		if seen[key] {
			continue
		}
		seen[key] = true
		out = append(out, c)
	}
	return out
}

func capCalls(calls []chat.ToolCall, max int) []chat.ToolCall {
	if len(calls) > max {
		return calls[:max]
	}
	return calls
}

package toolloop

import (
	"context"
	"strings"

	"github.com/sirupsen/logrus"

	"github.com/timmersuk/llm-workbench/internal/chat"
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
	Model string
	// Workspace is the resolved, confinement-checked root every tool operates
	// within (ResolveWorkspace for Run, ResolveExecutionWorkspace for Execute).
	Workspace string
	// Tools the engine executes during the loop (ReadOnlyTools for Run; the
	// write-enabled set for Execute). May be empty — then the loop is a single
	// completion, preserving the plain-chat path for callers with no workspace.
	Tools []Tool
	// StopTool, if set, is offered to the model but never executed: when the
	// model calls it, the loop stops and returns that call as Result.StopCall.
	// Run uses this for the Draft-proposing tool; Execute leaves it nil and
	// stops only when the model finishes without a tool call.
	StopTool *chat.Tool
	// MaxTurns bounds tool-call round-trips (claudeRunnerMaxTurns for Run,
	// claudeExecutionMaxTurns for Execute). Reaching it returns Exhausted.
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
	// executed call runs. Execute uses this to surface every real action as
	// an ExecuteEvent; Run leaves it nil since RunOutput only surfaces text
	// and the final StopCall. An error aborts the loop, matching onDelta's
	// contract.
	OnToolCall func(name, argumentsJSON string) error
	// OnToolResult mirrors OnToolCall for the result of executing that call.
	OnToolResult func(name, result string, isError bool) error
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
	specs := toolSpecs(cfg.Tools, cfg.StopTool)
	byName := toolsByName(cfg.Tools)
	maxCalls := cfg.MaxToolCallsPerTurn
	if maxCalls <= 0 {
		maxCalls = defaultMaxToolCallsPerTurn
	}

	msgs := make([]chat.Message, 0, len(messages)+2*cfg.MaxTurns)
	msgs = append(msgs, messages...)

	var lastText string
	var totalTokens int
	for turn := 1; turn <= cfg.MaxTurns; turn++ {
		var text strings.Builder
		var calls []chat.ToolCall

		err := e.client.StreamChatCompletion(ctx, chat.CompletionRequest{
			Model:     cfg.Model,
			Messages:  msgs,
			Tools:     specs,
			MaxTokens: cfg.MaxTokens,
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

		// StopTool takes precedence: if the model proposed the Draft (or
		// whatever stop tool is configured), that ends the loop even if it
		// also called a read tool in the same turn.
		if cfg.StopTool != nil {
			if sc := findCall(calls, cfg.StopTool.Function.Name); sc != nil {
				return Result{Content: lastText, StopCall: sc, Turns: turn, TokensUsed: totalTokens}, nil
			}
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
				if err := cfg.OnToolCall(call.Function.Name, call.Function.Arguments); err != nil {
					return Result{Content: lastText, Turns: turn, TokensUsed: totalTokens}, err
				}
			}
			result, isError := executeCall(ctx, byName, cfg.Workspace, call)
			if cfg.OnToolResult != nil {
				if err := cfg.OnToolResult(call.Function.Name, result, isError); err != nil {
					return Result{Content: lastText, Turns: turn, TokensUsed: totalTokens}, err
				}
			}
			msgs = append(msgs, chat.Message{Role: "tool", ToolCallID: call.ID, Content: result})
		}
	}

	// Hit the turn budget without a natural stop.
	return Result{Content: lastText, Turns: cfg.MaxTurns, Exhausted: true, TokensUsed: totalTokens}, nil
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
// model: the executable tools plus the non-executed StopTool, if any.
func toolSpecs(tools []Tool, stop *chat.Tool) []chat.Tool {
	if len(tools) == 0 && stop == nil {
		return nil
	}
	specs := make([]chat.Tool, 0, len(tools)+1)
	for _, t := range tools {
		specs = append(specs, t.Spec())
	}
	if stop != nil {
		specs = append(specs, *stop)
	}
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

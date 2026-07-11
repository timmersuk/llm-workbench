package main

import (
	"context"
	"fmt"
	"strings"
	"time"
)

// checkStatus is a tri-state for a wire check.
type checkStatus string

const (
	statusPass checkStatus = "PASS"
	statusWarn checkStatus = "WARN"
	statusFail checkStatus = "FAIL"
	statusSkip checkStatus = "SKIP"
)

type checkResult struct {
	name   string
	status checkStatus
	detail string
}

// reasoningProbe reports which reasoning key(s) a model populates, so the
// engine's client knows to parse `reasoning` (gpt-oss) as well as
// `reasoning_content` (qwen/deepseek). Returned as a detail string.
type wireFindings struct {
	reasoningKey string // "reasoning_content", "reasoning", "both", or "none"
	servedModel  string // the model id the server actually echoed back
	checks       []checkResult
}

var readmeSchema = toolDefs() // read_file + grep_search

// runWireChecks executes the single-request diagnostics that don't need a
// full loop: liveness, reasoning-key shape, structured tool emission
// (non-streaming and streaming), and a tool-result round-trip.
func runWireChecks(ctx context.Context, c *client, model string, maxTokens int, testToolChoice bool) wireFindings {
	f := wireFindings{reasoningKey: "none"}

	// 1. Liveness — a trivial completion must return non-empty text quickly.
	{
		resp, _, err := c.complete(ctx, completionRequest{
			Model:     model,
			Messages:  []message{{Role: "user", Content: "Reply with exactly the word: ready"}},
			MaxTokens: maxTokens,
		})
		switch {
		case err != nil:
			f.checks = append(f.checks, checkResult{"liveness", statusFail, err.Error()})
		case len(resp.Choices) == 0 || strings.TrimSpace(resp.Choices[0].Message.Content) == "":
			f.servedModel = resp.Model
			f.checks = append(f.checks, checkResult{"liveness", statusWarn, "empty content (model may have replied only in a reasoning channel)"})
		default:
			f.servedModel = resp.Model
			f.checks = append(f.checks, checkResult{"liveness", statusPass, snippet([]byte(resp.Choices[0].Message.Content), 40)})
		}
	}

	// 2. Structured tool call (non-streaming) + reasoning-key detection.
	{
		resp, _, err := c.complete(ctx, completionRequest{
			Model:     model,
			Messages:  toolPromptMessages(),
			Tools:     readmeSchema,
			MaxTokens: maxTokens,
		})
		if err != nil {
			f.checks = append(f.checks, checkResult{"tool-call (non-stream)", statusFail, err.Error()})
		} else if len(resp.Choices) == 0 {
			f.checks = append(f.checks, checkResult{"tool-call (non-stream)", statusFail, "no choices"})
		} else {
			m := resp.Choices[0].Message
			f.reasoningKey = detectReasoningKey(m.Reasoning, m.ReasoningContent)
			switch {
			case len(m.ToolCalls) == 0:
				extra := ""
				if containsToolXML(m.Content) || containsToolXML(m.Reasoning+m.ReasoningContent) {
					extra = " — emitted tool-call XML as text instead"
				}
				f.checks = append(f.checks, checkResult{"tool-call (non-stream)", statusFail, "no structured tool_calls" + extra})
			case !firstCallIs(m.ToolCalls, "read_file"):
				f.checks = append(f.checks, checkResult{"tool-call (non-stream)", statusWarn, "called " + m.ToolCalls[0].Function.Name + ", expected read_file"})
			default:
				dup := ""
				if hasDuplicateCalls(m.ToolCalls) {
					dup = fmt.Sprintf(" — WARNING: %d calls, duplicates present", len(m.ToolCalls))
				}
				f.checks = append(f.checks, checkResult{"tool-call (non-stream)", statusPass, m.ToolCalls[0].Function.Arguments + dup})
			}
		}
	}

	// 3. Structured tool call (streaming) — deltas must accumulate to a call.
	{
		obs, err := c.stream(ctx, completionRequest{
			Model:     model,
			Messages:  toolPromptMessages(),
			Tools:     readmeSchema,
			MaxTokens: maxTokens,
		})
		switch {
		case err != nil:
			f.checks = append(f.checks, checkResult{"tool-call (stream)", statusFail, err.Error()})
		case !obs.sawToolDelta:
			f.checks = append(f.checks, checkResult{"tool-call (stream)", statusFail, fmt.Sprintf("%d chunks, no tool-call deltas", obs.chunks)})
		default:
			first := obs.toolCalls[obs.toolCallOrder[0]]
			ok := first.Function.Name != "" && isCompleteJSON(first.Function.Arguments)
			if !ok {
				f.checks = append(f.checks, checkResult{"tool-call (stream)", statusWarn, "deltas did not accumulate to a complete call: " + snippet([]byte(first.Function.Arguments), 60)})
			} else {
				f.checks = append(f.checks, checkResult{"tool-call (stream)", statusPass, fmt.Sprintf("%s over %d chunks", first.Function.Name, obs.chunks)})
			}
		}
	}

	// 4. Tool-result round-trip — pose a question the injected tool result
	//    answers, then feed that result back and expect the model to use it.
	{
		msgs := []message{
			{Role: "system", Content: "You are a coding assistant. Call read_file when you need file contents."},
			{Role: "user", Content: "What port is configured in config.yaml?"},
			{Role: "assistant", ToolCalls: []toolCall{{
				ID: "probe_call_1", Type: "function",
				Function: functionCall{Name: "read_file", Arguments: `{"path":"config.yaml"}`},
			}}},
			{Role: "tool", ToolCallID: "probe_call_1", Content: "server:\n  port: 8437\n"},
		}
		resp, _, err := c.complete(ctx, completionRequest{Model: model, Messages: msgs, Tools: readmeSchema, MaxTokens: maxTokens})
		switch {
		case err != nil:
			f.checks = append(f.checks, checkResult{"tool round-trip", statusFail, err.Error()})
		case len(resp.Choices) == 0:
			f.checks = append(f.checks, checkResult{"tool round-trip", statusFail, "no choices"})
		case strings.Contains(resp.Choices[0].Message.Content, "8437"):
			f.checks = append(f.checks, checkResult{"tool round-trip", statusPass, "used tool result in answer"})
		default:
			f.checks = append(f.checks, checkResult{"tool round-trip", statusWarn, "answer did not reference the tool result"})
		}
	}

	// 5. tool_choice:"required" tolerance — OFF by default: some servers
	//    (LM Studio + certain templates) hang indefinitely on it, so a short
	//    timeout distinguishes support from hang without wedging the box.
	if testToolChoice {
		short := &client{baseURL: c.baseURL, apiKey: c.apiKey, http: c.http, timeout: 30 * time.Second}
		_, _, err := short.complete(ctx, completionRequest{
			Model:      model,
			Messages:   toolPromptMessages(),
			Tools:      readmeSchema,
			ToolChoice: "required",
			MaxTokens:  maxTokens,
		})
		if err != nil {
			f.checks = append(f.checks, checkResult{"tool_choice=required", statusWarn, "not supported / hangs: " + snippet([]byte(err.Error()), 60)})
		} else {
			f.checks = append(f.checks, checkResult{"tool_choice=required", statusPass, "accepted"})
		}
	} else {
		f.checks = append(f.checks, checkResult{"tool_choice=required", statusSkip, "off by default (can hang some servers); -tool-choice to enable"})
	}

	return f
}

func toolPromptMessages() []message {
	return []message{
		{Role: "system", Content: "You are a coding assistant. Call read_file when you need file contents."},
		{Role: "user", Content: "Read the file README.md and summarize it."},
	}
}

func detectReasoningKey(reasoning, reasoningContent string) string {
	r := strings.TrimSpace(reasoning) != ""
	rc := strings.TrimSpace(reasoningContent) != ""
	switch {
	case r && rc:
		return "both"
	case r:
		return "reasoning"
	case rc:
		return "reasoning_content"
	default:
		return "none"
	}
}

func firstCallIs(calls []toolCall, name string) bool {
	return len(calls) > 0 && calls[0].Function.Name == name
}

// isCompleteJSON is a cheap balanced-brace check — enough to tell a fully
// accumulated argument object from a truncated stream.
func isCompleteJSON(s string) bool {
	s = strings.TrimSpace(s)
	if !strings.HasPrefix(s, "{") || !strings.HasSuffix(s, "}") {
		return false
	}
	depth := 0
	for _, r := range s {
		switch r {
		case '{':
			depth++
		case '}':
			depth--
		}
	}
	return depth == 0
}

package main

import (
	"context"
	"fmt"
	"strings"
	"time"
)

// runResult records what one loop iteration did and which pathologies it
// exhibited. A run can succeed while still exhibiting a pathology (e.g. a
// duplicate call the loop deduped past), so pathology flags are independent
// of ok.
type runResult struct {
	ok      bool          // produced the grounded answer
	turns   int           // model turns taken
	dur     time.Duration //
	dupCall bool          // emitted the same (name,args) twice in one turn
	spiral  bool          // repeated an n-gram past the spiral threshold
	toolXML bool          // wrote tool-call XML into content/reasoning text
	stall   bool          // ended with an intent-to-call but no call, ungrounded
	errText string        // transport/protocol error
}

// runLoop drives one bounded read/grep loop for the grounding question and
// classifies the outcome. maxTokens caps each generation so a pathological
// model cannot run unbounded.
func runLoop(ctx context.Context, c *client, model, root, question, expect string, maxTurns, maxTokens int) runResult {
	start := time.Now()
	res := runResult{}

	msgs := []message{
		{Role: "system", Content: "You are a code exploration assistant. Use the read_file and grep_search tools to ground every answer in the repository's actual contents. Keep answers short."},
		{Role: "user", Content: question},
	}
	tools := toolDefs()

	for turn := 0; turn < maxTurns; turn++ {
		res.turns = turn + 1
		resp, _, err := c.complete(ctx, completionRequest{
			Model:     model,
			Messages:  msgs,
			Tools:     tools,
			MaxTokens: maxTokens,
		})
		if err != nil {
			res.errText = err.Error()
			res.dur = time.Since(start)
			return res
		}
		if len(resp.Choices) == 0 {
			res.errText = "no choices in response"
			res.dur = time.Since(start)
			return res
		}
		m := resp.Choices[0].Message
		reasoning := m.Reasoning + m.ReasoningContent

		// Pathology: tool-call XML written as text instead of a structured
		// tool_calls entry (the qwen thinking-mode failure).
		if containsToolXML(m.Content) || containsToolXML(reasoning) {
			res.toolXML = true
		}
		// Pathology: repetition spiral in either channel.
		if isSpiral(m.Content) || isSpiral(reasoning) {
			res.spiral = true
		}

		if len(m.ToolCalls) > 0 {
			// Pathology: duplicate identical calls in a single turn.
			if hasDuplicateCalls(m.ToolCalls) {
				res.dupCall = true
			}
			// Record the assistant turn, then answer each tool call.
			msgs = append(msgs, message{Role: "assistant", ToolCalls: m.ToolCalls})
			for _, tc := range dedupeCalls(m.ToolCalls) {
				out := dispatchTool(root, tc)
				msgs = append(msgs, message{Role: "tool", ToolCallID: tc.ID, Content: out})
			}
			continue
		}

		// No tool call: this is the model's final turn.
		if strings.Contains(normalize(m.Content), normalize(expect)) {
			res.ok = true
		} else if announcesIntent(m.Content) || announcesIntent(reasoning) {
			// Said it would act, then stopped without acting or answering.
			res.stall = true
		}
		res.dur = time.Since(start)
		return res
	}

	// Exhausted turns without a final answer.
	res.dur = time.Since(start)
	return res
}

// hasDuplicateCalls reports whether any (name, arguments) pair repeats within
// a single assistant message.
func hasDuplicateCalls(calls []toolCall) bool {
	seen := map[string]bool{}
	for _, tc := range calls {
		key := tc.Function.Name + "\x00" + tc.Function.Arguments
		if seen[key] {
			return true
		}
		seen[key] = true
	}
	return false
}

// dedupeCalls drops exact-duplicate calls so the loop executes each distinct
// call once — the engine will do the same, so the probe measures behavior
// under that guard rather than amplifying the pathology.
func dedupeCalls(calls []toolCall) []toolCall {
	seen := map[string]bool{}
	out := make([]toolCall, 0, len(calls))
	for _, tc := range calls {
		key := tc.Function.Name + "\x00" + tc.Function.Arguments
		if seen[key] {
			continue
		}
		seen[key] = true
		out = append(out, tc)
	}
	return out
}

func containsToolXML(s string) bool {
	return strings.Contains(s, "<tool_call>") || strings.Contains(s, "<function=") ||
		strings.Contains(s, "<tool▁call>") // deepseek-style variant
}

// announcesIntent detects "I'll read...", "let me search...", i.e. narrated
// intent to use a tool without an actual structured call.
func announcesIntent(s string) bool {
	l := strings.ToLower(s)
	for _, p := range []string{"let me ", "i'll ", "i will ", "next, i", "let's ", "i need to read", "i should"} {
		if strings.Contains(l, p) {
			return true
		}
	}
	return false
}

// isSpiral flags degenerate repetition: the most frequent 5-word window
// occurs at least spiralThreshold times.
func isSpiral(s string) bool {
	const spiralThreshold = 8
	const window = 5
	fields := strings.Fields(strings.ToLower(s))
	if len(fields) < window*spiralThreshold {
		return false
	}
	counts := map[string]int{}
	for i := 0; i+window <= len(fields); i++ {
		gram := strings.Join(fields[i:i+window], " ")
		counts[gram]++
		if counts[gram] >= spiralThreshold {
			return true
		}
	}
	return false
}

func normalize(s string) string {
	return strings.Join(strings.Fields(strings.ToLower(s)), " ")
}

// classify renders a one-word headline outcome for a run (for the per-run log
// line). The report tallies pathologies independently.
func (r runResult) classify() string {
	switch {
	case r.errText != "":
		return "ERROR"
	case r.ok:
		return "OK"
	case r.spiral:
		return "SPIRAL"
	case r.toolXML:
		return "TOOL-XML"
	case r.stall:
		return "STALL"
	default:
		return "UNGROUNDED"
	}
}

func (r runResult) String() string {
	tag := r.classify()
	extra := []string{}
	if r.dupCall {
		extra = append(extra, "dup-calls")
	}
	if r.spiral && tag != "SPIRAL" {
		extra = append(extra, "spiral")
	}
	if r.toolXML && tag != "TOOL-XML" {
		extra = append(extra, "tool-xml")
	}
	suffix := ""
	if len(extra) > 0 {
		suffix = " [" + strings.Join(extra, ",") + "]"
	}
	detail := ""
	if r.errText != "" {
		detail = ": " + snippet([]byte(r.errText), 80)
	}
	return fmt.Sprintf("%-10s turns=%d %s%s%s", tag, r.turns, r.dur.Round(time.Millisecond), suffix, detail)
}

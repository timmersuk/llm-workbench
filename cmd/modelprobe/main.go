// Command modelprobe qualifies an OpenAI-compatible model/endpoint for use
// as a workbench tool-loop executor. It runs single-request wire checks
// (liveness, reasoning-key shape, structured tool-call emission streaming and
// not, tool-result round-trip) and an N-run agentic loop that tallies loop
// reliability and detects the local-model pathologies the Milestone 8 Phase 0
// spike characterized: duplicate tool calls, repetition spirals, tool-call
// XML emitted as text, and announce-but-stall turns.
//
// It is deliberately self-contained (stdlib + internal/utils only, no eino/
// dive/internal-chat dependency) so it can bound generations with max_tokens
// and inspect raw wire shape. Milestone 8 PR 2 refactors it onto the shared
// internal/toolloop engine as that engine's first consumer.
package main

import (
	"context"
	"flag"
	"fmt"
	"os"
	"strings"
	"time"

	"github.com/timmersuk/llm-workbench/internal/utils"
)

func main() {
	var (
		baseURL   = flag.String("base-url", utils.GetEnvDefault("LLM_BASE_URL", "http://localhost:11434/v1"), "OpenAI-compatible endpoint base URL")
		apiKey    = flag.String("api-key", utils.GetEnvDefault("LLM_API_KEY", ""), "API key (optional for local servers)")
		model     = flag.String("model", utils.GetEnvDefault("LLM_MODEL", ""), "model id to probe (required)")
		dir       = flag.String("dir", ".", "target repository directory for the loop's read/grep tools")
		groundEnv = flag.String("ground-file", "", "file the loop asks the model to read for the grounding test (default: auto-pick)")
		question  = flag.String("question", "", "override the loop question (default: derived read-and-quote grounding test)")
		expect    = flag.String("expect", "", "override the success substring the final answer must contain (default: derived)")
		runs      = flag.Int("runs", 5, "number of loop iterations for the reliability tally")
		maxTurns  = flag.Int("max-turns", 12, "max model turns per loop run")
		maxTokens = flag.Int("max-tokens", 2048, "per-request max_tokens ceiling")
		timeout   = flag.Duration("timeout", 5*time.Minute, "per-request timeout")
		toolChoc  = flag.Bool("tool-choice", false, "also test tool_choice=required (can hang some servers)")
	)
	flag.Parse()

	if *model == "" {
		fmt.Fprintln(os.Stderr, "error: -model (or LLM_MODEL) is required")
		flag.Usage()
		os.Exit(2)
	}

	q, e, gerr := groundingTest(*dir, *groundEnv, *question, *expect)
	if gerr != nil {
		fmt.Fprintln(os.Stderr, "error: could not set up grounding test:", gerr)
		os.Exit(1)
	}

	c := newClient(*baseURL, *apiKey, *timeout)
	ctx := context.Background()

	fmt.Printf("modelprobe — %s @ %s\n", *model, *baseURL)
	fmt.Printf("loop target: %s | grounding: answer must contain %q\n\n", *dir, truncateLabel(e, 60))

	// --- Wire checks ---
	fmt.Println("WIRE CHECKS")
	wf := runWireChecks(ctx, c, *model, *maxTokens, *toolChoc)
	for _, ck := range wf.checks {
		fmt.Printf("  %-6s %-24s %s\n", ck.status, ck.name, ck.detail)
	}
	if served := strings.TrimSpace(wf.servedModel); served != "" && served != *model {
		fmt.Printf("  %-6s %-24s requested %q but server served %q — results are for the served model\n",
			"WARN", "served model", *model, served)
	}
	fmt.Printf("  %-6s %-24s %s\n", "INFO", "reasoning key", reasoningKeyNote(wf.reasoningKey))
	fmt.Println()

	// --- Loop reliability ---
	fmt.Printf("LOOP RELIABILITY (%d runs)\n", *runs)
	var (
		ok, dup, spiral, toolXML, stall, errs int
		totalDur                              time.Duration
	)
	for i := 0; i < *runs; i++ {
		r := runLoop(ctx, c, *model, *dir, q, e, *maxTurns, *maxTokens)
		fmt.Printf("  run %d/%d: %s\n", i+1, *runs, r)
		if r.ok {
			ok++
		}
		if r.dupCall {
			dup++
		}
		if r.spiral {
			spiral++
		}
		if r.toolXML {
			toolXML++
		}
		if r.stall {
			stall++
		}
		if r.errText != "" {
			errs++
		}
		totalDur += r.dur
	}
	fmt.Println()

	// --- Report card ---
	rate := float64(ok) / float64(*runs)
	fmt.Println("SUMMARY")
	fmt.Printf("  loop reliability : %d/%d (%.0f%%)\n", ok, *runs, rate*100)
	fmt.Printf("  avg run duration : %s\n", (totalDur / time.Duration(*runs)).Round(time.Millisecond))
	fmt.Printf("  pathologies      : dup-calls=%d spiral=%d tool-xml=%d stall=%d error=%d\n", dup, spiral, toolXML, stall, errs)
	fmt.Printf("  reasoning channel: %s\n", wf.reasoningKey)
	fmt.Printf("  verdict          : %s\n", verdict(wf, rate))

	if rate < 0.5 {
		os.Exit(1) // usable-as-CI signal: <50% loop reliability is a failing grade
	}
}

// groundingTest builds the loop's question and expected-substring. By default
// it picks a file, reads its first meaningful line itself, and asks the model
// to read that file and quote the line — a portable, self-checking read_file
// grounding test. Both halves are overridable for custom/multi-step probes.
func groundingTest(dir, groundFile, question, expect string) (string, string, error) {
	if question != "" && expect != "" {
		return question, expect, nil
	}
	file := groundFile
	if file == "" {
		for _, cand := range []string{"README.md", "go.mod", "CLAUDE.md"} {
			if _, err := os.Stat(dir + "/" + cand); err == nil {
				file = cand
				break
			}
		}
	}
	if file == "" {
		return "", "", fmt.Errorf("no default ground-file found in %s; pass -ground-file or -question/-expect", dir)
	}
	line, err := firstMeaningfulLine(dir + "/" + file)
	if err != nil {
		return "", "", err
	}
	q := fmt.Sprintf("Use the read_file tool to read %q, then reply with its first non-empty line, verbatim.", file)
	return q, line, nil
}

func firstMeaningfulLine(path string) (string, error) {
	b, err := os.ReadFile(path)
	if err != nil {
		return "", err
	}
	for _, line := range strings.Split(string(b), "\n") {
		t := strings.TrimSpace(line)
		if len(t) >= 6 {
			return t, nil
		}
	}
	return "", fmt.Errorf("%s has no line of at least 6 chars to use as a grounding sentinel", path)
}

func reasoningKeyNote(key string) string {
	switch key {
	case "reasoning":
		return "reasoning (NOTE: internal/chat parses only reasoning_content — engine client must also read `reasoning`)"
	case "reasoning_content":
		return "reasoning_content (matches internal/chat)"
	case "both":
		return "both keys populated"
	default:
		return "none (no separate reasoning channel observed)"
	}
}

func verdict(wf wireFindings, rate float64) string {
	toolOK := true
	for _, ck := range wf.checks {
		if strings.HasPrefix(ck.name, "tool-call") && ck.status == statusFail {
			toolOK = false
		}
	}
	switch {
	case !toolOK:
		return "UNUSABLE — cannot emit structured tool calls"
	case rate >= 0.8:
		return "READY — reliable enough for the tool loop"
	case rate >= 0.5:
		return "MARGINAL — usable with loop guards; expect retries"
	default:
		return "UNRELIABLE — tool calls work but the loop rarely completes"
	}
}

func truncateLabel(s string, n int) string {
	s = strings.Join(strings.Fields(s), " ")
	if len(s) > n {
		return s[:n] + "…"
	}
	return s
}

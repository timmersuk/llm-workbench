package main

import (
	"context"
	"strings"
	"time"
)

// modelReport is the full result of probing one model: the single-request
// wire findings plus the per-run loop outcomes. Both single-model mode and
// fleet mode build one of these per model, so they share identical probe
// logic and only differ in how they render it.
type modelReport struct {
	model  string
	wire   wireFindings
	runs   []runResult
	avgDur time.Duration
}

// probeModel runs the wire checks and the N-run loop against one already-
// reachable model, aggregating everything into a modelReport.
func probeModel(ctx context.Context, c *client, model, dir, question, expect string, runs, maxTurns, maxTokens int, testToolChoice bool) modelReport {
	rep := modelReport{model: model, wire: runWireChecks(ctx, c, model, maxTokens, testToolChoice)}
	var total time.Duration
	for i := 0; i < runs; i++ {
		r := runLoop(ctx, c, model, dir, question, expect, maxTurns, maxTokens)
		rep.runs = append(rep.runs, r)
		total += r.dur
	}
	if runs > 0 {
		rep.avgDur = total / time.Duration(runs)
	}
	return rep
}

func (r modelReport) okCount() int {
	n := 0
	for _, run := range r.runs {
		if run.ok {
			n++
		}
	}
	return n
}

func (r modelReport) reliability() float64 {
	if len(r.runs) == 0 {
		return 0
	}
	return float64(r.okCount()) / float64(len(r.runs))
}

// pathologyCounts tallies how many runs exhibited each pathology.
func (r modelReport) pathologyCounts() (dup, spiral, toolXML, stall, errs int) {
	for _, run := range r.runs {
		if run.dupCall {
			dup++
		}
		if run.spiral {
			spiral++
		}
		if run.toolXML {
			toolXML++
		}
		if run.stall {
			stall++
		}
		if run.errText != "" {
			errs++
		}
	}
	return
}

// toolCallSupported reports whether the model emitted a structured tool call
// in the non-streaming wire check — the load-bearing capability. A model that
// fails this cannot drive the loop at all.
func (r modelReport) toolCallSupported() bool {
	for _, ck := range r.wire.checks {
		if ck.name == "tool-call (non-stream)" {
			return ck.status == statusPass
		}
	}
	return false
}

func (r modelReport) verdict() string {
	rate := r.reliability()
	switch {
	case !r.toolCallSupported():
		return "UNUSABLE — no structured tool calls"
	case rate >= 0.8:
		return "READY"
	case rate >= 0.5:
		return "MARGINAL — usable with loop guards"
	default:
		return "UNRELIABLE — tool calls work, loop rarely completes"
	}
}

// pathologyLabel is a compact one-field summary for the fleet matrix.
func (r modelReport) pathologyLabel() string {
	dup, spiral, toolXML, stall, errs := r.pathologyCounts()
	parts := []string{}
	add := func(name string, n int) {
		if n > 0 {
			parts = append(parts, name+"="+itoa(n))
		}
	}
	add("dup", dup)
	add("spiral", spiral)
	add("xml", toolXML)
	add("stall", stall)
	add("err", errs)
	if len(parts) == 0 {
		return "-"
	}
	return strings.Join(parts, ",")
}

func itoa(n int) string {
	if n == 0 {
		return "0"
	}
	var b [20]byte
	i := len(b)
	for n > 0 {
		i--
		b[i] = byte('0' + n%10)
		n /= 10
	}
	return string(b[i:])
}

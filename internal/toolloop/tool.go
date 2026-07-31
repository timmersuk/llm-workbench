// Package toolloop implements the shared tool-call-loop engine of
// docs/adr/0009-shared-tool-loop-engine-for-run-and-execute.md: a generic,
// hand-rolled loop (send message → run any tool calls → feed results back →
// repeat) parameterized by toolset, workspace, turn bound, and stop
// condition, so ChatClientRunner.Run (read-only stage conversations) and,
// later, ChatClientRunner.Execute (implementation) are two configurations of
// one engine rather than two implementations.
//
// docs/adr/0011-hand-roll-tool-loop-engine-over-eino-or-dive.md records why
// this is hand-rolled rather than built on a framework: the loop's real work
// is guarding against local-model misbehaviour (unbounded generation,
// duplicate calls), which no framework supplies and which lives here.
package toolloop

import (
	"context"
	"fmt"
	"path/filepath"
	"strings"

	"github.com/timmersuk/llm-workbench/internal/chat"
)

// Tool is one function the engine can execute during the loop. A Tool is
// pure with respect to the loop: it takes the model's JSON arguments and the
// workspace it is confined to, and returns result text fed back to the model
// as a tool-role message. Execution errors are returned as an error; the
// engine turns them into an error result the model can see and recover from,
// rather than aborting the run (a lesson from the Phase 0 spike, where a
// framework that treated a tool error as fatal killed otherwise-recoverable
// runs).
type Tool interface {
	// Spec is the OpenAI-compatible declaration offered to the model.
	Spec() chat.Tool
	// Execute runs the tool with argumentsJSON (the raw string the model
	// produced) against workspace, returning the text result.
	Execute(ctx context.Context, workspace, argumentsJSON string) (string, error)
}

// toolName is a small helper to read a Tool's declared name.
func toolName(t Tool) string { return t.Spec().Function.Name }

// resolveInWorkspace enforces the confinement guarantee every tool shares: a
// model-supplied relative path must resolve inside workspace and never escape
// it. This mirrors ResolveWorkspace's guarantee for the cwd, extended to
// every path a tool touches.
func resolveInWorkspace(workspace, rel string) (string, error) {
	abs, err := filepath.Abs(filepath.Join(workspace, rel))
	if err != nil {
		return "", err
	}
	root, err := filepath.Abs(workspace)
	if err != nil {
		return "", err
	}
	if abs != root && !strings.HasPrefix(abs, root+string(filepath.Separator)) {
		return "", fmt.Errorf("path %q escapes the workspace", rel)
	}
	return abs, nil
}

// truncateResult caps a tool result at maxToolResultBytes with an explicit
// marker, so an oversized read/search tells the model to narrow its query
// rather than silently losing the tail. Small-context local models are the
// binding constraint (Milestone 8 grill); the caps are conservative fixed
// defaults, configurable later via the tool-output-caps-config task.
func truncateResult(s string) string {
	if len(s) > maxToolResultBytes {
		return s[:maxToolResultBytes] + "\n[truncated: result exceeded the output limit — narrow your query]"
	}
	return s
}

const (
	// maxToolResultBytes caps any single tool result. ~16KB comfortably
	// holds a paged file read or a capped grep without flooding a small
	// context window.
	maxToolResultBytes = 16 * 1024
	// readDefaultLines is how many lines Read returns when the model does not
	// page with offset/limit.
	readDefaultLines = 1000
	// globMaxMatches caps glob output.
	globMaxMatches = 200
)

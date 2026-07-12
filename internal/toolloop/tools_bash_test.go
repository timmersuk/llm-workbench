package toolloop

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func TestBashToolRunsCommand(t *testing.T) {
	dir := t.TempDir()
	b := bashTool{}

	out, err := b.Execute(context.Background(), dir, `{"command":"echo hello"}`)
	if err != nil {
		t.Fatal(err)
	}
	if strings.TrimSpace(out) != "hello" {
		t.Fatalf("unexpected output: %q", out)
	}
}

func TestBashToolRunsInWorkspaceDir(t *testing.T) {
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, "marker.txt"), []byte("in-workspace"), 0o644); err != nil {
		t.Fatal(err)
	}
	b := bashTool{}

	// Reading a relative path only succeeds if cwd is pinned to the workspace.
	out, err := b.Execute(context.Background(), dir, `{"command":"cat marker.txt"}`)
	if err != nil {
		t.Fatal(err)
	}
	if strings.TrimSpace(out) != "in-workspace" {
		t.Fatalf("expected cwd pinned to workspace, got %q", out)
	}
}

func TestBashToolReportsNonZeroExitAsResult(t *testing.T) {
	dir := t.TempDir()
	b := bashTool{}

	// A failed command is signal for the model, not a loop-aborting Go error.
	out, err := b.Execute(context.Background(), dir, `{"command":"echo boom; exit 3"}`)
	if err != nil {
		t.Fatalf("non-zero exit must not be a Go error, got %v", err)
	}
	if !strings.Contains(out, "boom") {
		t.Fatalf("output should include the command's stdout: %q", out)
	}
	if !strings.Contains(out, "exit status 3") {
		t.Fatalf("output should note the exit status: %q", out)
	}
}

func TestBashToolCapturesStderr(t *testing.T) {
	dir := t.TempDir()
	b := bashTool{}

	out, err := b.Execute(context.Background(), dir, `{"command":"echo oops 1>&2"}`)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(out, "oops") {
		t.Fatalf("stderr should be captured in combined output: %q", out)
	}
}

func TestBashToolReportsNoOutput(t *testing.T) {
	dir := t.TempDir()
	b := bashTool{}

	out, err := b.Execute(context.Background(), dir, `{"command":"true"}`)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(out, "no output") {
		t.Fatalf("silent success should say so: %q", out)
	}
}

func TestBashToolKilledOnDeadline(t *testing.T) {
	dir := t.TempDir()
	b := bashTool{}

	ctx, cancel := context.WithTimeout(context.Background(), 300*time.Millisecond)
	defer cancel()

	start := time.Now()
	out, err := b.Execute(ctx, dir, `{"command":"sleep 10"}`)
	if err != nil {
		t.Fatalf("a killed command reports partial output, not a Go error: %v", err)
	}
	if elapsed := time.Since(start); elapsed > 5*time.Second {
		t.Fatalf("command was not killed promptly: took %s", elapsed)
	}
	if !strings.Contains(out, "killed") {
		t.Fatalf("expected a kill marker, got %q", out)
	}
}

func TestBashToolRequiresCommand(t *testing.T) {
	dir := t.TempDir()
	b := bashTool{}
	if _, err := b.Execute(context.Background(), dir, `{}`); err == nil {
		t.Fatal("expected command-required error")
	}
}

func TestBashToolRejectsInvalidArguments(t *testing.T) {
	dir := t.TempDir()
	b := bashTool{}
	if _, err := b.Execute(context.Background(), dir, `not json`); err == nil {
		t.Fatal("expected invalid-arguments error")
	}
}

func TestBashToolTruncatesLargeOutput(t *testing.T) {
	dir := t.TempDir()
	b := bashTool{}

	// Emit well over the cap; the result must be capped with an explicit marker.
	out, err := b.Execute(context.Background(), dir, `{"command":"for i in $(seq 1 100000); do echo aaaaaaaaaa; done"}`)
	if err != nil {
		t.Fatal(err)
	}
	if len(out) > maxBashOutputBytes+200 {
		t.Fatalf("output not capped: %d bytes", len(out))
	}
	if !strings.Contains(out, "truncated") {
		t.Fatalf("expected truncation marker: %q", out[len(out)-100:])
	}
}

func TestExecutionToolsIncludesBash(t *testing.T) {
	names := make(map[string]bool)
	for _, tl := range ExecutionTools() {
		names[toolName(tl)] = true
	}
	if !names["bash"] {
		t.Fatalf("ExecutionTools missing bash: %v", names)
	}
}

func TestBashToolSpecValid(t *testing.T) {
	var schema map[string]any
	if err := json.Unmarshal(bashTool{}.Spec().Function.Parameters, &schema); err != nil {
		t.Fatalf("bash has invalid parameter JSON: %v", err)
	}
}

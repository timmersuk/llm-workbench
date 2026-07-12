package toolloop

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"time"

	"github.com/timmersuk/llm-workbench/internal/chat"
)

const (
	// bashTimeout is the default per-command wall-clock limit (grill,
	// 2026-07-10): a hanging command is killed rather than blocking the loop
	// indefinitely, its partial output returned so the model can react.
	bashTimeout = 2 * time.Minute
	// maxBashOutputBytes caps combined stdout+stderr (~20KB, milestone8 tool
	// caps) so a chatty command doesn't flood a small-context local model.
	maxBashOutputBytes = 20 * 1024
	// bashKillGrace bounds how long Execute waits after a timeout kill before
	// force-closing the command's output pipes. Killing bash does not kill a
	// grandchild (e.g. a `sleep` it spawned) that inherited those pipes, so
	// without this CombinedOutput blocks until the grandchild exits on its own.
	// The orphan still finishes in the background — acceptable under v1's
	// no-OS-sandbox posture (ADR 0010).
	bashKillGrace = 3 * time.Second
)

// --- bash ---

type bashTool struct{}

func (bashTool) Spec() chat.Tool {
	return chat.Tool{
		Type: "function",
		Function: chat.ToolSchema{
			Name:        "bash",
			Description: "Run a shell command with bash from the workspace root as the working directory. Returns combined stdout and stderr, plus a non-zero exit status when the command fails. Commands are killed after a 2-minute timeout.",
			Parameters: json.RawMessage(`{"type":"object","required":["command"],"properties":{` +
				`"command":{"type":"string","description":"Shell command to run via bash -c, from the workspace root"}}}`),
		},
	}
}

func (bashTool) Execute(ctx context.Context, workspace, argumentsJSON string) (string, error) {
	var args struct {
		Command string `json:"command"`
	}
	if err := json.Unmarshal([]byte(argumentsJSON), &args); err != nil {
		return "", fmt.Errorf("invalid arguments: %w", err)
	}
	if args.Command == "" {
		return "", fmt.Errorf("command is required")
	}

	shell, err := bashPath()
	if err != nil {
		return "", err
	}

	// Pinning the working directory to the workspace is the whole confinement
	// in v1 — no path allow-listing or OS-level sandbox (ADR 0010), matching
	// the trust level ClaudeRunner/CodexRunner already operate at.
	root, err := filepath.Abs(workspace)
	if err != nil {
		return "", err
	}

	cctx, cancel := context.WithTimeout(ctx, bashTimeout)
	defer cancel()

	cmd := exec.CommandContext(cctx, shell, "-c", args.Command)
	cmd.Dir = root
	cmd.WaitDelay = bashKillGrace
	out, runErr := cmd.CombinedOutput()
	result := truncateBash(string(out))

	// A killed command (timeout or loop cancellation) reports what it produced
	// so the model can adjust, rather than surfacing as a fatal error.
	if cctx.Err() == context.DeadlineExceeded {
		return appendMarker(result, fmt.Sprintf("[killed: command exceeded the %s timeout]", bashTimeout)), nil
	}

	// A non-zero exit is signal for the model (a failed build, a test failure),
	// not a loop-aborting error: return the output with the status appended.
	var exitErr *exec.ExitError
	if errors.As(runErr, &exitErr) {
		return appendMarker(result, fmt.Sprintf("[exit status %d]", exitErr.ExitCode())), nil
	}
	// Anything else (bash failed to launch) is a genuine error.
	if runErr != nil {
		return "", fmt.Errorf("running command: %w", runErr)
	}
	if result == "" {
		return "[command produced no output]", nil
	}
	return result, nil
}

// bashPath resolves the bash executable: Git-for-Windows bash.exe on Windows
// (already a hard dependency via git worktrees), system bash elsewhere.
func bashPath() (string, error) {
	if runtime.GOOS == "windows" {
		if p, err := exec.LookPath("bash.exe"); err == nil {
			return p, nil
		}
		for _, c := range []string{
			filepath.Join(os.Getenv("ProgramFiles"), "Git", "bin", "bash.exe"),
			filepath.Join(os.Getenv("ProgramFiles"), "Git", "usr", "bin", "bash.exe"),
		} {
			if _, err := os.Stat(c); err == nil {
				return c, nil
			}
		}
		return "", fmt.Errorf("bash not found: install Git for Windows or put bash.exe on PATH")
	}
	p, err := exec.LookPath("bash")
	if err != nil {
		return "", fmt.Errorf("bash not found on PATH: %w", err)
	}
	return p, nil
}

// appendMarker joins a trailing status/truncation marker onto command output,
// keeping the marker on its own line without a leading blank line for empty
// output.
func appendMarker(result, marker string) string {
	if result == "" {
		return marker
	}
	return result + "\n" + marker
}

// truncateBash caps combined command output at maxBashOutputBytes, mirroring
// truncateResult's explicit-truncation marker so the model knows output was
// dropped rather than the command being silent.
func truncateBash(s string) string {
	if len(s) > maxBashOutputBytes {
		return s[:maxBashOutputBytes] + "\n[truncated: output exceeded the limit]"
	}
	return s
}

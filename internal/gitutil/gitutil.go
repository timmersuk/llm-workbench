// Package gitutil holds small, generic git plumbing primitives shared across
// packages that each need to run git against a working directory — resolving
// a workspace's current branch, invoking git via argv — without belonging to
// any one of those packages' own domain (agentrunner resolves execution/
// review workspaces; toolloop implements agent-facing read tools; neither is
// "about" git itself).
package gitutil

import (
	"context"
	"fmt"
	"os/exec"
	"strings"
)

// RunGit runs `git <args...>` with dir as its working directory, returning
// combined stdout+stderr so a failure's error message actually explains what
// went wrong (git writes its errors to stderr). Always invoked via argv, never
// a shell string (ADR 0013).
func RunGit(ctx context.Context, dir string, args ...string) (string, error) {
	cmd := exec.CommandContext(ctx, "git", args...)
	cmd.Dir = dir
	out, err := cmd.CombinedOutput()
	if err != nil {
		return "", fmt.Errorf("git %s: %w: %s", strings.Join(args, " "), err, strings.TrimSpace(string(out)))
	}
	return string(out), nil
}

// CurrentBranch returns dir's currently checked-out branch name.
func CurrentBranch(ctx context.Context, dir string) (string, error) {
	out, err := RunGit(ctx, dir, "rev-parse", "--abbrev-ref", "HEAD")
	if err != nil {
		return "", fmt.Errorf("resolving current branch for %s: %w", dir, err)
	}
	return strings.TrimSpace(out), nil
}

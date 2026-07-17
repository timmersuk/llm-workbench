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
	"os"
	"os/exec"
	"path/filepath"
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

// EnsureGitExclude idempotently adds any of patterns not already present to
// dir's local, uncommitted .git/info/exclude — the .gitignore equivalent
// that never becomes part of the repository's own tracked content. Callers
// writing a workbench-owned scratch file into a checkout use this instead of
// editing the project's own tracked .gitignore: that file is the human's
// repository content, not something this tooling should silently modify
// (docs/adr/0015).
//
// Resolved via --git-common-dir, not --git-dir: for a linked worktree these
// differ (--git-dir is the worktree-specific .git/worktrees/<id>/, which has
// no info/ of its own), while --git-common-dir always names the one
// repository-wide location every worktree shares — so a single call from any
// worktree (or the main checkout) covers all of them.
func EnsureGitExclude(ctx context.Context, dir string, patterns ...string) error {
	commonDir, err := RunGit(ctx, dir, "rev-parse", "--git-common-dir")
	if err != nil {
		return fmt.Errorf("resolving common git dir for %s: %w", dir, err)
	}
	commonDir = strings.TrimSpace(commonDir)
	if !filepath.IsAbs(commonDir) {
		commonDir = filepath.Join(dir, commonDir)
	}
	excludePath := filepath.Join(commonDir, "info", "exclude")

	existing, err := os.ReadFile(excludePath)
	if err != nil && !os.IsNotExist(err) {
		return fmt.Errorf("reading %s: %w", excludePath, err)
	}
	have := make(map[string]bool, len(existing))
	for _, line := range strings.Split(string(existing), "\n") {
		have[strings.TrimSpace(line)] = true
	}

	var toAdd []string
	for _, p := range patterns {
		if !have[p] {
			toAdd = append(toAdd, p)
		}
	}
	if len(toAdd) == 0 {
		return nil
	}

	if err := os.MkdirAll(filepath.Dir(excludePath), 0o755); err != nil {
		return fmt.Errorf("creating %s: %w", filepath.Dir(excludePath), err)
	}
	f, err := os.OpenFile(excludePath, os.O_APPEND|os.O_CREATE|os.O_WRONLY, 0o644)
	if err != nil {
		return fmt.Errorf("opening %s: %w", excludePath, err)
	}
	defer f.Close()

	var b strings.Builder
	if len(existing) > 0 && !strings.HasSuffix(string(existing), "\n") {
		b.WriteString("\n")
	}
	for _, p := range toAdd {
		b.WriteString(p)
		b.WriteString("\n")
	}
	_, err = f.WriteString(b.String())
	if err != nil {
		return fmt.Errorf("writing %s: %w", excludePath, err)
	}
	return nil
}

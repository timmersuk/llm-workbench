// Package gitutil holds small, generic git plumbing primitives shared across
// packages that each need to run git against a working directory — resolving
// a workspace's current branch, invoking git via argv — without belonging to
// any one of those packages' own domain (agentrunner resolves execution/
// review workspaces; toolloop implements agent-facing read tools; neither is
// "about" git itself).
package gitutil

import (
	"context"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
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

// BranchExists reports whether dir's repository has a local branch named
// branch (refs/heads/branch) — unlike CurrentBranch, branch doesn't need to
// be checked out anywhere. `git show-ref` exits 1 (not an error condition)
// when the ref simply isn't found, so that specific case is distinguished
// from a real failure (dir isn't a git checkout, git not installed) via the
// underlying *exec.ExitError, the same way Go's os/exec surfaces "command
// ran but reported failure" generally.
func BranchExists(ctx context.Context, dir, branch string) (bool, error) {
	if _, err := RunGit(ctx, dir, "show-ref", "--verify", "--quiet", "refs/heads/"+branch); err != nil {
		var exitErr *exec.ExitError
		if errors.As(err, &exitErr) {
			return false, nil
		}
		return false, fmt.Errorf("checking for branch %s in %s: %w", branch, dir, err)
	}
	return true, nil
}

// Clone clones url into dest via `git clone url dest`. dest must not
// already exist; dest's parent directory must. Run with dest's parent as
// the working directory purely so RunGit has a valid, existing cwd — the
// clone target is dest itself, given as an argument, not derived from cwd.
func Clone(ctx context.Context, url, dest string) error {
	if _, err := RunGit(ctx, filepath.Dir(dest), "clone", url, dest); err != nil {
		return fmt.Errorf("cloning %s into %s: %w", url, dest, err)
	}
	return nil
}

// BehindOriginStatus is the result of BehindOrigin — a small struct rather
// than (bool, error), so "the fetch failed / staleness is unknown" is
// representable as ordinary data every caller reads, not a Go error every
// caller must specially unwrap (docs/milestones/done/milestone8a.md: a fetch
// failure "degrades to 'staleness unknown,' not an error").
type BehindOriginStatus struct {
	// Known is false when the check couldn't be completed (offline, git not
	// installed, dir isn't a git checkout, current branch has no configured
	// upstream, ...) — Behind is meaningless when Known is false.
	Known bool `json:"known"`
	// Behind is how many commits dir's current branch is behind its own
	// tracked upstream (@{upstream}); 0 if up to date or ahead. Only
	// meaningful when Known is true.
	Behind int `json:"behind"`
}

// BehindOrigin runs `git fetch` in dir, then reports how many commits dir's
// current branch is behind its own tracked upstream. Never returns a Go
// error: any failure along the way (offline, git not installed, dir isn't a
// git checkout, current branch has no configured upstream) collapses to
// BehindOriginStatus{} (Known: false) — this is an advisory-only signal
// (docs/milestones/done/milestone8a.md) that must never propagate as an error a
// caller has to handle.
func BehindOrigin(ctx context.Context, dir string) BehindOriginStatus {
	if _, err := RunGit(ctx, dir, "fetch", "--quiet"); err != nil {
		return BehindOriginStatus{}
	}
	out, err := RunGit(ctx, dir, "rev-list", "--count", "HEAD..@{upstream}")
	if err != nil {
		return BehindOriginStatus{}
	}
	behind, err := strconv.Atoi(strings.TrimSpace(out))
	if err != nil {
		return BehindOriginStatus{}
	}
	return BehindOriginStatus{Known: true, Behind: behind}
}

// DirtyStatus is the result of DirtyWorkingTree — the same "unknown as
// data, not an error" shape as BehindOriginStatus.
type DirtyStatus struct {
	Known bool `json:"known"`
	// Dirty is true when `git status --porcelain` reported any change —
	// including untracked files (plain --porcelain, not
	// --untracked-files=no; docs/milestones/done/milestone8a.md's resolved open
	// question). Only meaningful when Known is true.
	Dirty bool `json:"dirty"`
}

// DirtyWorkingTree reports whether dir's working tree has any uncommitted
// change, tracked or untracked. No network call, but can still fail (dir
// isn't a git checkout, git not installed) — like BehindOrigin, any failure
// collapses to DirtyStatus{} (Known: false) rather than a Go error.
func DirtyWorkingTree(ctx context.Context, dir string) DirtyStatus {
	out, err := RunGit(ctx, dir, "status", "--porcelain")
	if err != nil {
		return DirtyStatus{}
	}
	return DirtyStatus{Known: true, Dirty: strings.TrimSpace(out) != ""}
}

// CommitAll stages every change in dir (`git add -A`, tracked and untracked
// alike) and commits it with message. Used as a mechanical "safety commit"
// when an Execute run ends without success, so whatever the agent wrote to
// disk but never committed itself isn't silently lost the moment a later
// attempt forks a fresh worktree from this branch's tip — not something a
// caller invokes as part of ordinary, successful commit flow.
func CommitAll(ctx context.Context, dir, message string) error {
	if _, err := RunGit(ctx, dir, "add", "-A"); err != nil {
		return fmt.Errorf("staging changes in %s: %w", dir, err)
	}
	if _, err := RunGit(ctx, dir, "commit", "-m", message); err != nil {
		return fmt.Errorf("committing changes in %s: %w", dir, err)
	}
	return nil
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

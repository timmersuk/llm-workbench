package gitstore

import (
	"context"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"time"

	"github.com/sirupsen/logrus"
)

// RunPushWorker periodically turns every pending change accumulated since
// the last tick (by commit.go's enqueue-on-write) into real commits, and
// pushes them to "origin" (dataRepoURL, as configured at Open), until ctx
// is cancelled. It is meant to run for the lifetime of the server process
// in its own goroutine, e.g.:
//
//	go store.RunPushWorker(ctx, pushInterval)
//
// It never fetches or pulls — so a rejected non-fast-forward push (e.g.
// another process, or a manual edit on the remote, moved "origin" ahead of
// what this checkout knows about) is not auto-merged or rebased away. That
// is a deliberate, documented limitation of this package: it is logged and
// retried on every subsequent tick like any other push failure, but
// resolving the actual divergence is a manual operator task with no
// tooling here.
//
// Every failure is logged at Warn level and left to the next tick to
// retry — RunPushWorker itself never gives up and only ever returns when
// ctx is cancelled.
func (s *Store) RunPushWorker(ctx context.Context, interval time.Duration) {
	ticker := time.NewTicker(interval)
	defer ticker.Stop()

	// staleThreshold gates sweepOrphans (below): a file sitting dirty for
	// longer than this could not plausibly still be waiting on a write
	// that's mid-flight (every write enqueues its pending change under the
	// same lock it writes under — see commit.go's withPending), so if it's
	// dirty and unqueued for this long, the only explanation left is a
	// pending change lost to an earlier process restart. 2x the tick
	// interval is a safety margin on top of "should never survive even one
	// tick", not a tight bound.
	staleThreshold := 2 * interval

	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			s.push(staleThreshold)
		}
	}
}

// push runs one full tick: commit every pending change (and sweep any
// stale unqueued leftovers), then push whatever local commits — old or
// new — haven't reached origin yet. Committing happens under core.mu
// (commitPending); the push itself deliberately does not hold it — see
// core.mu's doc comment for why.
func (s *Store) push(staleThreshold time.Duration) {
	if err := s.core.commitPending(staleThreshold); err != nil {
		logrus.WithError(err).Warn("committing pending changes failed, will retry on next tick")
		return
	}

	if !aheadOfUpstream(s.core.root) {
		return
	}

	// `-u`/`--set-upstream` is a no-op reconfirmation on every push after
	// the first — needed because a checkout freshly cloned from an empty
	// remote (gitstore.Open's doc comment) has no upstream tracking branch
	// configured until the first successful push establishes one. `HEAD`
	// (rather than a hardcoded branch name) pushes whichever branch is
	// currently checked out under its own name on origin, so this doesn't
	// need to know or assume the branch name.
	if _, err := runGit(s.core.root, "push", "-u", originRemoteName, "HEAD"); err != nil {
		// Deliberately Warn, not Error/Fatal: a push failure never stops
		// local commits from continuing to succeed (commit.go) — only the
		// remote falls behind until either the underlying problem clears
		// up or an operator reconciles it by hand, retried automatically
		// on every future tick.
		logrus.WithError(err).WithField("dataRepoURL", s.core.dataRepoURL).
			Warn("push to data repository failed, will retry on next tick")
		return
	}
	logrus.WithField("dataRepoURL", s.core.dataRepoURL).Debug("pushed local commits to data repository")
}

// commitPending drains every pending change queued since the last tick
// (commit.go's enqueue-on-write) into one real `git commit` each — scoped
// via `git add <dir>` to just that change's own project/task directory, so
// unrelated pending changes queued in the same interval never end up
// sharing a commit message (see pendingChange's doc comment for why dir is
// always safe to scope to). Then sweeps any leftover dirty files old
// enough to be considered lost pending changes (sweepOrphans) — see
// RunPushWorker's staleThreshold comment for why "old enough" is the right
// test rather than "any leftover at all".
//
// Runs under core.mu for its entire duration: a concurrent API write
// racing an in-progress `git add`/`git commit` could otherwise stage a
// half-attributed change. Does not include the push step — see core.mu's
// doc comment.
func (c *core) commitPending(staleThreshold time.Duration) error {
	c.mu.Lock()
	defer c.mu.Unlock()

	pending := c.pending
	c.pending = nil

	for _, p := range pending {
		if err := c.addAndCommit([]string{p.dir}, p.message); err != nil {
			return err
		}
	}

	return c.sweepOrphans(staleThreshold)
}

// addAndCommit stages paths and commits message, unless nothing ended up
// staged — which happens when an earlier pendingChange in the same
// commitPending call already covered the same directory (both writes had
// already landed on disk by the time draining started, so the first
// `git add` on a shared directory captures both; the second finds nothing
// left to stage). That's a deliberate, accepted squash — see this
// package's design discussion — not an error.
func (c *core) addAndCommit(paths []string, message string) error {
	addArgs := append([]string{"add", "--"}, paths...)
	if _, err := runGit(c.root, addArgs...); err != nil {
		return fmt.Errorf("staging %v: %w", paths, err)
	}

	staged, err := hasStagedChanges(c.root)
	if err != nil {
		return fmt.Errorf("checking staged changes: %w", err)
	}
	if !staged {
		return nil
	}

	commitArgs := append(append([]string{}, commitAuthorArgs...), "commit", "-m", message)
	if _, err := runGit(c.root, commitArgs...); err != nil {
		return fmt.Errorf("committing %q: %w", message, err)
	}
	return nil
}

// sweepOrphans catches any working-tree change that commitPending's queue
// didn't know about: every legitimate write enqueues its own pending
// change under the same lock it writes under (commit.go's withPending), so
// under normal operation nothing should ever be dirty-and-unqueued for
// longer than it takes this tick to run. A file that's been dirty for
// longer than staleThreshold anyway can only mean a pending change was
// lost — the leading case being a process restart between a FileStore
// write landing on disk and the next tick draining the (in-memory, so
// wiped by the restart) queue. Rather than leaving that content
// uncommitted (and therefore unpushed) forever, it's swept into one
// generic commit here, bounding the maximum possible drift between disk
// and git history to roughly staleThreshold.
//
// Must be called with c.mu already held (commitPending's contract).
func (c *core) sweepOrphans(staleThreshold time.Duration) error {
	out, err := runGit(c.root, "status", "--porcelain")
	if err != nil {
		return fmt.Errorf("checking workspace status: %w", err)
	}

	now := time.Now()
	var stale []string
	for _, line := range strings.Split(out, "\n") {
		if len(line) < 4 {
			continue
		}
		path := line[3:]
		// Rename lines look like "R  old -> new"; the new path is what's
		// actually sitting on disk to check the age of.
		if idx := strings.Index(path, " -> "); idx >= 0 {
			path = path[idx+len(" -> "):]
		}
		info, err := os.Stat(filepath.Join(c.root, path))
		if err != nil {
			// Deleted or otherwise inaccessible. This package has no
			// delete-type operation today (commit.go is entirely
			// Create/Update/Append/Record), so this path is never expected
			// to be hit in practice — skip rather than guess at handling.
			continue
		}
		if now.Sub(info.ModTime()) >= staleThreshold {
			stale = append(stale, path)
		}
	}
	if len(stale) == 0 {
		return nil
	}

	message := fmt.Sprintf("Sweep: recover %d unqueued change(s), likely from a prior restart", len(stale))
	return c.addAndCommit(stale, message)
}

// aheadOfUpstream reports whether the current branch has any commit not
// yet reflected in its upstream tracking ref (`@{u}`) — updated locally by
// this package's own last successful push, so checking it is a purely
// local ref comparison, no network call needed to ask "is there anything
// to push". Two states read as "nothing to push, don't bother": no commit
// exists yet (`rev-parse HEAD` fails — a fresh clone of an empty remote
// with nothing created since) and HEAD already matches `@{u}` (the
// steady-state idle case this exists to short-circuit, avoiding a
// `git push` subprocess and network round-trip on every tick for no
// reason). No upstream configured yet (`rev-parse @{u}` fails — before
// this checkout's very first successful push) reads as "try" — that's the
// only way to find out.
func aheadOfUpstream(root string) bool {
	head, err := runGit(root, "rev-parse", "HEAD")
	if err != nil {
		return false
	}
	upstream, err := runGit(root, "rev-parse", "@{u}")
	if err != nil {
		return true
	}
	return strings.TrimSpace(head) != strings.TrimSpace(upstream)
}

// hasStagedChanges reports whether the git index currently has any staged
// (uncommitted) changes — `git diff --cached --quiet` exits 0 for "clean"
// and 1 for "changes staged"; any other exit code or launch failure is a
// real error, not a status to interpret.
func hasStagedChanges(root string) (bool, error) {
	cmd := exec.Command("git", "diff", "--cached", "--quiet")
	cmd.Dir = root
	err := cmd.Run()
	if err == nil {
		return false, nil
	}
	var exitErr *exec.ExitError
	if errors.As(err, &exitErr) && exitErr.ExitCode() == 1 {
		return true, nil
	}
	return false, fmt.Errorf("git diff --cached --quiet: %w", err)
}

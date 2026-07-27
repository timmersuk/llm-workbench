package gitstore

import (
	"context"
	"errors"
	"time"

	"github.com/go-git/go-git/v5"
	"github.com/sirupsen/logrus"
)

// RunPushWorker periodically pushes every commit accumulated on the local
// checkout (by commit.go's synchronous Create/Update commits) to "origin"
// (dataRepoURL, as configured at Open), until ctx is cancelled. It is
// meant to run for the lifetime of the server process in its own
// goroutine, e.g.:
//
//	go store.RunPushWorker(ctx, pushInterval)
//
// It never fetches or pulls — see this package's doc comment for why: this
// milestone's push worker is push-only, so a rejected non-fast-forward
// push (e.g. another process, or a manual edit on the remote, moved
// "origin" ahead of what this checkout knows about) is not auto-merged or
// rebased away. That is a deliberate, documented limitation of this
// milestone (see the design note near cmd/server/main.go): it is logged
// and retried on every subsequent tick like any other push failure, but
// resolving the actual divergence is a manual operator task with no
// tooling here.
//
// Every failure, including non-fast-forward rejections, is logged at Warn
// level and left to the next tick to retry — RunPushWorker itself never
// gives up and only ever returns when ctx is cancelled.
func (s *Store) RunPushWorker(ctx context.Context, interval time.Duration) {
	ticker := time.NewTicker(interval)
	defer ticker.Stop()

	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			s.push()
		}
	}
}

// push attempts a single push of every local commit to origin, holding
// core.mu for the duration — the same mutex commit.go's withCommit uses —
// since go-git's *git.Repository offers no concurrency guarantees of its
// own for two operations (a local commit and a push, say) running against
// the same checkout at once.
func (s *Store) push() {
	c := s.core
	c.mu.Lock()
	defer c.mu.Unlock()

	err := c.repo.Push(&git.PushOptions{RemoteName: originRemoteName})
	if err == nil {
		logrus.WithField("dataRepoURL", c.dataRepoURL).Debug("pushed local commits to data repository")
		return
	}
	if errors.Is(err, git.NoErrAlreadyUpToDate) {
		return
	}
	// Deliberately Warn, not Error/Fatal: a push failure (including a
	// non-fast-forward rejection) never stops local writes from continuing
	// to commit successfully (commit.go) — only the remote falls behind
	// until either the underlying problem clears up or an operator
	// reconciles it by hand, retried automatically on every future tick.
	logrus.WithError(err).WithField("dataRepoURL", c.dataRepoURL).
		Warn("push to data repository failed, will retry on next tick")
}

package agentrunner

import (
	"context"
	"sync"
	"time"

	"github.com/timmersuk/llm-workbench/internal/gitutil"
)

// WorkspaceStatus bundles the two advisory hygiene signals for a project's
// shared checkout (docs/milestones/done/milestone8a.md's "Advisory checks"):
// whether its current branch is behind its own tracked upstream, and
// whether it has any uncommitted change. Both are advisory only — nothing
// here ever blocks Execute/Review, unlike ErrWrongBranch (worktree.go).
type WorkspaceStatus struct {
	BehindOrigin gitutil.BehindOriginStatus `json:"behind_origin"`
	Dirty        gitutil.DirtyStatus        `json:"dirty"`
}

// behindOriginCacheTTL throttles the git-fetch-requiring behind-origin
// check so rapid-fire conversation turns against the same shared checkout
// don't fetch on every message (docs/milestones/done/milestone8a.md, resolved:
// 5 minutes — matching AGENT_TIMEOUT's existing default elsewhere in this
// codebase). The dirty-working-tree check needs no such throttle — no
// network call — so GetWorkspaceStatus always runs it fresh.
const behindOriginCacheTTL = 5 * time.Minute

type behindOriginCacheEntry struct {
	status  gitutil.BehindOriginStatus
	fetched time.Time
}

// behindOriginLocks/behindOriginCache are keyed by the same absolute
// workspace path workspacePath derives — the same sync.Map-per-path shape
// cloneLocks (runner.go) already uses for a different per-workspace-path
// concurrency problem, applied here so two concurrent callers racing past
// a stale cache entry don't both fire a real git fetch within the same TTL
// window. Because the key is identical to ResolveWorkspace's own
// derivation, a stage-conversation turn's check and a workspace-status
// HTTP poll within the same window transparently share one cache entry —
// one cache per checkout, not one per caller.
var behindOriginLocks sync.Map // map[string]*sync.Mutex
var behindOriginCache sync.Map // map[string]behindOriginCacheEntry

// nowFunc/behindOriginChecker/dirtyChecker are indirected purely for tests
// — the same seam cloneRepository (runner.go) provides for Clone — so the
// TTL-cache logic is exercised without a real 5-minute wait or a real git
// subprocess; gitutil_test.go separately exercises the real subprocess
// behavior of BehindOrigin/DirtyWorkingTree directly.
var nowFunc = time.Now
var behindOriginChecker = gitutil.BehindOrigin
var dirtyChecker = gitutil.DirtyWorkingTree

// GetWorkspaceStatus resolves repositories[0]'s shared-checkout path under
// reposRoot (workspacePath — never cloning: a missing/never-cloned
// checkout just yields "unknown" on both checks, the same outcome as any
// other git failure). Returns ErrNoRepository/ErrInvalidRepository only
// for bad input (no repositories configured, or a malformed identifier) —
// never for a git-level failure, which is represented as data inside the
// returned WorkspaceStatus instead.
func GetWorkspaceStatus(ctx context.Context, reposRoot string, repositories []string) (WorkspaceStatus, error) {
	workspace, err := workspacePath(reposRoot, repositories)
	if err != nil {
		return WorkspaceStatus{}, err
	}
	return WorkspaceStatus{
		BehindOrigin: behindOriginCached(ctx, workspace),
		Dirty:        dirtyChecker(ctx, workspace),
	}, nil
}

func behindOriginCached(ctx context.Context, workspace string) gitutil.BehindOriginStatus {
	lockVal, _ := behindOriginLocks.LoadOrStore(workspace, &sync.Mutex{})
	mu := lockVal.(*sync.Mutex)
	mu.Lock()
	defer mu.Unlock()

	now := nowFunc()
	if v, ok := behindOriginCache.Load(workspace); ok {
		entry := v.(behindOriginCacheEntry)
		if now.Sub(entry.fetched) < behindOriginCacheTTL {
			return entry.status
		}
	}
	status := behindOriginChecker(ctx, workspace)
	behindOriginCache.Store(workspace, behindOriginCacheEntry{status: status, fetched: now})
	return status
}

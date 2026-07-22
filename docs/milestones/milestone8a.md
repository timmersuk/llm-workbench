# Milestone 8a — Shared-Checkout Hygiene

**Status:** Scoped via a `/grill-with-docs` session on 2026-07-22. Numbered
`8a` rather than folded into the Milestone 9 sequence because it's a
prerequisite the core Task loop's trustworthiness depends on, not a new
capability — it should land before Milestone 9 (knowledge-base promotion),
not after. **PR 1 shipped** (2026-07-22): lazy auto-clone, live-verified —
see "What shipped (PR 1)" below. **PR 2 shipped** (2026-07-22):
`Project.DefaultBranch` determination plus the blocking wrong-branch gate,
unit-tested (no live `gh`/GitHub call — see "What shipped (PR 2)" below for
why that's still an honest verification, not a shortcut).

## Why now

An audit of what stands between the current codebase and an MVP-usable
inception-to-delivery loop found that every other open orphan/incomplete
item (`docs/milestones/milestone-orphans.md`, plus assorted draft-status
backlog tasks) is a deferred feature, a hardening pass, or ops/tooling
polish layered on top of an already-working loop. Two items from
Milestone 7 are different in kind: they don't add a missing capability,
they're silent correctness risks sitting inside the loop's own core
stages.

* **No repo auto-clone.** `ResolveWorkspace`
  (`internal/agentrunner/runner.go:206-231`) presumes a project's
  repository is already cloned at `AGENT_REPOS_ROOT` and errors
  (`ErrInvalidRepository`) if the directory doesn't exist, rather than
  cloning it.
* **No staleness check on the shared checkout.** Nothing verifies the
  shared checkout is current or on the right branch before a
  Requirements/Architecture/Planning conversation runs against it — and,
  more consequentially, `ResolveExecutionWorkspace`/`ResolveReviewWorkspace`
  (`internal/agentrunner/worktree.go:64,116`) both fork a new worktree from
  whatever branch the shared checkout currently happens to be on, which
  then becomes that work's PR base branch
  (`internal/agentrunner/pr.go:104-107,141`, `PushAndOpenPR` takes no
  caller-supplied `baseBranch` — it's always `gitutil.CurrentBranch(ctx,
  dir)`). A shared checkout accidentally left on a feature branch doesn't
  just make Requirements/Planning's answers questionable — it silently
  determines what branch an entire execution gets built and merged
  against.

Both were flagged with open design forks in their own orphan entries
("whether the fix is a `remote` field... or a one-time bootstrap", "exact
mechanism undecided") and neither had a backlog task stub. This session
resolves both.

## Introduces

* **Lazy auto-clone.** `ResolveWorkspace` clones the repository on first
  access if the resolved directory is missing, rather than erroring.
* **`Project.default_branch`.** A new, human-inspectable/correctable field,
  auto-determined via `gh repo view --json defaultBranchRef` and persisted
  — not re-queried live. Populated lazily whenever it's unset, independent
  of whether cloning happened, so already-checked-out projects
  (`llm-workbench`, `agent-shell`) get backfilled the same way a
  freshly-cloned one would.
* **A blocking wrong-branch gate**, uniquely among the checks introduced
  here: `ResolveExecutionWorkspace`/`ResolveReviewWorkspace` refuse to fork
  a worktree if the shared checkout isn't on `Project.default_branch`.
* **Two advisory checks**: behind-origin (TTL-throttled `git fetch` +
  comparison against the current branch's own upstream) and
  dirty-working-tree (`git status --porcelain`), surfaced but never
  blocking.
* **Dual surfacing** of the advisory signals: injected into the
  stage-conversation system prompt (so the agent can factor them into its
  answers) *and* a frontend UI banner (so a human sees them regardless of
  whether the model mentions them) — matching "no hidden state"
  (`docs/architectural invariants.md`) as a guarantee, not a hope.

## Auto-clone

`repositories[0]` already carries a host+path string by existing
convention (e.g. `github.com/timmersuk/llm-workbench` —
`docs/engineering conventions.md:278-291` documents `ResolveWorkspace`
deriving the workspace directory name from its last path segment). No new
`Project` field is needed for cloning itself: prefix `https://` and
`git clone` it. **HTTPS only, no new credential storage** — cloning relies
entirely on the operator's ambient git credential setup (a credential
helper, cached PAT, whatever `git` on that machine already does for a
manual clone of the same URL), the same trust model `internal/gitutil`
already operates at everywhere else. A clone failure surfaces as an
ordinary, visible error — no retry loop, no credential-prompting UI.

Clone-if-absent lives inside `ResolveWorkspace` itself (lazy, not a
separate human-triggered bootstrap step), guarded by a per-path lock so two
concurrent requests can't race to clone the same directory. The existing
`AGENT_TIMEOUT` (default 5m, `docs/engineering conventions.md:271`) already
bounds a `Run` call well beyond what a clone actually takes, so the first
turn against a brand-new project simply takes longer once, rather than
needing new progress/async plumbing.

## `Project.default_branch` and the wrong-branch gate

There is deliberately no general "default branch" concept anywhere in this
codebase today — `PushAndOpenPR` (`internal/agentrunner/pr.go:141`) already
treats "whatever the shared checkout is currently on" as the legitimate PR
base branch, by design, with no caller override. This milestone introduces
the first stored exception to that: a `Project.default_branch` field,
because that "whatever it's on" philosophy is exactly what makes an
accidental branch switch expensive to recover from (per this session's own
discussion — a worktree forked from an accidental feature branch commits
an entire execution to the wrong PR base, discovered only once it's time
to merge).

`default_branch` is determined via `gh repo view --json defaultBranchRef`
— consistent with, not a new dependency beyond, `pr.go`'s existing
GitHub-specific `gh` usage — the first time `ResolveWorkspace` finds it
unset on a `Project`, and persisted there afterward (human-inspectable and
-correctable in the project's YAML like any other field, never silently
re-derived once set).

**If determination fails** (`gh` not installed, not authenticated,
rate-limited, or a non-GitHub remote), this **fails closed**: Execute/Review
cannot run for that project until `default_branch` is set, either by fixing
whatever blocked the `gh` lookup or by setting the field by hand. This is a
deliberate choice, made against the alternative (fail open, degrade to
today's unprotected behavior) — accepted with the understanding that a
machine-wide `gh` problem (auth expired, not installed) blocks Execute/Review
across every project needing backfill on that machine simultaneously, not
just one.

Once known, `default_branch` backs a **blocking** check —
the one check in this milestone that actually gates work rather than
merely reporting on it: `ResolveExecutionWorkspace`/`ResolveReviewWorkspace`
compare the shared checkout's `gitutil.CurrentBranch` against
`Project.default_branch` before forking a worktree, and refuse if they
don't match. This is deliberately stricter than every other check here,
because it's the one place a wrong answer is expensive to undo — a
worktree fork and subsequent PR-base-branch commitment, not just a
conversation's advisory context.

## Advisory checks: behind-origin and dirty-working-tree

Two further signals, checked wherever `ResolveWorkspace` resolves the
shared checkout, both **advisory only** — they inform, they never block:

* **Behind-origin** — is the current branch behind its own tracked
  upstream? Requires a `git fetch`, throttled with a short in-memory TTL
  per workspace so rapid-fire conversation turns don't trigger a fetch on
  every message; a fetch failure (offline, transient network issue)
  degrades to "staleness unknown," not an error.
* **Dirty-working-tree** — uncommitted changes in the shared checkout
  (`git status --porcelain`), no network needed.

Both surface identically: injected into the stage-conversation system
prompt (alongside the existing Task/Project/Knowledge context building —
`internal/api/stage_conversation.go`) and as a frontend banner, independent
of whether the model chooses to mention either in its reply.

## What shipped (PR 1, 2026-07-22)

`ResolveWorkspace` (`internal/agentrunner/runner.go`) now takes a
`context.Context` and, when the resolved directory doesn't exist, clones it
via a new `gitutil.Clone(ctx, url, dest)` primitive instead of erroring —
HTTPS-derived from `repositories[0]` (`"https://" + repositories[0]`), no
new credential handling, relying entirely on the operator's ambient git
config exactly like every other `gitutil` call. A per-workspace-path
`sync.Map` of mutexes (`cloneLocks`) serializes concurrent callers so two
requests resolving the same missing workspace at once clone exactly once,
not twice; the actual clone call is indirected (`cloneRepository`, a
package-level var defaulting to `gitutil.Clone`) so tests substitute a fake
rather than hitting the network or spawning a real subprocess. A new
`ErrCloneFailed` sentinel is distinct from `ErrInvalidRepository` — the
former is a failed clone attempt (network, auth, wrong URL), the latter is
about the repository identifier itself being malformed or unsafe.

All five `ResolveWorkspace` call sites (`internal/agentrunner/worktree.go`'s
`ResolveExecutionWorkspace`/`ResolveReviewWorkspace`, and
`internal/api/finalize.go`, `pr.go`, `stage_conversation.go`) now pass a
real `context.Context` through — `r.Context()` at the three HTTP-handler
call sites, the already-threaded `ctx` at the other two.

One real gap surfaced and fixed along the way, not scoped in the original
design: `TestResolveWorkspace_RejectsPathTraversal` previously passed only
because a `..`-laden repository identifier harmlessly fell through to a
"workspace doesn't exist" error once `path.Base` cleaned it (the cleaned
form never actually escapes `reposRoot`, so nothing was ever really being
rejected on its own merits) — but a missing workspace now triggers a real
clone attempt (a subprocess and a network call) rather than just erroring.
`ResolveWorkspace` now explicitly rejects any `.`/`..` path component in
the *raw* identifier before that cleaning happens, so a malformed
identifier is caught before it can reach the clone at all.

Verified end-to-end with a real (non-mocked) `git clone`, not just the
indirected unit tests: `TestClone_ClonesRealRepository`
(`internal/gitutil/gitutil_test.go`) clones a real local source repository
(built the same way `worktree_test.go`'s own `initTestRepo` fixture does)
into a fresh destination and reads back its committed content.

## What shipped (PR 2, 2026-07-22)

`Project` gained a `DefaultBranch` field (`internal/project/project.go`),
threaded through `CreateInput`/`UpdateInput` like every other field —
human-inspectable and -correctable via the normal project-edit path, not a
special case.

`internal/agentrunner/default_branch.go` is new: a `DefaultBranchResolver`
interface (`Determine(ctx, repository) (string, error)`) mirroring
`GitHubPRClient`'s shape (`pr.go`) exactly — a real implementation shelling
out to `gh repo view <owner>/<repo> --json defaultBranchRef -q
.defaultBranchRef.name`, a fake in tests. It takes the repository
explicitly as an argument rather than resolving it from a local checkout's
git remote config the way `pr.go`'s other `gh`/`git` calls do, so
determining a default branch never needs a workspace to already exist —
`githubOwnerRepo` just strips `repositories[0]`'s leading host segment
(`"github.com/timmersuk/llm-workbench"` → `"timmersuk/llm-workbench"`).

The orchestration — read `Project.DefaultBranch` first, call the resolver
only when unset, persist the result — lives in `internal/api`
(`default_branch.go`'s `ensureDefaultBranch`), not `agentrunner`: this
keeps `agentrunner` free of any dependency on the `project` package's
domain types, consistent with `ResolveWorkspace`/`ResolveExecutionWorkspace`
already taking plain `repositories []string` rather than a `project.Project`.
`ensureDefaultBranch` is threaded into all three places a Project's default
branch can matter — `handleStartExecution`, `handleReviewDiff`, and
`resolveStageRun`'s Review-stage branch (`stage_conversation.go`) — the
same `DefaultBranchResolver` seam threaded through `NewRouter` exactly the
way `GitHubPRClient`/`prClient` already is. **Fails closed**: a
determination failure propagates as an ordinary HTTP error rather than
letting Execute/Review proceed with an unknown default branch.

The blocking check itself (`checkDefaultBranch`, `worktree.go`) is a new
`ErrWrongBranch` sentinel, distinct from every other check this milestone
introduces: `ResolveExecutionWorkspace`/`ResolveReviewWorkspace` both call
it right after deriving `baseBranch` via `gitutil.CurrentBranch`, and both
refuse to proceed — the one place in this milestone something actually
blocks rather than merely advises. An empty `defaultBranch` (never
determined) blocks exactly like a real mismatch does, by construction (the
comparison always fails against `""`), not as a special case. Applied to
*both* functions even though only `ResolveExecutionWorkspace` forks a new
worktree — `ResolveReviewWorkspace` re-derives `BaseBranch` from the shared
checkout on every call too, and a wrong branch there would silently corrupt
every diff Review computes against it, not just block a fork.

**Verification note:** this PR is unit-tested only, not live-verified
against a real `gh`/GitHub call — unlike PR 1's real (non-mocked) `git
clone` test, there's no equivalent here exercising the actual `gh repo
view` subprocess, since doing so would require live GitHub auth in the
test environment. `DetermineDefaultBranch`'s `gh` invocation itself
(`runGH`, shared with `pr.go`) is exactly the same call shape `pr.go`'s own
production code already uses and already ships — the new surface here is
`githubOwnerRepo`'s string parsing (unit-tested directly) and the
blocking/fail-closed control flow around it (unit-tested via the
`DefaultBranchResolver` fake), not the `gh` invocation mechanism itself.

**PR 1 (shipped):**

* `internal/gitutil`: new `Clone` primitive, alongside the existing
  `RunGit`/`CurrentBranch`.
* `internal/agentrunner`: `ResolveWorkspace` gains clone-if-absent,
  indirected (`cloneRepository`) for testability, guarded by a per-path
  lock (`cloneLocks`).

**PR 2 (shipped):**

* `internal/project`: new `DefaultBranch` field on `Project`, threaded
  through `CreateInput`/`UpdateInput` (human-correctable) like every other
  field.
* `internal/agentrunner`: new `default_branch.go` —
  `DefaultBranchResolver` interface, `NewDefaultBranchResolver()`, and
  `ErrDefaultBranchUnknown`. **Not** folded into `ResolveWorkspace` itself
  (the original sketch here) — the determine-and-persist orchestration
  lives in `internal/api` instead (`ensureDefaultBranch`), since it needs
  `project.Store`/`project.UpdateInput`, and `agentrunner` deliberately
  takes no dependency on the `project` package's domain types (matching
  `ResolveWorkspace` already taking `repositories []string`, not a
  `project.Project`).
* `internal/agentrunner/worktree.go`: new `ErrWrongBranch` sentinel and
  `checkDefaultBranch`; `ResolveExecutionWorkspace`/`ResolveReviewWorkspace`
  both gain a `defaultBranch string` parameter and call it.
* `internal/api`: new `default_branch.go` (`ensureDefaultBranch`), threaded
  as a `DefaultBranchResolver` parameter through `NewRouter` and every
  handler that calls `ResolveExecutionWorkspace`/`ResolveReviewWorkspace` —
  `handleStartExecution`, `handleReviewDiff`, `resolveStageRun`/
  `buildReviewContext`.

**PR 3 (not yet built):**

* `internal/gitutil`: new primitives for `git fetch` (TTL-throttled,
  wrapped so a failure returns "unknown" rather than propagating) and
  `git status --porcelain` (dirty-tree check), alongside the existing
  `RunGit`/`CurrentBranch`/`Clone`.
* `internal/api/stage_conversation.go`: system-prompt injection for the two
  advisory signals.
* Frontend: a banner component surfacing the same two advisory signals.

## Out of scope

* **SSH cloning and any credential storage/management.** HTTPS with
  ambient auth only; a private repo without a working ambient credential
  simply fails to clone, visibly.
* **Non-GitHub default-branch detection.** `default_branch` determination
  is GitHub-specific (`gh repo view`), inheriting the same constraint
  `pr.go`'s PR-opening mechanism already has — not a new limitation this
  milestone introduces.
* **Blocking on behind-origin or dirty-working-tree.** Deliberately kept
  advisory — only wrong-branch is expensive enough to undo to justify
  blocking.
* **An explicit human-triggered bootstrap step.** Auto-clone is lazy/inline
  only; a dedicated "set up this project" UI action was considered and
  rejected as more scope than the latency risk warrants.

## Phasing

* **PR 1 — Auto-clone. ✅ Shipped (2026-07-22).** Lazy clone-if-absent
  inside `ResolveWorkspace`, HTTPS-derived from `repositories[0]`,
  per-path lock against concurrent clones. No `default_branch`/staleness
  work yet — proven independently against a project whose directory
  doesn't yet exist. See "What shipped (PR 1)" below.
* **PR 2 — `default_branch` and the blocking wrong-branch gate. ✅ Shipped
  (2026-07-22).** The `gh repo view` lookup, lazy backfill decoupled from
  cloning, the persisted field, and the blocking comparison wired into
  `ResolveExecutionWorkspace`/`ResolveReviewWorkspace`. This is the
  safety-critical piece and lands before the advisory checks. See "What
  shipped (PR 2)" below.
* **PR 3 — Advisory checks and surfacing.** TTL-throttled behind-origin
  fetch, dirty-working-tree check, system-prompt injection, and the
  frontend banner.

## Open questions for whoever executes this milestone

* **Does `git status --porcelain` count untracked files as "dirty," or
  only modifications to already-tracked files?** Counting untracked files
  risks false positives from incidental scratch files; not decided during
  scoping.
* **Exact TTL for the behind-origin fetch throttle** — "short" was agreed,
  a specific duration (e.g. 2 vs 5 minutes) wasn't pinned down.
* ~~**The blocking gate's error message/recovery hint**~~ — resolved in PR 2:
  `checkDefaultBranch` reports the plain mismatch (`shared checkout is on
  %q, expected %q`), no explicit "switch to X" recovery hint. Revisit if
  PR 3's frontend banner needs friendlier copy than the raw backend error.
* **Frontend banner placement** — which view(s) show the advisory
  staleness banner; not specified during scoping.

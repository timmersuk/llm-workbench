# Milestone 7 — Merge and PR Cycle

**Status: Scoping (2026-07-16)** — general shape scoped via a
`/grill-with-docs` session on 2026-07-15, then independently reviewed
against the codebase by a second model pass, which caught two real
mechanism gaps (both now folded in: the review-record reuse for
`pr_review`'s reject actions, and the refspec-push approach for PR
continuity across a rejection cycle — see "The push/PR/rejection
mechanism" and "Schema changes"). **PR 1, PR 2, and PR 3 scoped via
follow-up `/grill-with-docs` sessions on 2026-07-16** — see "Phasing"
below; `pr_review`/`StagePRReview` naming is now final, not a placeholder.
Remaining per-section detail (the PR-comment tool) is still to be
sharpened in a further follow-up session the way Milestone 6's PRs 3, 5,
and 6 each were. **Knowledge-base
promotion, previously bundled into this milestone by Milestone 6's "Out of
scope" section, has been split out** — see "Out
of scope" below for why and where it goes instead.

## Why now

Milestone 6 shipped Review's three-way decision, but `approved` only ever
reaches `stage: complete` with the execution branch sitting in a local
worktree — human-mergeable by hand, exactly as `internal/agentrunner`'s
doc comments already say, with no merge helper anywhere in that package
(confirmed absent; this is the gap Milestone 6 explicitly deferred here).

Scoping this milestone surfaced that "merge" was the wrong verb entirely.
A plain local `git merge` into `main` — the original, narrower framing —
would bypass any external code review or CI a real team wants before code
lands on a shared branch, which is not what `docs/vision.md`'s "I review
the branch... I merge" is actually describing for anyone working with a
team and a remote. This milestone instead builds toward the realistic
version: push the execution branch to GitHub, open a PR, and let the
normal human/team PR process — external review, CI, an eventual merge
button click on GitHub itself — play out, with the workbench staying in
sync with that outcome rather than trying to perform the merge itself.

## Introduces

* **A new stage, `pr_review`**, inserted between `review` and the
  (renamed) terminal stage. `StageComplete` is renamed to `StageMerged`
  throughout (`internal/task/task.go`'s stage-constants block,
  `FinalizeReview`'s `nextStage = StageComplete` branch,
  `internal/api/review_test.go`, `internal/task/review_test.go`,
  `TaskDetailPanel.tsx`'s stage routing) — "complete" was misleading for
  a task whose branch hadn't actually reached `main` yet.
* **A human-triggered "Push & Open PR" action**, available once a
  Review's `approved` verdict lands the task on `pr_review`: pushes the
  execution branch to the project's GitHub remote and opens a PR via the
  `gh` CLI, shelled out by argv (never a shell string, continuing ADR
  0013's discipline) and relying on ambient `gh auth`/git credential
  configuration already present on the host machine — no credential or
  token storage is built.
* **Task-level PR tracking** (a `pull_request: {url, number}`-shaped
  field, sketch below) so the PR link survives a reload, and so a task
  that cycles through more than one execution attempt against the same
  logical PR (see the rejection cycle below) pushes more commits to the
  *same* PR rather than opening a duplicate.
* **Two human-recorded resolutions from `pr_review`**, implemented by
  **widening `FinalizeReview` to also be valid from `StagePRReview`** (not
  just `StageReview`) rather than a parallel mechanism:
  * **"Mark as merged"** — a human assertion, no GitHub polling in this
    milestone, and no review-record write (there's no approved/rejected/
    needs_changes decision being made — the PR already got its verdict
    externally) — advances to `StageMerged` directly.
  * **Reject** — the human chooses `needs_changes` (→ `implementation`)
    or `rejected` (→ `requirements`), which **writes a new
    `reviews/review-NNN.yaml` entry** through the widened `FinalizeReview`
    — the exact same decision shape and stage-transition logic an
    internal Review verdict already produces, deliberately reused rather
    than reimplemented. This isn't just a naming parallel: PR 4's
    fork-from-prior-branch gate (`resolveReviewContinuation`,
    `internal/api/execution.go:262-283`) keys off the *latest recorded
    review's decision*, not which stage produced it — so a
    `needs_changes` review written from `pr_review` correctly re-triggers
    that existing gate with zero new logic, and a `rejected` review
    written from `pr_review` gets PR 5's "surface prior rejection into
    the reopened Requirements prompt" addendum for free, for the same
    reason.
* **A new GitHub PR-comment read tool**, extending Milestone 6 PR 6's
  precedent (a narrow, argv-only, read-only tool over an artifact the
  agent couldn't otherwise see — ADR 0013) one layer further out: to the
  PR's actual review comments, not just the rejected branch's code. Given
  to whichever stage's agent is reopened on rejection (Requirements *or*
  Implementation), so a capable model can inspect what an external
  reviewer actually said instead of a human transcribing it into a
  prompt by hand.

## The push/PR/rejection mechanism

Arriving at `pr_review` (via Review's `approved`) shows a "Push & Open
PR" action — explicit, not automatic, the same reasoning as Review's own
`autoStart: false` (Milestone 6 PR 3 decision 2): this is a real,
externally-visible, hard-to-reverse action (a team-visible PR, not a
disposable local worktree), so it must not fire on a panel mount.
The first time it's clicked for a task, it pushes the execution's branch
and runs `gh pr create`, recording the resulting URL, number, and
**branch name** onto the task's `pull_request` field.

Because every execution attempt gets its own freshly-named branch by
design (`ExecutionBranchName`, ADR 0012 decision 1 — deliberate
per-attempt audit isolation, not something this milestone should touch),
a later attempt continuing after a PR rejection cycle (below) lands on a
*different* local branch than the one the existing PR already points at.
Rather than opening a second PR, a push for a task that already has a
`pull_request` field uses an explicit refspec —
`git push origin <new-attempt-branch>:<pull_request.branch>` — landing
the new attempt's commits onto the *remote* branch the PR already
tracks, without renaming or reusing the local branch itself. This keeps
Milestone 6's one-worktree-per-attempt invariant fully intact while still
keeping one PR per task.

From `pr_review`, the task sits until a human records what actually
happened on GitHub — there is no live polling or webhook integration in
this milestone (see "Out of scope"). Two paths forward:

* **Merged** — a "Mark as merged" action moves `Stage` to `StageMerged`
  directly, with no review-record write (see "Introduces"); there is no
  separate "PR approved but not yet merged" waypoint. That waypoint was in an earlier draft of this scoping pass
  and was deliberately dropped: it's a clean, unambiguous state only in
  the specific case of exactly one external approver (a two-person
  team), and a Schrödinger's-cat state everywhere else (a solo project
  has no external approver at all; a team requiring multiple approvals
  has no single moment "approved" cleanly becomes true). Collapsing it
  out avoids a state the tool can't actually keep honest. The
  "underhanded motive" for having a distinct stage in the first place —
  a future milestone polling GitHub and using an LLM to hint the human
  toward the right next action — doesn't need the stage to survive: that
  future work can annotate the `pr_review` screen with GitHub's live
  status (open / N of M approved / changes requested / merged) and a
  hint, without the workbench's own state machine claiming to know
  "approved" as a discrete fact.
* **Rejected / changes requested** — the human picks `needs_changes`
  (→ `implementation`) or `rejected` (→ `requirements`), recorded as a
  new review verdict through the widened `FinalizeReview` (see
  "Introduces" for why this reuse — not a parallel mechanism — is what
  makes PR 4's fork gate and PR 5's rejected-context addendum both fire
  correctly). Both destination conversations gain the new PR-comment tool
  so the agent can pull the actual review discussion on demand rather
  than the human transcribing it — this superseded an earlier "human
  pastes feedback as free text" sketch: transcription doesn't scale and
  the tool precedent (PR 6) already existed for exactly this shape of
  problem.
* Because reality can diverge from the tool regardless of which of the
  above happens first (someone merges the PR by hand outside the
  workbench, or a second reviewer requests changes after a first
  approved but before merge), `pr_review` needs to accept both
  resolutions from itself at any time — not a strict linear path — so
  the tool's state doesn't end up asserting something false. Wilder
  divergences (the PR gets deleted, force-pushed over by someone else)
  are not specially modeled; ordinary error handling on the next action
  attempted is expected to surface those rather than the state machine
  anticipating every possible external shenanigan.

## Schema changes (sketch — not binding, to be sharpened per-section)

**`task.yaml`** gains an optional `pull_request` field once a PR has been
opened:

```yaml
pull_request:
  url: https://github.com/org/repo/pull/123
  number: 123
  branch: task-exec/fix-login-bug/exec-001
```

Populated by the "Push & Open PR" action; left absent until then. The
`branch` field records which remote branch the PR actually tracks —
needed because a later execution attempt continuing after a rejection
cycle lands on a *different* local branch (ADR 0012 decision 1) and must
push onto this recorded branch via refspec, not its own name (see
mechanism section above). Unlike `reviews/review-NNN.yaml`/
`executions/exec-NNN.yaml`, this is **not** an append-only store — a task
has at most one open PR at a time by construction (a fresh PR only gets
created if this field is absent), so there's nothing to enumerate the way
multiple review verdicts or execution attempts can pile up.

**`internal/task/task.go`**: `StageComplete` → `StageMerged` (final name,
`pr_review`/`StagePRReview` naming resolved 2026-07-16 — see "Phasing").
Also needed for the same rename: `frontend/src/types.ts`'s `TaskStage`
union and `TaskKanbanBoard.tsx`'s `STAGES` array/label map both hardcode
stage names today and need the rename plus a new column for `pr_review`.

**`internal/task/lifecycle.go`**: `FinalizeReview`'s `approved` branch
targets `StagePRReview` instead of `StageComplete`/`StageMerged` — landed
in PR 2, not PR 1 (see "Phasing" decisions): retargeting `approved` before
there's a `pr_review` screen for a human to land on would break the
already-shipped approve flow, so PR 1 kept `approved` targeting the
renamed terminal stage (`StageMerged`) and PR 2 flips the target instead —
ahead of PR 3's routes/frontend, a deliberate accepted gap since the
system isn't run/deployed between those two PRs merging. PR 1's
stage-guard does widen to accept `StagePRReview` alongside `StageReview`
right away (for the reject path — see mechanism section), guarded so
`approved` specifically is rejected from `StagePRReview` (only valid from
`StageReview`). There is no `ReviseToImplementation` to model
the reject path on: `needs_changes` has always been handled inline inside
`FinalizeReview` itself, never a sibling function, so this milestone
shouldn't invent one either. Separately, a `MarkPRMerged` function moves
`StagePRReview` → `StageMerged` directly, with no review-record write,
requiring `Task.PullRequest != nil`.

## Out of scope

* **Knowledge-base promotion.** Split out of this milestone entirely
  during scoping, once probing its purpose turned up that (a)
  `data/knowledge/` doesn't have a single concept in it yet in this
  workspace, and (b) this project's own durable knowledge — `CLAUDE.md`,
  `docs/engineering conventions.md`, `CONTEXT.md`'s glossary, `docs/adr/`
  — already works fine, hand-authored, entirely without touching
  `internal/knowledge`. The purpose that *does* hold up: `data/knowledge/`
  is for what doesn't belong in any single project's own docs/ADRs but
  should inform any or all projects — a workspace-wide, cross-project
  layer, structurally distinct from a per-repo ADR folder the way
  Serena's memory tool defaults to per-project scoping with an explicit
  `global/`-prefix opt-in for cross-project memories (a validating
  reference, not a design to copy wholesale — Serena's `write_memory` has
  no human-approval gate, which is too weak a bar for this system's
  "humans own intent" invariant). Left deliberately unscoped and
  unnumbered (no `milestone9.md` yet) rather than provisionally slotted
  in, so a number doesn't imply a priority decision that hasn't been
  made. Needs its own dedicated `/grill-with-docs` session once there's a
  concrete answer to "what does this do that hand-written docs don't."
* **Two related gaps, surfaced during this milestone's scoping but not
  fixed here** — both real, both flagged so they aren't lost, both need
  their own follow-up session:
  * The system presumes a project's repository is already cloned at
    `AGENT_REPOS_ROOT` — `ResolveWorkspace`
    (`internal/agentrunner/runner.go:227-233`) errors if the local
    directory doesn't exist rather than cloning it. Whether the fix is a
    `remote` field on `Project`, or only a one-time "clone if absent"
    bootstrap, is still open.
  * Nothing checks that the shared checkout is on `main`/up to date
    before a GrillMe or Planning conversation runs against it. A task
    built against a stale or wrong-branch checkout is guesswork as to
    its validity against `main`. Needs at minimum a warning surfaced;
    exact mechanism undecided.
* **Auto-detecting PR status via polling, and any LLM-generated hint**
  toward "mark as merged" vs. "reopen implementation" vs. "reopen
  requirements" (e.g. recognizing an obviously-trivial fix and steering
  the human away from re-running the full GrillMe interview). The stage
  model is deliberately shaped so this can be added later purely as
  richer information surfaced on the existing `pr_review` screen, not a
  new stage or transition.
* **Non-GitHub git hosts.** GitHub only, via the `gh` CLI, for now — no
  provider abstraction for GitLab/Bitbucket/etc. There's no second real
  implementation yet to abstract against.
* **Credential/token management.** Relies entirely on `gh auth`/git
  credentials already configured on the host machine; this milestone
  never stores, requests, or handles a credential itself. Relatedly,
  `gh` being absent, `gh auth` having expired, or the push itself failing
  (branch protection rules, a non-fast-forward push) are known,
  unhandled-by-design risks — expected to surface as an ordinary action
  failure, not something this milestone tries to detect or work around in
  advance.
* **The GitHub-side merge itself.** Reaching `StageMerged` only records
  that a human has asserted the PR was merged — the workbench never
  clicks GitHub's merge button, opens the merge automatically, or
  performs the merge itself under any circumstance.

## Phasing

Delivered as sequential PRs, matching Milestone 6's cadence — each
independently reviewable and live-verifiable, rather than one large diff.
PR 1, PR 2, and PR 3 are scoped in detail so far (all via `/grill-with-docs`
sessions on 2026-07-16); later PRs will each get their own follow-up
session the way Milestone 6's PRs 3, 5, and 6 did.

* **PR 1 — Stage machinery for the PR cycle.** Backend-only in spirit,
  plus the one mechanical rename that has to land in lockstep with the
  frontend. Proven the way Milestone 6 PR 1 proved `RecordReview` before
  any UI existed: construct a fixture task already in `StagePRReview` and
  call the new functions directly. No HTTP routes, no `pr_review` UI, no
  `gh`/git push mechanics — those all wait for later PRs.

  Binding decisions:
  1. **Naming final.** `pr_review` / `StagePRReview` are the shipped
     names, not placeholders — they already match the existing
     short-lowercase-noun convention (`requirements`, `planning`,
     `implementation`, `review`) and stay unambiguous next to the
     existing internal `review` stage.
  2. **`StageComplete` → `StageMerged` ships fully in PR 1, backend and
     frontend together.** The wire value itself changes
     (`stage: complete` → `stage: merged`), so `internal/task/task.go`'s
     constant, both `review_test.go` files, `TaskDetailPanel.tsx`,
     `TaskKanbanBoard.tsx`, `frontend/src/types.ts`'s `TaskStage` union,
     and the frontend test fixtures hardcoding `'complete'`
     (`ReviewPanel.test.tsx`, `TaskDetailPanel.test.tsx`) all update
     together. This is the one piece of "frontend" PR 1 touches — pure
     rename, not new UI.
  3. **`FinalizeReview`'s `approved` branch keeps targeting the renamed
     terminal stage (`StageMerged`) in PR 1** — retargeting it to
     `StagePRReview` is deferred to PR 2 below. Retargeting now, before a
     `pr_review` screen exists to land a human on, would silently break
     the already-shipped approve flow (the frontend would receive a stage
     string it has no case for). (PR 2 does the retarget anyway, ahead of
     PR 3's routes/frontend — see PR 2's own notes on that tradeoff.)
  4. **`StagePRReview`, the widened `FinalizeReview` guard, and
     `MarkPRMerged` all ship in PR 1 as real, unit-tested machinery that
     stays unreachable through any live path** until PR 2 retargets
     `approved` (making `pr_review` live-reachable) and PR 3 ships the
     routes/frontend that let a human actually act on it. This mirrors
     how M6 PR 1 shipped a fully-working, unit-tested store and lifecycle
     months before any conversation or UI consumed it.
  5. **`FinalizeReview`'s widened guard explicitly rejects `decision ==
     approved` when `t.Stage == StagePRReview`** (new check, reusing
     `ErrWrongStage`). Without it, an `approved` verdict sent while
     already at `StagePRReview` would silently succeed as a same-stage
     no-op that still writes a spurious `reviews/review-NNN.yaml` entry —
     `approved` only ever makes sense from `StageReview`; `needs_changes`/
     `rejected` remain valid from both stages unchanged. (A broader audit
     of other "trust the caller" gaps like this one elsewhere in the
     codebase's lifecycle functions was spun off as a separate follow-up,
     not part of this milestone.)
  6. **`pull_request` lands in PR 1** as `*PullRequest` (pointer,
     `omitempty`) — `{URL, Number, Branch}` — `nil`/absent until "Push &
     Open PR" ships in a later PR. No producer yet; it exists purely so
     `MarkPRMerged` has something real to check. Pointer, not a value
     struct, so absence is an unambiguous `nil` check rather than a
     zero-value-struct ambiguity.
  7. **`MarkPRMerged` requires `t.PullRequest != nil`**, erroring
     otherwise — same defensive posture as decision 5, even though
     nothing populates the field until a later PR. Untestable
     end-to-end until then, but directly unit-testable against a fixture
     task with `PullRequest` set by hand.

* **PR 2 — Push & Open PR mechanism.** Scoped via a `/grill-with-docs`
  session on 2026-07-16. Backend-mechanism-only, same as PR 1: no HTTP
  routes, no frontend — those are deferred to PR 3 (below). Proven by
  direct unit/integration tests against fixture tasks already at
  `StagePRReview` (PR 1's technique), a local bare repo standing in for
  `origin` for the real git push mechanics, and a fake `GitHubPRClient`
  for the parts that would otherwise need real GitHub auth/network.

  Binding decisions:
  1. **`FinalizeReview`'s `approved` branch retargets `StageMerged` →
     `StagePRReview` in this PR** — a one-line change; the "approved only
     valid from `StageReview`" guard (PR 1 decision 5) stays correct
     unchanged, since once a task *is* at `StagePRReview`, `approved` is
     still correctly rejected there. **Deliberately not deferred to PR 3
     alongside the routes/frontend**, unlike PR 1's own retarget
     deferral: a live approval between PR 2 and PR 3 merging would land a
     task on `pr_review` with nothing yet to act on it — accepted, since
     the system isn't run/deployed in that window.
  2. **New `agentrunner.PushAndOpenPR(ctx, dir, newBranch, baseBranch,
     title, body string, existingNumber int, existingBranch string,
     client GitHubPRClient) (url string, number int, branch string, err
     error)`** — runs from the shared checkout (`ResolveWorkspace`), not a
     resolved execution/review worktree: a branch created via `git
     worktree add -b` is a ref in the same shared object database every
     worktree of that repo uses (established fact, not a new assumption —
     this is exactly what let M6 PR 6's `git show <branch>:<path>` work
     from the shared checkout for a branch checked out elsewhere), so
     pushing it by name needs no worktree lookup.
     - `existingNumber == 0` (no PR recorded yet): plain `git push origin
       newBranch`, then `client.Create(...)`.
     - `existingNumber != 0`: `client.State(existingNumber)` first — if
       not closed, refspec-push `newBranch:existingBranch` and reuse the
       existing URL/number (no new `Create` call); if closed, treat as if
       there were no existing PR (fresh push + `Create`, the returned
       values overwrite the stale record entirely).
  3. **The closed-PR check (decision 2's `client.State` call) is in
     scope for this PR**, not punted — a refspec push onto a closed PR's
     branch would succeed at the git level while GitHub silently doesn't
     reopen the PR, landing commits nobody's reviewing behind a success
     response. Worse than the "unhandled-by-design" risks already
     accepted for `gh auth`/non-fast-forward failures (an ordinary
     failure vs. a silent wrong-state success), and cheap to add behind
     the same fake-able seam.
  4. **New `GitHubPRClient` interface** (`Create(ctx, dir, head, base,
     title, body string) (url string, number int, err error)` and
     `State(ctx, dir string, number int) (state string, err error)`) —
     one interface for both operations, not two independent function
     types, since they're always used together on this one path and a
     single fake implements both for tests. Production implementation
     shells to real `gh` via argv (never a shell string, continuing ADR
     0013's discipline).
  5. **New `task.FileStore.RecordPullRequest(id string, pr PullRequest)
     (Task, error)`**, mirroring `MarkPRMerged`'s shape exactly: guards
     `t.Stage == StagePRReview` (reusing `ErrWrongStage`), sets
     `t.PullRequest`, no Stage change. Called uniformly for both the
     fresh-PR and refspec-continuity paths — the persisted shape is the
     same either way, so no special-casing belongs at the persistence
     layer.
  6. **Package boundary: `agentrunner` takes/returns plain strings, never
     `task.PullRequest`.** `agentrunner` has zero dependency on the
     `task` package today (confirmed, not assumed) and this PR keeps it
     that way — mirrors the existing `execution.go` pattern where
     `internal/api`'s handler is what glues `agentrunner`'s output into a
     `task` persistence call.
  7. **PR title = `t.Title`, falling back to `t.ID`** (matching the
     kanban board's existing `task.title || task.id` fallback). **Body =
     `t.Objective` plus the approving review's `Notes`** (if non-empty,
     under a short heading) **plus a plain marker that the workbench
     opened it** — giving an external reviewer the context a human would
     normally write into a PR description by hand, no templating beyond
     that.

* **PR 3 — HTTP routes and the `pr_review` frontend screen.** Scoped via
  a `/grill-with-docs` session on 2026-07-16. Wraps PR 2's
  `agentrunner.PushAndOpenPR`/`MarkPRMerged`/`FinalizeReview` reject path
  over HTTP and gives a human somewhere to actually click "Push & Open
  PR" for the first time — none of the underlying decision logic is new.

  Binding decisions:
  1. **`pr_review` has no agent conversation.** Unlike Review
     (`StageConversationPanel` + Draft/Finalize), the milestone's own
     mechanism section frames both `pr_review` resolutions ("mark as
     merged", reject) as a human reading external GitHub state and
     asserting a fact, not an agent judgment call — no LLM is in the
     loop here. The screen is a plain human-action panel.
  2. **Two new routes**: `POST .../pr/push` and `POST .../pr/merged`,
     both empty-body (fully server-derived per PR 2 decision 7 / task
     state) and both returning the plain updated `Task`. The reject path
     (`needs_changes`/`rejected` from `pr_review`) needs **no new route**
     at all — it's pure frontend wiring onto the existing
     `POST .../review/finalize`, which `task.FinalizeReview` already
     accepts from `StagePRReview` (PR 1/PR 2). The asymmetry between
     this plain-`Task` response shape and `finalizeReviewResponse`'s
     `{task, review}` wrapper is noted but deliberately not resolved here
     — `pull_request` is already fully embedded in `Task`, so there's
     nothing extra to carry the way a fresh review verdict is.
  3. **`agentrunner.PushAndOpenPR`'s signature drops the caller-supplied
     `baseBranch` parameter.** `ResolveWorkspace` (the shared checkout
     PR 2's mechanism runs from) returns only a directory, unlike
     `ExecutionWorkspace`/`ReviewWorkspace` which carry a `BaseBranch`
     field — but `baseBranch` is fully derivable from that same
     directory's current branch, the same fact `worktree.go` already
     relies on internally (twice, independently). Rather than duplicate that
     derivation a third time in `internal/api`, or leave it duplicated
     in-package, **a new `internal/gitutil` package** (`RunGit`,
     `CurrentBranch`) is introduced as the correct home for this
     generic-git-plumbing primitive — it doesn't conceptually belong in
     `agentrunner` (workspace resolution) any more than in `internal/api`
     (HTTP handlers). `PushAndOpenPR` calls `gitutil.CurrentBranch(ctx,
     dir)` itself instead of taking `baseBranch` as a parameter.
     `runGit`'s two existing independent implementations
     (`internal/agentrunner/worktree.go`, `internal/toolloop/tools_ref.go`)
     are both migrated onto `gitutil.RunGit` in this same PR, rather than
     leaving a third copy of the pattern behind.
  4. **`MarkPRMerged`'s "no pull_request recorded" guard now wraps
     `task.ErrWrongStage`** (`internal/task/lifecycle.go`), matching the
     stage guard directly above it, instead of a bare `fmt.Errorf` that
     fell through `writeMutationError`'s switch to a misleading 500.
     Unreachable/untestable over HTTP before this PR (PR 1 shipped it
     unit-tested-only); PR 3 is what exposes it through `POST .../pr/merged`
     for the first time, so getting its status code right now matters.
  5. **`TaskStore` gains `MarkPRMerged`/`RecordPullRequest`**, and
     `NewRouter` gains a `GitHubPRClient` parameter — a real one built in
     `cmd/server/main.go` via `agentrunner.NewGitHubPRClient()`, a fake in
     tests. Every existing `NewRouter` call site updates mechanically.
  6. **The `pr_review` screen is a single static layout**, not two
     conditionally-rendered states: the execution summary/diff (mirroring
     `ReviewPanel`), the "Push & Open PR" button, the PR link, the "Mark
     as merged" button, and the reject control are all always present,
     each enabled or disabled by whether `task.pull_request` is set —
     not mounted/unmounted based on it. (Every task normally *does* pass
     through `pr_review` with no PR yet, for however long it takes a
     human to click "Push & Open PR" — pushing is deliberately not
     automatic on arrival, the same `autoStart: false` reasoning as
     Review itself; this is expected steady-state, not an edge case.)
  7. **The reject control is a new, minimal component**, not a
     parameterized `ReviewDraftForm` — a 2-option decision select
     (`needs_changes`/`rejected`, omitting the `approved` option that's
     invalid from `pr_review`) plus a notes textarea. Simpler than
     `ReviewDraftForm`'s shape (no `propose_review` agent draft to
     receive; always human-initiated), so reusing it would only mean
     adding an "allowed decisions" prop to hide one option.
  8. **New `frontend/src/PRReviewPanel.tsx`**, rendered by
     `TaskDetailPanel` under a new `task.stage === 'pr_review'` branch.
     `pr_review` is added to `TaskStage`, `TaskKanbanBoard`'s `STAGES`
     array, and `STAGE_LABELS` (label: "PR Review"), positioned between
     `review` and `merged`. **No "Revise Plan" stage-action button for
     this stage** — the reject path already covers "go back"
     (`needs_changes` → implementation, `rejected` → requirements), so a
     parallel escape hatch would be redundant.
  9. **`TaskDetailPanel`'s `merged`-stage terminal screen copy is fixed**
     in this PR. It currently reads "Merge this branch by hand:
     `<branch>`" — correct for the pre-Milestone-7 world where `merged`
     (nee `complete`) was reached straight from an internal review
     approval, but wrong now that `merged` is only ever reached via the
     explicit "Mark as merged" action confirming a PR already merged on
     GitHub externally. Updated to link `task.pull_request.url` instead.
  10. **Testing follows `handleReviewDiff`'s existing precedent**: the
      push route's handler test uses a real local bare repo (standing in
      for `origin`) plus a fixture task/execution/review and a fake
      `GitHubPRClient`, with `ProjectStore`/`TaskStore` mocked — the same
      single test layer `handleReviewDiff` already uses for a handler
      that calls `agentrunner` directly against real git, rather than
      introducing a new narrower handler-only mock boundary.
      `PushAndOpenPR`'s own branch logic (fresh push / refspec-onto-
      existing / closed-PR-treated-as-fresh) stays covered exclusively by
      `pr_test.go`; the handler test only proves the wiring (project/repo
      resolution, task/execution/review lookups, response shape, 409 vs.
      500 mapping) is correct, not every branch again.

* **A later PR (not yet numbered/scoped in detail) — stage-conversation
  URL/actual-stage guard.** Surfaced by the same "trusts the caller" audit
  that motivated PR 1 binding decision 5: none of the five handlers in
  `internal/api/stage_conversation.go` (`handlePostStageMessage`,
  `handleStartStageConversation`, `handleGetStageConversation`,
  `handleDeleteStageMessage`, `handleRegenerateStageMessage`)
  cross-validate the URL's `stage` path segment against the task's actual
  current `Stage` — only that it names *a* valid stage at all
  (`stageTool()`'s check). A task at `implementation` can still be posted
  to via `.../stage/requirements/message`: the handler proceeds with the
  Requirements system prompt and `propose_context` tool, appends to that
  stale conversation, and can return a live Draft proposal over SSE that no
  longer means anything. Damage is bounded today (`FinalizeRequirements`/
  `FinalizePlan`/`FinalizeReview` still independently gate on the real
  `Stage` before anything can advance), but it lets a client pollute a
  "dead" stage's conversation file indefinitely. Scope for whoever picks
  this up: a 409 on mismatch, reusing `ErrWrongStage`
  (`internal/task/lifecycle.go`) for consistency with `Finalize*`/
  `Revise*`; confirm first that no frontend call site legitimately reads/
  posts to a stage other than the task's current one (assumed true —
  `GetConversation` is only ever called for the current stage from
  `TaskDetailPanel.tsx` — but not yet verified against every call site).

## Open questions for whoever executes this milestone

* Exact shape of the new PR-comment tool (name, arguments, what `gh`
  subcommand/`--json` fields it wraps, pagination/truncation) — deferred
  to a per-section grill session the way Milestone 6 PR 6's seven binding
  decisions were sharpened separately from its initial scoping mention.
## Follow-ups

Tracked here so they aren't lost between PRs; doesn't block Milestone 7's
remaining scope. (The stage-conversation URL/actual-stage guard finding
from the same audit is scoped as a future PR in "Phasing" above instead —
it's milestone-tracked work, not a standalone workbench Task.)

* **A dedicated testing-practices re-review for this project.** Surfaced
  while scoping PR 3's test strategy: precedent (`handleReviewDiff`'s
  handler test exercising real git rather than a narrower mocked
  boundary) was followed deliberately for PR 3 rather than re-litigated,
  but "that's what the codebase already does" was flagged as too weak a
  bar on its own — the same kind of gap a second-pass review (the Opus
  pass that caught PR 2's real mechanism gaps during initial scoping)
  is meant to catch before an unexamined pattern hardens into unquestioned
  convention. Not resolved here; needs its own dedicated look at this
  project's test-layering habits before they compound further.

* **`FinalizeReview`'s execution-lookup assumption is coupled to
  `RecordExecution`'s guard, not independently enforced.** The same audit
  confirmed `FinalizeReview`'s "no new execution can have been recorded
  since Review" comment (`internal/task/lifecycle.go:127-132`) holds today
  only because `RecordExecution` separately guards success writes to
  `StageImplementation` (`internal/task/execution.go:180-198`) — true by
  construction across two functions, not verified by either one. No bug
  today; not yet worth its own task, but worth a second look if either
  function changes. Noted here rather than filed, so it isn't forgotten.

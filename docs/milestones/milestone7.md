# Milestone 7 — Merge and PR Cycle

**Status: Scoping (2026-07-16)** — general shape scoped via a
`/grill-with-docs` session on 2026-07-15, then independently reviewed
against the codebase by a second model pass, which caught two real
mechanism gaps (both now folded in: the review-record reuse for
`pr_review`'s reject actions, and the refspec-push approach for PR
continuity across a rejection cycle — see "The push/PR/rejection
mechanism" and "Schema changes"). **PR 1 scoped via a follow-up
`/grill-with-docs` session on 2026-07-16** — see "Phasing" below;
`pr_review`/`StagePRReview` naming is now final, not a placeholder.
Remaining per-section detail (exact tool shapes, HTTP routes, frontend
wiring for later PRs) is still to be sharpened in further follow-up
sessions the way Milestone 6's PRs 3, 5, and 6 each were. **Knowledge-base
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
**eventually** targets `StagePRReview` instead of `StageComplete`/
`StageMerged` — but not in PR 1 (see "Phasing" decision 3): retargeting
`approved` before there's a `pr_review` screen for a human to land on
would break the already-shipped approve flow, so PR 1 keeps `approved`
targeting the renamed terminal stage (`StageMerged`) and only a later PR,
landing together with "Push & Open PR" and its frontend, flips the
target. PR 1's stage-guard does widen to accept `StagePRReview` alongside
`StageReview` right away (for the reject path — see mechanism section),
guarded so `approved` specifically is rejected from `StagePRReview` (only
valid from `StageReview`). There is no `ReviseToImplementation` to model
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
Only PR 1 is scoped in detail so far (via a `/grill-with-docs` session on
2026-07-16); later PRs will each get their own follow-up session the way
Milestone 6's PRs 3, 5, and 6 did.

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
     `StagePRReview` is deferred to the later PR that ships "Push & Open
     PR" together with its frontend. Retargeting now, before a
     `pr_review` screen exists to land a human on, would silently break
     the already-shipped approve flow (the frontend would receive a stage
     string it has no case for).
  4. **`StagePRReview`, the widened `FinalizeReview` guard, and
     `MarkPRMerged` all ship in PR 1 as real, unit-tested machinery that
     stays unreachable through any live path** until the later PR (3)
     defers to exists. This mirrors how M6 PR 1 shipped a fully-working,
     unit-tested store and lifecycle months before any conversation or
     UI consumed it.
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
* The refspec-push approach (mechanism section) handles the common case
  of landing a new attempt on an existing open PR's branch, but not what
  happens if that PR was closed (not merged) on GitHub by a human in the
  meantime — pushing to its branch wouldn't reopen it. Needs a decision:
  detect that and open a fresh PR, or surface an error for the human to
  resolve on GitHub directly.
* Exact HTTP routes and frontend surface for the new `pr_review` screen
  and its actions — likely following `ReviewPanel`'s shape (Milestone 6
  PR 3) but not designed here.
* Which later PR actually retargets `FinalizeReview`'s `approved` branch
  to `StagePRReview` and ships the "Push & Open PR" action + its
  frontend — not split into its own PR boundary yet (Phasing above only
  scopes PR 1).

## Follow-ups

Tracked here so they aren't lost between PRs; doesn't block Milestone 7's
remaining scope. (The stage-conversation URL/actual-stage guard finding
from the same audit is scoped as a future PR in "Phasing" above instead —
it's milestone-tracked work, not a standalone workbench Task.)

* **`FinalizeReview`'s execution-lookup assumption is coupled to
  `RecordExecution`'s guard, not independently enforced.** The same audit
  confirmed `FinalizeReview`'s "no new execution can have been recorded
  since Review" comment (`internal/task/lifecycle.go:127-132`) holds today
  only because `RecordExecution` separately guards success writes to
  `StageImplementation` (`internal/task/execution.go:180-198`) — true by
  construction across two functions, not verified by either one. No bug
  today; not yet worth its own task, but worth a second look if either
  function changes. Noted here rather than filed, so it isn't forgotten.

# Milestone 7 — Merge and PR Cycle

**Status: Scoping (2026-07-15)** — general shape scoped via a
`/grill-with-docs` session on 2026-07-15; per-section detail (exact tool
shapes, HTTP routes, frontend wiring) to be sharpened in follow-up
`/grill-with-docs` sessions the way Milestone 6's PRs 3, 5, and 6 each
were. **Knowledge-base promotion, previously bundled into this milestone
by Milestone 6's "Out of scope" section, has been split out** — see "Out
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
* **Two human-recorded resolutions from `pr_review`**:
  * **"Mark as merged"** — a human assertion, no GitHub polling in this
    milestone — advances to `StageMerged`.
  * **Reject** — the human chooses to reopen `requirements` or
    `implementation`, mirroring Review's existing `rejected`/
    `needs_changes` split (`FinalizeReview`, `internal/task/lifecycle.go`)
    rather than inventing a new decision shape. Reopening to
    `implementation` reuses Milestone 6 PR 4's continuation mechanics
    as-is — the fresh execution attempt forks from the prior attempt's
    branch tip (ADR 0012) — since the underlying problem (a task whose
    branch needs more work before it can land) is the same one PR 4
    already solved for internally-rejected reviews.
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
Clicking it pushes the execution's branch and runs `gh pr create`,
recording the resulting URL/number onto the task.

From `pr_review`, the task sits until a human records what actually
happened on GitHub — there is no live polling or webhook integration in
this milestone (see "Out of scope"). Two paths forward:

* **Merged** — a "Mark as merged" action moves `Stage` to `StageMerged`
  directly; there is no separate "PR approved but not yet merged"
  waypoint. That waypoint was in an earlier draft of this scoping pass
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
* **Rejected / changes requested** — the human picks a destination,
  `requirements` or `implementation`, the same two destinations Review's
  own `rejected`/`needs_changes` already use internally. Both destination
  conversations gain the new PR-comment tool so the agent can pull the
  actual review discussion on demand rather than the human transcribing
  it — reason this superseded an earlier "human pastes feedback as free
  text" sketch: transcription doesn't scale and the tool precedent (PR 6)
  already existed for exactly this shape of problem.
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
```

Populated by the "Push & Open PR" action; left absent until then. Unlike
`reviews/review-NNN.yaml`/`executions/exec-NNN.yaml`, this is **not** an
append-only store — a task has at most one open PR at a time by
construction (a fresh PR only gets created if this field is absent), so
there's nothing to enumerate the way multiple review verdicts or
execution attempts can pile up.

**`internal/task/task.go`**: `StageComplete` → `StageMerged`, plus the
new `StagePRReview` (or better name — open question below) constant.

**`internal/task/lifecycle.go`**: `FinalizeReview`'s `approved` branch
now targets `StagePRReview` instead of `StageComplete`. New functions
alongside `ReviseToRequirements`/`ReviseToPlanning`/
`ReviseToImplementation`, valid only from `StagePRReview`, for the two
rejection destinations, and a `MarkPRMerged`-shaped function moving
`StagePRReview` → `StageMerged`.

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
  never stores, requests, or handles a credential itself.
* **The GitHub-side merge itself.** Reaching `StageMerged` only records
  that a human has asserted the PR was merged — the workbench never
  clicks GitHub's merge button, opens the merge automatically, or
  performs the merge itself under any circumstance.

## Open questions for whoever executes this milestone

* Naming: `pr_review` / `StagePRReview` are working names from this
  scoping session, not final — worth a naming pass alongside the
  `StageMerged` rename.
* Exact shape of the new PR-comment tool (name, arguments, what `gh`
  subcommand/`--json` fields it wraps, pagination/truncation) — deferred
  to a per-section grill session the way Milestone 6 PR 6's seven binding
  decisions were sharpened separately from its initial scoping mention.
* Whether "Push & Open PR" needs idempotency handling beyond "the
  `pull_request` field is already set, so push more commits to the
  existing branch/PR instead of calling `gh pr create` again" — e.g. what
  happens if that existing PR was closed (not merged) on GitHub by a
  human in the meantime.
* Exact HTTP routes and frontend surface for the new `pr_review` screen
  and its actions — likely following `ReviewPanel`'s shape (Milestone 6
  PR 3) but not designed here.
* Whether `MarkPRMerged`/the rejection-destination functions need any
  validation beyond "task is currently in `StagePRReview`" — e.g.
  requiring `pull_request` to already be set.

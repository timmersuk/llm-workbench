# Milestone 6 — Review

**Status: Scoping (2026-07-12)** — original design scoped via a
`/grill-with-docs` session on 2026-07-09; re-validated against `main` after
Milestone 8 shipped. Every code anchor below still holds (verified 2026-07-12:
`stageTool`'s two-case switch, `ExecutionInput{PlanRef, ContextRefs}`,
`Context.Verification []string`, the `Finalize*/Revise*` shapes, and
`StageComplete` still defined-but-unused — M6 remains its first user). Two
scoping decisions taken 2026-07-12:

* **Automated-checks test-running** (was open question #2) → **reuse Milestone
  8's cwd-confined `bash` tool**. The review conversation runs as a
  `internal/toolloop` loop with `bash` confined to the execution worktree, so
  the agent runs the project's test command itself — no bespoke test-runner
  machinery. M8's `Execute` path already proves this shape end-to-end.
* **Delivery** → **phased, matching Milestone 8's PR cadence** (see "Phasing"
  below), not one large diff.

## Why now

Milestone 5 shipped Execute, which leaves every successfully-executed
task sitting at `stage: review` with nothing there to greet it.
`RecordExecution` (`internal/task/execution.go:206-212`) already performs
the `implementation → review` auto-advance, and
`internal/agentrunner/worktree.go`'s `ResolveExecutionWorkspace` doc
comment already anticipates this milestone directly: the worktree is
"left in place... so a future Review-stage UI can read its diff."
`docs/vision.md`'s founding narrative ("I review the branch... Tests
pass... I merge") stops being true right at this point today —
`TaskDetailPanel.tsx:211` currently routes `stage: review` to the same
`ExecutePanel` as `implementation`, and `StageComplete`
(`internal/task/task.go:19`) has been a defined-but-unused constant since
Milestone 1.

Unlike Execution (Milestone 5), which had to build a whole new capability
(write-enabled autonomous agents in isolated worktrees), Review only
*extends* machinery that already exists: the Conversation → Draft →
Finalize loop GrillMe and Planning Mode already use, and the
worktree/diff primitives Execution already produces. That's why Review is
in scope this milestone and merge/knowledge-promotion (see "Out of
scope") are not — those two have no existing machinery to extend.

## Introduces

* AI-assisted review of a completed execution — automated checks, test
  meaningfulness validation, and per-verification-step confirmation, all
  conducted conversationally over the same Conversation → Draft →
  Finalize mechanism GrillMe/Planning Mode already use
* structured verification steps in `context.yaml` (agent-executable vs.
  human-judgment)
* `reviews/review-NNN.yaml`, append-only like `executions/exec-NNN.yaml`,
  and the three-way `approved | rejected | needs_changes` decision, each
  wired to a real stage transition — including the first thing to ever
  reach `stage: complete`
* `ReviewPanel`, replacing `ExecutePanel` for `stage: review` in
  `TaskDetailPanel`

## The review mechanism

Review is conversational, using the exact same Conversation → Draft →
Finalize shape as GrillMe/Planning Mode (see `CONTEXT.md`'s **Review**
entry), not a bespoke approve/reject form. A human explicitly starts it —
arriving at `stage: review` shows the execution's diff and commit
summary but runs no checks on its own — then the review conversation
proceeds through three phases the agent works through, which the human
can interrupt or discuss at any point:

1. **Automated checks**: run the project's test suite and a Standards +
   Spec code-review-style pass over the diff (the same two axes the
   `/code-review` skill already applies interactively — this phase runs
   it as part of the agent's own turn instead).
2. **Test-meaningfulness validation**: the agent looks at what the tests
   in the diff actually assert, not just whether they pass — e.g.
   flagging a test that can't fail, or one that doesn't touch the code
   path it claims to cover.
3. **Per-verification-step confirmation**: `context.yaml`'s
   `verification: []` list (see schema change below) is walked entry by
   entry — `agent_executable` entries are attempted by the agent itself
   (hit an endpoint, run a command, drive a UI check) and reported;
   `human_judgment` entries are left for the human to perform, with the
   agent only recording their confirmation.

A failing automated check is never auto-rejected — per the "humans own
intent" invariant (`docs/architectural invariants.md`), it's surfaced as
findings inside the conversation, and the human decides the outcome via
the ordinary Draft/Finalize step below.

### Wiring into the existing stage-conversation machinery

The HTTP routes (`internal/api/router.go:98-102`,
`.../tasks/{taskId}/stages/{stage}/...`) are already generic over
`stage` — no new routes needed for the conversation itself. Three
existing stage-name switches need a `review` case added:

* `validateConversationStage` (`internal/task/conversation.go:56-61`) —
  widen from `StageRequirements`/`StagePlanning` to also accept
  `StageReview`, so `conversation-review.yaml` persists the same way
  `conversation-requirements.yaml`/`conversation-planning.yaml` do.
* `stageTool` (`internal/api/stage_conversation.go`) — a new
  `case task.StageReview` returning the new `propose_review` tool (see
  Draft tool, below).
* `buildStagePrompt` (`internal/api/stage_conversation.go`) — a new
  review system prompt (a `reviewSystemPrompt` constant alongside
  `grillMeSystemPrompt`/`planningModeSystemPrompt`) encoding the
  three-phase discipline above, plus the diff/commit summary and the
  structured verification-step list so the agent has something concrete
  to check off.

A "Start Review" action (human-triggered, per the decision above) is the
first thing shown on arrival at `stage: review`, before the automated-
checks phase runs — reusing `StageConversationPanel`'s existing "start"
affordance rather than inventing a separate button.

### Draft tool: `propose_review`

New to `internal/drafttool/drafttool.go`, following `ProposeContext`/
`ProposePlan`'s exact shape (`Definition{Name, Description, Schema}`,
added to `All()`):

```go
const ProposeReviewName = "propose_review"

var proposeReviewSchema = json.RawMessage(`{
  "type": "object",
  "properties": {
    "decision": {"type": "string", "enum": ["approved", "rejected", "needs_changes"]},
    "notes": {"type": "string"}
  },
  "required": ["decision", "notes"]
}`)

// ProposeReview is the Review-stage Draft tool definition.
var ProposeReview = Definition{
	Name:        ProposeReviewName,
	Description: "Propose this execution's review outcome (decision + notes) for the human to review before Finalize.",
	Schema:      proposeReviewSchema,
}
```

`All()` (`drafttool.go`) returns `{ProposeContext, ProposePlan,
ProposeReview}` — both call sites (`internal/api/stage_conversation.go`'s
`stageTool` for in-process Claude/local runners, and `cmd/draftmcp` for
Codex's external MCP server) stay in lockstep automatically, since
`cmd/draftmcp` already iterates `drafttool.All()` generically rather than
listing tools by name.

### Schema changes

**`context.yaml`'s `verification: []`**, currently `[]string`
(`internal/task/context.go:19`), becomes a list of structured entries:

```yaml
verification:
  - description: "Hitting POST /api/v1/... returns 200 with the new field"
    kind: agent_executable   # agent_executable | human_judgment
  - description: "The new empty-state copy reads naturally in context"
    kind: human_judgment
```

`RequirementsDraft.Context.Verification`, `drafttool.ProposeContext`'s
schema, and `FinalizeRequirements`'s `trimmedList(draft.Context.Verification)`
call (`internal/task/lifecycle.go:39`) all need updating together — GrillMe
is still the thing that produces this list; Review is just the first
consumer of `kind`. See `docs/adr/0008-structure-context-verification-entries.md`.

**`execution.yaml`'s `input:` block** (`internal/task/execution.go:46-49`,
`ExecutionInput{PlanRef, ContextRefs}`) gains `ReviewFeedback string`
(`review_feedback` in YAML/JSON) — populated only when an execution is
primed by `needs_changes` feedback from a prior review (see decision
mapping below), empty otherwise.

**`reviews/review-NNN.yaml`** — new, append-only, mirroring
`executions/exec-NNN.yaml` exactly (`internal/task/execution.go:97-147`):
a `reviewsDir`/`reviewPath` pair, a `NextReviewID` scanner, and
`RecordReview`/`ListReviews` following `NextExecutionID`/
`RecordExecution`/`ListExecutions`'s established shape file-for-file. New
`internal/task/review.go`. Field-for-field per the schema doc's shape:

```yaml
review_id: review-001
task_id: fix-login-bug
decision: approved | rejected | needs_changes
notes: ""
created_at: 2026-07-09T00:00:00Z
```

A task that cycles `review → implementation (needs_changes) → review`
again simply records a new `review-NNN.yaml` each time — the full history
of every verdict across cycles is a first-class queryable fact, the same
way every execution attempt already is.

### The three-way decision, wired to lifecycle transitions

`FinalizeReview` (new, `internal/task/lifecycle.go`, alongside
`FinalizeRequirements`/`FinalizePlan`) records a `reviews/review-NNN.yaml`
via `RecordReview` and then branches on `decision`:

* **`approved`** → `Stage = StageComplete`. Terminal for this milestone —
  no merge action (that's Milestone 7). This is the first thing to ever
  reach `StageComplete`.
* **`needs_changes`** → a new `ReviseToImplementation(id, reviewFeedback string)`
  (`internal/task/lifecycle.go`, alongside `ReviseToRequirements`/
  `ReviseToPlanning`), valid only from `StageReview`, moves `Stage` back
  to `StageImplementation`. Unlike `ReviseToRequirements`/`ReviseToPlanning`
  (which just flip `Stage` and let the human re-run the existing stage
  Conversation), this Revise also carries the review's `notes` forward
  into the *next* execution attempt's `input.review_feedback` field —
  there's no per-execution Conversation to resume the way Requirements/
  Planning have, so the feedback has to travel through `execution.yaml`
  itself. The execute-triggering handler needs to thread this into the
  executor's prompt the same way `plan_ref`/`context_refs` already seed
  it.
* **`rejected`** → reuse `ReviseToRequirements` (`internal/task/lifecycle.go:92-108`)
  completely as-is — no new lifecycle code. The requirements/plan
  themselves were wrong, not just the implementation, so this reopens the
  same GrillMe Conversation Milestone 4 built. The one gap:
  `ReviseToRequirements` today only flips `Stage`, with no way for the
  reopened requirements conversation to see *why* it's being reopened.
  Proposed fix: `buildStagePrompt`'s `StageRequirements` case checks for
  the most recent `reviews/review-NNN.yaml` and, if its `decision` is
  `rejected`, prepends its `notes` to the system prompt as prior review
  context — no schema change needed, just reading an artifact that's
  already there once Review has run once.

A manual **"spin off a followup task"** escape hatch needs no new
machinery: ordinary task creation already exists, and a human can do this
at any point regardless of the formal decision value. Not built here.

**Existing, unrelated to the above, worth knowing before touching this
code**: `ReviseToPlanning` (`internal/task/lifecycle.go:110-132`) is
*already* valid from both `implementation` and `review`, and
`TaskDetailPanel.tsx:211-217` already renders a "Revise Plan" button for
both stages. This predates Milestone 6 — it's a pre-existing manual
override (the plan itself needs rework, independent of a formal review
verdict), not what `rejected`/`needs_changes` map to, just a parallel path
a human already has. Don't reinvent it.

### Frontend: `ReviewPanel`

New `frontend/src/ReviewPanel.tsx`, following `GrillMePanel.tsx`/
`PlanningModePanel.tsx`'s ~40-line wrapper shape exactly: wraps
`StageConversationPanel<ReviewDraft>` with `stage="review"`, an
`emptyDraft`, a `renderDraft` (a new `ReviewDraftForm.tsx`, mirroring
`RequirementsDraftForm.tsx`/`PlanDraftForm.tsx` — a decision `<select>`
plus a notes textarea), and `onFinalize` calling a new
`finalizeReview(projectId, taskId, draft)` API function hitting a new
`POST .../tasks/{taskId}/review/finalize` route (`handleFinalizeReview`,
`internal/api/finalize.go`, same bespoke per-artifact-type shape as
`handleFinalizeRequirements`/`handleFinalizePlan` — no generic Finalize
introduced here, consistent with today's pattern).

Before the conversation starts, `ReviewPanel` also needs the diff/commit
summary to show. `CollectExecutionOutput`
(`internal/agentrunner/worktree.go:82-103`) already returns `commits` and
`artifacts` (changed file paths) via `git diff --name-only`; this
milestone adds a full-patch variant (same function shape, dropping
`--name-only` so it returns the actual patch text) for `ReviewPanel` to
render.

`TaskDetailPanel.tsx:211` currently groups `implementation` and `review`
together only for the "Revise Plan" button, while a separate condition is
the only thing that renders `ExecutePanel` for `stage === 'implementation'`.
The insertion point is a new `task.stage === 'review'` block rendering
`ReviewPanel`, sitting alongside the existing "Revise Plan" affordance,
not replacing it.

## Out of scope

* **Merging the execution branch into the base branch.** No merge helper
  exists anywhere in `internal/agentrunner` — `approved` stops at
  `stage: complete` this milestone, leaving the worktree/branch in place
  exactly as Milestone 5 already does for every execution, human-mergeable
  by hand. Building merge automation now would mean inventing
  conflict-handling policy with no existing code to build on, unlike
  Review, which only extends machinery Milestones 4 and 5 already
  shipped. Deferred to Milestone 7 (`docs/milestones/milestone7.md`).
* **Knowledge-base promotion** (folding a completed task's learnings into
  the Knowledge layer). `internal/knowledge/knowledge.go` is deliberately
  read-only today (`FileReader.Get(conceptID)` only, doc comment: "no
  Create/Update/List/index") and `docs/knowledge schema v0.md` §6
  confirms no Go-side store exists yet. Promotion needs a write path that
  doesn't exist, the same reason merge is deferred. Deferred to
  Milestone 7.

## Phasing

Delivered as sequential PRs, matching Milestone 8's cadence — each
independently reviewable and live-verifiable, rather than one large diff:

* **PR 1 — Review backend + lifecycle. ✅ Shipped (#23).** The append-only
  `reviews/review-NNN.yaml` store (`internal/task/review.go`:
  `RecordReview`/`ListReviews`/`NextReviewID`, mirroring the execution
  store), `FinalizeReview` and the three-way
  `approved | rejected | needs_changes` → stage-transition wiring. The
  first thing to ever reach `StageComplete`. No conversation or UI yet —
  proven by unit tests over the lifecycle transitions. (The `context.yaml`
  `verification` schema change and `ReviseToImplementation`/the
  `execution.yaml` `review_feedback` field, originally slotted here, moved
  out: the schema change is inseparable from its frontend authoring form
  and its Review consumer, so it landed in PR 2; the execute-retrigger
  plumbing is deferred to its own later PR — `needs_changes` notes already
  persist in `review-NNN.yaml`.)
* **PR 2 — Review conversation. ✅ Shipped.** The `context.yaml`
  `verification: []` schema migration (`[]string` → structured
  `{description, kind}` entries, `docs/adr/0008`) across backend and
  frontend together, the `propose_review` Draft tool (`internal/drafttool`,
  added to `All()` so `cmd/draftmcp` picks it up for free), the three
  `stage`-switch cases (`validateConversationStage`, `stageTool`,
  `buildStagePrompt`), and the `reviewSystemPrompt` encoding the three-phase
  discipline. The automated-checks phase drives a `toolloop` loop with the
  M8 `bash` tool (test command) plus the read-only toolset (Standards/Spec
  pass over the diff), confined to the execution worktree
  (`ResolveReviewWorkspace`). Full-patch variant of `CollectExecutionOutput`
  (`CollectExecutionPatch`) so the prompt carries the actual diff.
  Verified end-to-end through the real router/FileStore/git chain with a
  faked model, matching M8's bar as closely as a model-less environment allows.
* **PR 3 — ReviewPanel frontend.** Design sharpened via a
  `/grill-with-docs` session on 2026-07-14; the six decisions below are
  binding on whoever executes it.

  Frontend: `ReviewPanel.tsx` + `ReviewDraftForm.tsx` (a decision
  `<select>` + notes `<textarea>`, mirroring the GrillMe/Planning
  wrappers), the `finalizeReview` / `getReviewDiff` / `listReviews` API
  functions, and the `TaskDetailPanel` `stage === 'review'` **and**
  `stage === 'complete'` insertion points alongside the existing "Revise
  Plan" affordance. Backend: three small routes — `handleFinalizeReview`
  (`POST .../review/finalize`, returning `{task, review}`),
  `GET .../reviews` (wrapping the existing `ListReviews`), and
  `GET .../review/diff` (wrapping the existing `CollectExecutionPatch`).

  Binding decisions:
  1. **Diff display (A-lite).** The pre-conversation view renders a
     summary from the existing `executions` list (branch, commit list,
     changed-file paths) plus a collapsed `<details>` "View diff" that
     renders the raw patch (`GET .../review/diff` → `CollectExecutionPatch`)
     in a `<pre>`, no syntax highlighting. The full patch is thus visible
     in-app on demand, not just fed to the agent's prompt.
  2. **Explicit start (no auto-run).** `StageConversationPanel` gains an
     `autoStart?: boolean` prop (default `true`, preserving GrillMe/
     Planning behaviour); `ReviewPanel` passes `false`. The "Start Review"
     affordance lives *inside* `StageConversationPanel` (shown when empty-
     and-not-yet-started instead of auto-firing, keeping start logic where
     `startConversation` already resolves model/executor), with a
     configurable button label. Rationale: Review's first phase runs the
     real test suite + a code-review pass — real compute that must not
     fire on every panel mount/reload.
  3. **`complete` screen.** A new `stage === 'complete'` block in
     `TaskDetailPanel` (inline, not a heavyweight panel): an "approved"
     confirmation, the verdict notes, and the execution branch name framed
     as "merge this branch by hand" (no merge automation exists until M7).
     This is the first screen anything ever reaches `complete` for.
  4. **Terminal record survives reload.** `handleFinalizeReview` returns
     `{task, review}` for the just-approved case; `GET .../reviews` (mirror
     of `handleListExecutions`) lets a re-visited `complete` task re-read
     its latest verdict notes rather than showing them blank.
  5. **Propose-first Finalize.** No bespoke always-visible approve/reject
     form — Finalize unlocks only after the agent calls `propose_review`,
     exactly as GrillMe/Planning gate Finalize on their draft tool. The
     agent's reasoning arrives pre-populated in the notes field for the
     human to edit.
  6. **Routing by stage.** `onFinalized(task, review)` just calls
     `setTask(task)`; `TaskDetailPanel` re-routes by the new stage across
     all three outcomes (`complete` / `implementation` / `requirements`) —
     no per-decision special-casing in the panel.

* **PR 4 — Review-cycle prompt plumbing (tracked deferral, not yet
  scheduled).** Two backend behaviours deliberately deferred out of PR 3
  so it stays "review UI + minimal routes." Neither is optional long-term
  — they close the `needs_changes`/`rejected` loops end-to-end:
  * **Execute-retrigger feedback threading.** `needs_changes` sends a task
    back to `implementation`; the review's notes must reach the *next*
    execution attempt's prompt via `input.review_feedback`. Notes already
    persist in `review-NNN.yaml`, so nothing is lost meanwhile — PR 4 wires
    them into the executor prompt the way `plan_ref`/`context_refs` already
    seed it (see "Open questions" below for the exact wire shape).
  * **`rejected` → requirements prompt enrichment.** `buildStagePrompt`'s
    `StageRequirements` case should read the most recent `rejected`
    `review-NNN.yaml` and prepend its notes, so the reopened GrillMe
    conversation knows *why* it was reopened. No schema change — just
    reading an artifact that already exists once Review has run.

## Open questions for whoever executes this milestone

* Exact wire shape for how `ReviseToImplementation`'s `reviewFeedback`
  reaches the next `Execute` call's prompt — whether it's injected into
  the executor's system prompt alongside `plan_ref`/`context_refs` (the
  leaning) or handled some other way. A PR 1 implementation detail,
  sketched above but not fully specified.

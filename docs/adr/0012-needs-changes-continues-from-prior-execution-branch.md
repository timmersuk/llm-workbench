# `needs_changes` re-execution forks from the prior attempt's branch tip, not `main`

Every execution attempt gets its own isolated git worktree/branch
(`ResolveExecutionWorkspace`, `internal/agentrunner/worktree.go`), forked
from `main` today regardless of why the task is back at `implementation`.
For a `needs_changes` review verdict specifically, we decided the retry
should instead fork its new worktree/branch from the *prior* execution's
branch tip — so the agent lands in a workspace that already contains what
it built last time, rather than a blank checkout of `main`.

We considered threading the prior execution's diff into the new attempt's
system prompt instead (reusing `CollectExecutionPatch`, already built for
Review) and rejected it: it asks the model to read a unified diff and
mentally reconstruct file state before it can make an edit, which is both
more token-hungry and less reliable than letting it `read_file`/`grep`
a workspace that's already in that state. We also considered literally
resuming the *same* worktree/branch across attempts (no new git objects at
all) and rejected that too — it would break `ResolveReviewWorkspace`'s
convention of reconstructing a worktree path deterministically from
`executionID` (one worktree per attempt), and blur the audit boundary
between what one attempt did versus the next.

`ExecutionWorkspace.BaseBranch` (what `CollectExecutionOutput`/
`CollectExecutionPatch` diff against for Review) stays decoupled from
what the worktree's branch is actually *forked from* — a `needs_changes`
retry forks from the prior execution's branch but still records/diffs
against `main`, so Review's diff machinery needed zero changes to support
this.

Scoped to `needs_changes` only: `rejected` still forks fresh from `main`
(the plan/requirements are being redone from scratch), gated on a fresh
lookup of the latest review's decision at execute-time, not a persisted
flag.

**Correction (2026-07-15):** the paragraph above originally read "which
branch to fork from is resolved the same way `buildReviewContext` already
finds 'the execution under review' — the last entry of `ListExecutions` —
rather than adding an explicit `execution_id` field to `Review`," with the
safety argument that this holds only because the state machine never
allows two executions in flight for one task at once. That argument missed
a real case: `RecordExecution` only advances `Stage` to `review` on
success, so a `needs_changes` retry that itself *fails* is still recorded
under `executions/` without ever producing a new review — at that point
`ListExecutions`' last entry is that failed retry, not the execution the
latest review actually reviewed, and a further retry would fork from (and
carry stale feedback about) the wrong attempt. An independent structural
review of the shipped code caught this as a real, reachable, previously
untested bug. Fixed by doing the thing this ADR originally talked itself
out of: `Review` now has an `execution_id` field (`docs/task schema
v0.md`), set by `FinalizeReview` at the one moment the link is
unambiguous — the task is confirmed at `stage: review` right there, and no
new execution can have been recorded since. `resolveReviewContinuation`
(`internal/api/execution.go`) now reads this field directly instead of
inferring it from `ListExecutions`' last entry.

# Milestone Orphans — swept-up follow-ups and unscoped gaps

**Status: Tracking doc, not a build plan.** Not a numbered milestone —
nothing here is scoped into PRs, and nothing here is claimed to be blocking
any other work. This is a single place to land every "deferred, not
blocking this milestone" item and every silently-dropped gap found across
the project's closed-out milestone docs, so they stop being scattered
across `docs/milestones/done/*.md` (and, in one case, not tracked
anywhere at all). Each entry below replaces a mention that used to live
only in its origin milestone doc; that doc now points here instead.

Swept in on 2026-07-17, prompted by finding that Milestone 7 itself closed
out with two open sections (its own "Follow-ups", and two items nested in
its "Out of scope") that had no future milestone or backlog task attached —
the same pattern an audit then found repeated in Milestones 2, 3, 4, and 5.

Milestone 6's knowledge-base promotion item was initially left out of this
sweep on the theory that it wasn't a silent drop — M6 deferred it to M7,
and M7's own scoping re-examined and re-deferred it again, on the record.
That theory was wrong: checking for a backlog task stub the way
`git-backed-storage`/`resumable-chat-streams`/`human-executor` each have
found none — the item only ever existed as each milestone doc pointing at
the next one (M6 → "deferred to M7", M7 → "needs its own session", M8 →
"still `docs/milestones/milestone7.md`"), a chain with no anchor at the
end. Structurally identical to M4's `ask_question` tool below. Swept in
here for the same reason.

---

## From Milestone 2 — Git-backed task/project storage

Tasks and projects persist as plain files under `data/` (`WORKSPACE_ROOT`),
read/written directly via `os.ReadFile`/`os.WriteFile` — no version
history, no `.git/` of their own. Milestone 2 deliberately walked back an
earlier "git-backed" framing (`docs/project_summary.md`, `CLAUDE.md`) to
this simpler flat-file `FileStore`, deferring the git-backed version.

Already has a backlog stub:
`data/projects/llm-workbench/tasks/git-backed-storage/task.yaml`
(`status: draft`, `stage: requirements`, created 2026-07-06). Never
scheduled into any milestone or PR since. Confirmed still true in code —
`internal/task/store.go` and its siblings (`execution.go`, `review.go`,
`context.go`, `conversation.go`, `plan.go`) are all plain-file I/O; no git
library anywhere in the repo.

## From Milestone 3 — Chat streams don't survive a refresh

Milestone 3's streaming design relays a completion live over a single HTTP
response per browser tab; nothing about an in-progress generation is held
anywhere once that response is written. A refresh, dropped connection, or
navigation away mid-stream loses the in-progress response with no way to
reconnect and resume or replay it.

Already has a backlog stub:
`data/projects/llm-workbench/tasks/resumable-chat-streams/task.yaml`
(`status: draft`, `stage: requirements`, created 2026-07-06). Its own
`assumptions` explicitly asked to be "revisited once [Milestone 4's]
shape is known" — Milestone 4 shipped `conversation-{stage}.yaml`
persistence, but that only durably stores finished Draft-tool turns, not
in-progress streaming chunks, and nobody revisited the question afterward.
Still open: does Milestone 4's persistence solve most of this for free, or
is a dedicated in-memory resume mechanism still needed?

## From Milestone 4 — Structured `ask_question` tool

A structured `ask_question` tool (options + a recommendation as a
first-class object, rendered as clickable choices) instead of the current
prompt-only interview discipline (one question per turn, a recommended
answer written into the prompt text, no structured UI affordance).

Named once, in Milestone 4's own "Follow-ups" section, and never mentioned
again anywhere in the repo — no backlog task stub was ever created for it,
unlike every other item on this page. This is the one entry here that had
no tracking at all before today.

## From Milestone 5 — `human` executor type stays schema-only

`execution.yaml`'s `executor.type` enum (`docs/task schema v0.md`) lists
`human` alongside `claude-code`/`codex`, but nothing implements it — no
UI, no API handler, no schema validation beyond the enum entry itself.
Deliberate, not an oversight: Milestone 5 chose not to build speculative
support ahead of a real use case.

Already has a backlog stub:
`data/projects/llm-workbench/tasks/human-executor/task.yaml`
(`status: draft`, `stage: requirements`, created 2026-07-09). Never
scheduled into any milestone or PR since.

## From Milestone 6 — Knowledge-base promotion

Folding a completed task's learnings into the Knowledge layer
(`internal/knowledge`). `internal/knowledge/knowledge.go` is deliberately
read-only today (`FileReader.Get(conceptID)` only, doc comment: "no
Create/Update/List/index") and `docs/knowledge schema v0.md` §6 confirms
no Go-side store exists yet — promotion needs a write path that doesn't
exist.

Milestone 7 re-examined this further: `data/knowledge/` doesn't have a
single concept in it yet in this workspace, and this project's own durable
knowledge (`CLAUDE.md`, `docs/engineering conventions.md`, `CONTEXT.md`'s
glossary, `docs/adr/`) already works fine, hand-authored, without touching
`internal/knowledge`. The purpose that does hold up: `data/knowledge/` is
for what doesn't belong in any single project's own docs/ADRs but should
inform any or all projects — a workspace-wide, cross-project layer,
structurally distinct from a per-repo ADR folder (Serena's memory tool is
a validating reference here, not a design to copy wholesale — its
`write_memory` has no human-approval gate, too weak a bar for this
system's "humans own intent" invariant).

**Resolved — scoped as Milestone 9 (2026-07-22).** The concrete answer to
"what does this do that hand-written docs don't" turned out to be real
technology-evaluation findings already stranded inside per-repo ADRs
(`docs/adr/0010`, `docs/adr/0011`) that are reusable facts about tools, not
decisions about this repo, plus a real second consumer already on record
(`data/projects/agent-shell/tasks/concept-notes/`). See
`docs/milestones/done/milestone9.md` for the full scoping — write path
(`propose_knowledge`, human-approval-gated), read path (an unfiltered,
whole-bundle query tool replacing the static-list-proposal idea considered
during scoping), and the `KnowledgeStore` interface shape, validated against
real external tools (llm-wiki-kit, Basic Memory, mem0, Cognee) rather than
assumed. No longer an orphan.

## From Milestone 7 — Testing-practices re-review

Surfaced while scoping PR 3's test strategy: PR 3 followed existing
precedent (`handleReviewDiff`'s handler test exercising real git rather
than a narrower mocked boundary) deliberately, but "that's what the
codebase already does" was flagged as too weak a bar on its own — the same
kind of gap a second-pass review (the Opus pass that caught PR 2's real
mechanism gaps during Milestone 7's initial scoping) is meant to catch
before an unexamined pattern hardens into unquestioned convention. No
backlog stub existed for this before today; needs its own dedicated look
at this project's test-layering habits.

## From Milestone 7 — `FinalizeReview`/`RecordExecution` coupling

`FinalizeReview`'s "no new execution can have been recorded since Review"
comment (`internal/task/lifecycle.go:127-132`) holds today only because
`RecordExecution` separately guards success writes to `StageImplementation`
(`internal/task/execution.go:180-198`) — true by construction across two
functions, not verified by either one. No bug today; worth a second look
if either function changes.

## From Milestone 7 — No repo auto-clone

**Resolved — scoped as Milestone 8a (2026-07-22).** Flagged during an audit
of what actually blocks MVP usability as one of only two orphans that are
silent correctness risks inside the core loop, not deferred features —
scoped together with "No staleness check on the shared checkout" below,
since both live in the same `ResolveWorkspace` code path. See
`docs/milestones/done/milestone8a.md`: lazy clone-if-absent, HTTPS-only, no
new credential storage. No longer an orphan.

## From Milestone 7 — No staleness check on the shared checkout

**Resolved — scoped as Milestone 8a (2026-07-22).** See
`docs/milestones/done/milestone8a.md`: a new `Project.default_branch` (auto-
determined via `gh repo view`) backs a **blocking** wrong-branch check at
the worktree-fork point — the one place a wrong answer is expensive to
undo — while behind-origin and dirty-working-tree stay advisory,
surfaced both in the stage-conversation system prompt and a frontend
banner. No longer an orphan.

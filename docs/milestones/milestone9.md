# Milestone 9 — Knowledge-Base Promotion

**Status:** Scoped via a `/grill-with-docs` session on 2026-07-22. Not
started.

## Why now

Milestone 6 deferred "folding a completed task's learnings into the
Knowledge layer" to Milestone 7; Milestone 7 re-deferred it, on the record,
pending its own dedicated `/grill-with-docs` session; Milestone 8 still
named it as open. `docs/milestones/milestone-orphans.md` ("From Milestone
6 — Knowledge-base promotion") eventually swept it in as a genuine orphan —
three milestones pointing at each other with no anchor at the end — and
named the actual blocker: no concept existed yet, so there was no concrete
answer to "what does this do that hand-written docs
(`CLAUDE.md`/`docs/engineering conventions.md`/ADRs) don't."

This milestone starts from a concrete answer instead of a blank slate.
Walking Milestone 6/7/8's own history surfaced two real technology/library
evaluations stranded inside per-repo ADRs that are actually reusable facts
about tools, not decisions about this repo —
`docs/adr/0011-hand-roll-tool-loop-engine-over-eino-or-dive.md`'s local-model
tool-loop pathology catalog (repetition spirals, duplicate tool calls,
tool-call XML leaking into the reasoning channel, silent stop-after-narration)
and `docs/adr/0010-defer-bash-sandboxing-for-execution-harness.md`'s
Sandboxie/Landlock sandboxing evaluation — plus a third, differently-shaped
candidate (Go service scaffolding conventions) drawn from a real external
incident. Critically, `data/projects/agent-shell/tasks/concept-notes/`
is a second, real project in this same workspace that will need the
pathology catalog for its own planned "homespun Go agent runner" — a real
second consumer, not a hypothetical one.

## Introduces

* A write path: an executor can propose a new knowledge concept, or an
  edit to an existing one, for human approval — folding a task's learnings
  into `data/knowledge/` instead of leaving them stranded in that task's own
  `context.yaml` or a per-repo ADR.
* A read path: any task, at any stage, can browse and fetch from the whole
  `data/knowledge/` bundle live — not just the concepts a human already
  thought to curate into `Project.knowledge[]`/`Task.references.knowledge[]`.
* `internal/knowledge`'s provider interface widened from read-only
  (`Get` only) to a full `KnowledgeStore` (`Get`/`List`/`Put`), renamed to
  make clear it's no longer read-only, and kept strictly non-filesystem-shaped
  at the interface boundary so a non-local-file `KnowledgeStore`
  implementation stays genuinely viable later.
* At least one real concept migrated into `data/knowledge/` as part of this
  work: the local-model tool-loop pathology catalog.

## The promotion mechanism (write path)

A new Draft tool, `propose_knowledge` (`internal/drafttool.ProposeKnowledge`,
alongside the existing `ProposeContext`/`ProposePlan`/`ProposeReview` —
`internal/drafttool/drafttool.go:14-18,107`), offered only during a task's
**Review stage**, mirroring how `propose_review` is scoped there today.
Adding it to `drafttool.All()` is what makes it reachable from all three
runners for free: `cmd/draftmcp` picks it up as one more static MCP tool for
`CodexRunner`, and `internal/api/stage_conversation.go`'s existing per-stage
tool registration does the same for `ClaudeRunner`/`ChatClientRunner` — no
new per-runner plumbing needed for the write side itself (see "Open
questions" below for the one piece that *does* need new plumbing: the read
side coexisting with this as a non-stop-condition tool).

Unlike `propose_review`, the decision is **two-way (accept/reject)**, not
three-way. `propose_review`'s `needs_changes` exists specifically because it
continues from a prior *execution branch*
(`docs/adr/0012-needs-changes-continues-from-prior-execution-branch.md`) — a
knowledge concept proposal has no execution branch to continue from, so
modeling a third state here would be copying that shape without the thing
that made it necessary. "Needs changes" is just more conversation; the
executor redrafts and re-proposes within the same turn.

`propose_knowledge` covers both a brand-new concept ID and an edit to an
existing one — the same tool call either way, always carrying the full
resulting content (conceptID, `type`, other frontmatter fields, and the
markdown body), never a diff. On accept, the concept is written via
`KnowledgeStore.Put` — a whole-file replace matching this whole-file-propose
shape.

**Durability model.** `data/` (including `data/knowledge/`) is tracked in
this same repository, not a submodule and not a separate workspace repo.
Like every other Task/Project write today (`TaskStore`/`ProjectStore`
neither of which call `git commit` anywhere in `internal/`), an accepted
`propose_knowledge` write just lands on disk — the human-approval gate is
the accept/reject decision itself, and durability past that is whatever git
workflow already commits `data/` today. This is deliberately **not** a new
PR-based mechanism; Milestone 7's push/PR/rejection cycle is a different
subsystem entirely, scoped to external repos referenced by Projects, not to
the workbench's own `data/` storage.

`type` stays freeform, per OKF's own stance that type values aren't
centrally registered (`docs/knowledge schema v0.md` §2) — no fixed enum is
introduced. Only one concept exists at scoping time; locking in a taxonomy
now would repeat the exact mistake (inventing structure ahead of need) that
left this item unscoped for three milestones.

## The discovery mechanism (read path)

A new, always-available, **read-only query tool** — `List` (concept IDs
plus enough frontmatter to browse: `type`/`title`/`description`/`tags`) and
`Get` (full content by ID). No full-text search in v1: at launch size (one
to three concepts) an executor scanning a short listing is entirely
sufficient, and search is solving a scale problem this bundle doesn't have
yet — a legitimate, explicitly-named future follow-up, not a silent gap.

This **replaces** an earlier design considered during scoping — proposing
relevant concept IDs into `Project.knowledge[]` at a new project's first
Requirements/Architecture stage, gated behind its own accept/reject step.
Live query access is strictly more useful (works for mature projects, not
just brand-new ones) and needs no proposal ceremony at all, since it's
non-destructive.

Scope is **unfiltered across the whole bundle**, not limited to the current
Project's curated list — the entire motivating case (agent-shell finding
llm-workbench's pathology catalog) depends on it seeing concepts nobody has
curated into agent-shell's own list yet. Available at every task stage,
since it carries the same trust level as the existing read-only toolset
(`readOnlyTools`, `internal/agentrunner/claude_runner.go:28`) and neither
Review's nor Execute's wider toolsets ever remove that baseline.

**`Project.knowledge[]`/`Task.references.knowledge[]` are unchanged by this
work.** `internal/api/stage_conversation.go:731-739` already resolves every
listed concept ID via `KnowledgeStore.Get` and injects its full body into
every stage conversation's system prompt unconditionally — a **push**
mechanism for concepts already known to be load-bearing. The new query tool
is the **pull** side, for anything not already known to be relevant. Both
coexist; nothing about the curated list's existing behavior changes.

## Interface shape and provider compatibility

`internal/knowledge.FileReader` → renamed **`FileStore`** (matching
`task.FileStore`/`project.FileStore`'s naming once it can write, not just
read); the consumer-side interface `KnowledgeReader`
(`internal/api/router.go:62`) → renamed **`KnowledgeStore`**, gaining:

```go
type KnowledgeStore interface {
    Get(conceptID string) (knowledge.Concept, error)
    List() ([]knowledge.ConceptSummary, error)
    Put(conceptID string, c knowledge.Concept) error
}
```

`Create`/`Update` were considered as two separate methods, then collapsed
into one `Put` (upsert) — validated against a real precedent
([Basic Memory](https://mcpservers.org/servers/basicmachines-co/basic-memory),
which exposes a single `wiki_write_page` MCP tool for both cases, and
already ships the exact local-file-vs-hosted-backend swap this interface is
meant to allow, through that same tool surface) rather than only by our own
reasoning.

**Constraint:** no filesystem-shaped type crosses this interface boundary —
`List` returns concept IDs and frontmatter fields, never paths or
`os.DirEntry`; `Put` takes a conceptID and a `Concept` value, never raw
bytes. Path-traversal validation and OKF serialization stay inside the
concrete `FileStore`, which is the interface's only implementation today,
not a property of the interface itself.

**Research validation, done during scoping rather than assumed:**
tools in this space split cleanly into two categories. Curated,
individually-addressable document stores —
[llm-wiki-kit](https://github.com/iamsashank09/llm-wiki-kit)
(`wiki_read_page`/`wiki_write_page`/`wiki_status` map directly onto
`Get`/`Put`/`List`) and Basic Memory above — fit this interface shape
cleanly, including as a plausible future MCP-proxying `KnowledgeStore`
implementation. Auto-extraction memory engines — mem0 (server-assigned
opaque IDs, semantic-search-first, `user_id`/`agent_id`/`run_id`-scoped) and
[Cognee](https://github.com/topoteretes/cognee) (raw content →
LLM-driven entity/relationship extraction → graph+vector store, no
stable human-chosen ID anywhere in the pipeline) — don't fit, and that's a
structural mismatch rather than a missing adapter: both are built for
auto-extracted, session-scoped memory, not curated, human-approved,
individually-addressable documentation. That split confirms `docs/adr/0002`'s
OKF choice was the right precedent to build on, not an accident.

This also surfaced the real boundary on future flexibility: adopting an
auto-committing, no-stable-ID engine (Cognee/mem0-shaped) as the *primary*
store is genuinely foreclosed — but that was already decided upstream, by
`docs/architectural invariants.md`'s "Humans own intent" and "Knowledge is
separate from intent," not by this milestone's interface design. Consistent
with that invariant, search/graph/auto-suggest capabilities remain available
to add later as assistive layers (e.g., ranking `List` results, pre-filling
a `propose_knowledge` draft) that never bypass the accept/reject gate. The
gate itself stays as designed for now; revisiting it is explicitly out of
scope for this milestone (see below).

## Schema changes

* `internal/knowledge`: `FileReader` → `FileStore`; new `ConceptSummary`
  struct (conceptID + `type`/`title`/`description`/`tags`); `List`/`Put`
  methods added.
* `internal/api/router.go:62`: `KnowledgeReader` → `KnowledgeStore`,
  widened per above.
* `internal/drafttool`: new `ProposeKnowledge` `Definition` alongside the
  existing three, added to `All()`.
* A new native `toolloop.Tool` (query/list-and-get over `KnowledgeStore`)
  for `ChatClientRunner`'s toolset.
* `ClaudeRunner`: the always-on query tool needs to coexist with the
  stage's single stop-condition Draft tool inside the same in-process MCP
  server/session — today's `RunInput.Tool` (singular,
  `internal/agentrunner/claude_runner.go:290-299`) and `processMessage`'s
  stop-condition matching (`claude_runner.go:395-433`) both assume exactly
  one tool per session and treat any matching tool call as ending the turn.
  This needs to generalize to "one stop-condition tool plus N always-on
  tools" — see "Open questions" below.
* Frontend: new `KnowledgeDraftForm.tsx`, mirroring `ReviewDraftForm.tsx`'s
  shape (a form per concept field, not a generic drafts-renderer — no
  existing component renders an arbitrary draft schema for free).

## Content migration

The local-model tool-loop pathology catalog is ported out of
`docs/adr/0011-hand-roll-tool-loop-engine-over-eino-or-dive.md` into a real
concept document under `data/knowledge/` as part of this milestone, proving
the write path end-to-end against real content rather than shipping
infrastructure nobody has used. The OS-sandboxing evaluation
(`docs/adr/0010`) and the Go scaffolding-conventions concept are natural
fast-follows once the pipeline is proven, not committed to in this
milestone.

## Out of scope

Deferred deliberately, named rather than dropped:

* **Full-text/semantic search** over `data/knowledge/`. Revisit once the
  bundle is large enough that a browsable listing stops working.
* **A fixed `type` taxonomy.** Revisit once enough concepts exist that
  inconsistent `type` values actually cause a problem.
* **Any assistive search/graph/auto-suggest capability** layered on top of
  the write path (e.g. an LLM pre-filling a `propose_knowledge` draft from a
  task transcript). Structurally compatible with this design, not built now.
* **Reconsidering the human-approval gate itself.** Kept as-is; explicitly
  revisitable later, not reopened by this milestone.
* **Ingest trigger points beyond a task's Review stage** (e.g. a standalone
  command, or triggering outside any task).
* **Sandboxing-evaluation and scaffolding-conventions content** — tracked as
  fast-follows once the pathology-catalog migration proves the pipeline.

## Phasing

Delivered as sequential PRs, matching prior milestones' cadence:

* **PR 1 — `KnowledgeStore` + write path, backend only.** The
  `FileReader`→`FileStore` rename, `List`/`Put` additions, the
  `ConceptSummary` type, and `propose_knowledge`
  (`internal/drafttool.ProposeKnowledge`) wired into the Review-stage
  conversation with the two-way accept/reject decision. Proven via fixture
  tasks and unit tests before any UI exists, matching how Milestone 6 PR 1
  proved `RecordReview` first.
* **PR 2 — Read path: the query tool.** The native `toolloop.Tool` for
  `ChatClientRunner`, plus resolving the `ClaudeRunner`/`CodexRunner`
  always-on-tool-alongside-a-stop-condition-tool question below, so all
  three runners can `List`/`Get` from any stage.
* **PR 3 — `KnowledgeDraftForm` frontend.** Mirrors `ReviewDraftForm`'s
  pattern; wires `propose_knowledge` into the Review-stage UI a human
  actually sees and acts on.
* **PR 4 — Content migration.** Port the pathology catalog into a real
  `data/knowledge/` concept doc; live-verify an executor finding and citing
  it via the new query tool, end to end.

## Open questions for whoever executes this milestone

* **How does the always-on query tool coexist with `ClaudeRunner`'s
  single stop-condition Draft tool?** `internal/drafttool` already solves
  this cleanly for *proposal*-shaped tools (one more `Definition` in
  `All()`, picked up by both `cmd/draftmcp` and the per-stage registration
  for free) — but the query tool is not proposal-shaped, it must not end
  the turn when called, and today's `RunInput.Tool`/`processMessage`
  assume exactly one tool per session whose call always means "done."
  Candidates: widen `RunInput.Tool` to a slice with one marked as the stop
  condition; or register the query tool on a second in-process MCP server
  alongside the existing `"draft"` one. Needs deciding during PR 2, not
  here.
* **Same question for `CodexRunner`.** Its `cmd/draftmcp` static server is
  currently framed entirely around `drafttool.All()`'s proposal tools. A
  second static MCP server binary for the query tool, or a widened
  `drafttool.Definition` carrying a "does this end the turn" flag, are the
  two live options.
* **Concept ID / directory layout for the pathology catalog** — not pinned
  during scoping; pick something descriptive under `data/knowledge/` when
  PR 4 lands (e.g. a `model-behavior/` or similar grouping), consistent
  with OKF's hierarchical bundle structure.

# Milestone 9 — Knowledge-Base Promotion

**Status: Shipped (2026-07-23)** — all four phased PRs merged. Scoped via a
`/grill-with-docs` session on 2026-07-22. See "What shipped (PR 1)" through
"(PR 4)" below for what actually landed.

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

* **PR 1 — `KnowledgeStore` + write path, backend only. ✅ Shipped
  (2026-07-23).** The `FileReader`→`FileStore` rename, `List`/`Put`
  additions, the `ConceptSummary` type, and `propose_knowledge`
  (`internal/drafttool.ProposeKnowledge`) wired into the Review-stage
  conversation with the two-way accept/reject decision. Proven via fixture
  tasks and unit tests before any UI exists, matching how Milestone 6 PR 1
  proved `RecordReview` first. See "What shipped (PR 1)" below.
* **PR 2 — Read path: the query tool. ✅ Shipped (2026-07-23).** The native
  `toolloop.Tool` for `ChatClientRunner`, plus resolving the
  `ClaudeRunner`/`CodexRunner` always-on-tool-alongside-a-stop-condition-tool
  question below, so all three runners can `List`/`Get` from any stage. See
  "What shipped (PR 2)" below.
* **PR 3 — `KnowledgeDraftForm` frontend. ✅ Shipped (2026-07-23).** Mirrors
  `ReviewDraftForm`'s pattern; wires `propose_knowledge` into the
  Review-stage UI a human actually sees and acts on. See "What shipped
  (PR 3)" below.
* **PR 4 — Content migration. ✅ Shipped (2026-07-23).** Port the pathology
  catalog into a real `data/knowledge/` concept doc; live-verify an
  executor finding and citing it via the new query tool, end to end. See
  "What shipped (PR 4)" below. Milestone 9 is complete as of this PR.

## What shipped (PR 1, 2026-07-23)

`internal/knowledge`: `FileReader`→`FileStore` (`NewFileReader`→
`NewFileStore`), plus `ConceptSummary` (conceptID, type, title, description,
tags) and two new methods — `List()` (walks the bundle, skipping the
reserved `index.md`/`log.md` filenames, logging and skipping any concept
that fails to parse rather than failing the whole listing — an empty/
not-yet-existing bundle root returns `nil, nil`, not an error) and `Put`
(a whole-file create-or-replace by concept id; `c.Type` is authoritative
over `c.Frontmatter["type"]`, so a `Get`→`Put`→`Get` round-trip is always
consistent). `internal/api/router.go`'s `KnowledgeReader` interface and
`Server.KnowledgeReader` field are renamed `KnowledgeStore`, widened to the
full `Get`/`List`/`Put` shape scoped above.

`internal/drafttool` gained `ProposeKnowledge` (`propose_knowledge`),
added to `All()` — picked up by `cmd/draftmcp`'s `tools/list` and
`CodexRunner.ensureRegistered`'s per-tool approval grant automatically,
with no changes needed in either place.

Wiring `propose_knowledge` into the Review-stage conversation *alongside*
`propose_review` (rather than instead of it — either may be called
independently within one conversation) meant Review needed to offer two
Draft tools where every runner previously assumed exactly one. Resolved by
widening the singular-tool shape to a slice, not by inventing an
always-on/non-stop distinction (that remains PR 2's problem for the
read-only query tool, which must coexist with *whichever* Draft tool(s)
a stage offers without ending the turn — a different, harder shape than
"offer two, stop on whichever one is called"):
`internal/toolloop.Config.StopTool *chat.Tool` → `StopTools []chat.Tool`
(the loop stops on a call to any of them); `agentrunner.RunInput.Tool
chat.Tool` → `Tools []chat.Tool`; `ClaudeRunner.clientFor` registers every
offered tool on the same in-process SDK MCP server (`CreateSDKMcpServer`
already took a tools-variadic signature, so this needed no SDK-level
change) and `processMessage` matches a call against any of their qualified
names; `ChatClientRunner` passes `in.Tools` straight through to
`cfg.StopTools`; `CodexRunner` tells the model about every offered tool
name in its prompt instruction and matches a call against any of them.
`stage_conversation.go`'s `stageTool(stage)` now returns `[]chat.Tool`
(one for Requirements/Planning, two for Review), and
`runStageTurn`'s hallucination guard checks membership in the offered set
rather than equality with a single name.

A new handler, `handleFinalizeKnowledge` (`internal/api/knowledge_draft.go`,
`POST .../tasks/{taskId}/knowledge/finalize`), is the backend half of the
two-way accept/reject decision — deliberately not a `TaskStore` method or a
task-state transition of any kind, since a knowledge concept lives in a
workspace-wide store independent of any one task, and Review's own
conversation/verdict continues regardless of what a human decides here. On
accept it calls `KnowledgeStore.Put`; on reject it's a no-op beyond
acknowledging the decision — there is no "needs_changes" record the way
Review's own three-way verdict has, since a rejected proposal is just more
conversation the executor can redraft within. Gated on the task currently
being at `stage: review` (the only stage `propose_knowledge` is ever
offered from), even though the write itself touches no task state.

No UI yet, as scoped — proven via `internal/knowledge`'s unit tests, the
new multi-tool coverage in `toolloop`/`claude_runner`/`codex_runner`'s own
test suites, and `internal/api/knowledge_draft_test.go`'s handler-level
fixture tests (accept, reject, wrong stage, invalid body/decision,
missing/invalid concept id). `go build ./...`, `go vet ./...`, and
`go test ./...` all pass across the whole module.

## What shipped (PR 2, 2026-07-23)

New package `internal/knowledgetool` (mirroring `internal/drafttool`'s
shape but for a fundamentally different kind of tool — see below):
`list_knowledge_concepts`/`get_knowledge_concept` name/description/schema
`Definition`s plus `All()`, a narrow `Store` interface (`Get`/`List` only —
deliberately no `Put`; the write side stays reachable exclusively through
`propose_knowledge`'s human accept/reject gate, never through a tool an
executor can call unattended), and `ExecuteList`/`ExecuteGet` — the actual
query logic, rendering results as plain text, shared verbatim by every
caller that runs these tools for real.

**The coexistence question resolved simpler than scoped.** The Open
Questions below (as originally written) worried about generalizing
`RunInput.Tools` to "some stop the turn, some don't." That generalization
turned out to be unnecessary: `internal/toolloop.Config` already
distinguishes `Tools` (executed for real, loop continues — what
Read/Grep/Glob/bash already are) from `StopTools` (never executed, ends the
loop — what Draft tools are). The knowledge tools are simply new `Tools`
entries (`toolloop.KnowledgeTools`, new `tools_knowledge.go`), not a new
concept the engine needed to learn. `ChatClientRunner.loopTools` (renamed
from the free function `loopToolsFor`, now a method so it can read
`r.knowledgeStore`) always includes them, independent of whether a usable
workspace resolved this turn — data/knowledge/ is a workspace-wide bundle,
not part of any project's checked-out repository, so a project with no
configured repo still gets knowledge access even though its file tools
degrade to none.

**`ClaudeRunner`** registers a *second* in-process SDK MCP server
(`knowledgeServerName = "knowledge"`, alongside the existing `"draft"`
one), always present when constructed with a non-nil store, independent of
`in.Tools`/stage. Its two tools carry real handlers
(`knowledgeListHandler`/`knowledgeGetHandler`, methods so they can close
over `r.knowledgeStore`) — unlike `draftToolHandler`'s fire-and-forget ack.
`processMessage` needed no changes at all: it only ever inspects the
`"draft"` server's calls for a Draft proposal, so a knowledge-tool call
just flows through the `claude` CLI's own turn loop like any other
MCP-executed tool, gets a real result, and the CLI continues on its own.

**`CodexRunner`** has no in-process MCP mechanism, so the same
external-process pattern PR 1 already relied on for Draft tools extends
naturally: `cmd/draftmcp` gained a `--knowledge-root` flag; when set, it
constructs its own `*knowledge.FileStore` and actually executes
`list_knowledge_concepts`/`get_knowledge_concept` for real in `tools/call`
(unlike the Draft tools, which stay ack-only — there's no event-stream side
channel a query's real answer could travel through instead, the way a
proposal's payload travels via `MCPToolCall.Arguments`). `NewCodexRunner`
gained a `knowledgeRoot string` parameter, passed to the registered
`draftmcp` process as an `args` entry in its persisted `mcp_servers.*`
config; `registerDraftServer` also grants `approval_mode: "approve"` to
`knowledgetool.All()`'s names (when `knowledgeRoot` is set) exactly as it
already does for `drafttool.All()`'s.

**Wiring**: `cmd/server/main.go` passes the same `knowledgeStore`
(`*knowledge.FileStore`) into `NewClaudeRunner`/`NewChatClientRunner`, and
`filepath.Join(workspaceRoot, "knowledge")` into `NewCodexRunner`, so all
three executors answer identically regardless of which one a conversation
uses. `internal/api/stage_conversation.go` needed **zero** changes — tool
availability is entirely runner-construction-level now, not per-request,
so "available at every task stage" fell out for free rather than requiring
new per-stage wiring.

Proven via `internal/knowledgetool`'s own unit tests, new coverage in
`internal/toolloop`, `ChatClientRunner` (including a full tool-call round
trip through the real engine), `ClaudeRunner`'s two handler methods, and
`cmd/draftmcp`'s `tools/list`/`tools/call` behavior with and without
`--knowledge-root`. `CodexRunner.registerDraftServer`'s config-writing path
has no existing test seam (no prior test exercises it at all — it talks to
a real `codex` CLI client with no fake indirection) and stays covered only
by code review + the type system, a pre-existing gap this PR didn't
introduce. `go build ./...`, `go vet ./...`, and `go test ./...` all pass
across the whole module.

## What shipped (PR 3, 2026-07-23)

New `frontend/src/KnowledgeDraftForm.tsx`, mirroring `ReviewDraftForm`'s
shape (a handful of fields plus a body textarea) but adapted to OKF's open
frontmatter bag: `concept_id`/`type` as their own inputs, `title`/
`description`/`tags` (comma-separated) mapped to/from `frontmatter` as the
three most common fields, and every other frontmatter key (`resource`,
`timestamp`, producer-defined fields) preserved byte-for-byte rather than
requiring a raw-JSON textarea for the common case.

**`StageConversationPanel<D, S>` gained a second, independent Draft-tool
track.** Before this PR it assumed exactly one Draft tool per stage — a
single `pendingDraft`, one `renderDraft`, one `onFinalize` — which the
backend had already outgrown as of PR 1/2 (Review offers both
`propose_review` and `propose_knowledge` in the same conversation). A new
optional `secondaryDraft` config (`toolName`, `emptyDraft`, `renderDraft`,
`onAccept`, `onReject`) adds a second, fully independent
pending-draft/finalizing/error state, routed by matching
`event.tool_call.name` against `secondaryDraft.toolName` — both in the live
SSE stream handler and in the mount-effect's history-rehydration scan
(walked once, tracking the most recent tool call of each kind
independently, since they can interleave throughout the conversation).
Requirements/Planning pass no `secondaryDraft` and are completely
unaffected — confirmed by the full existing test suite passing unchanged.
Unlike the main draft's Finalize/Request-changes/Discard trio, the
secondary track is a plain two-way Accept/Reject (matching
`propose_knowledge`'s own two-way decision) with no "request changes"
affordance — the human can just reply normally in chat to ask for a
redraft, the same as before Drafts existed at all.

`ReviewPanel.tsx` wires `secondaryDraft` to `KnowledgeDraftForm` and two new
`finalizeKnowledge(..., 'accepted' | 'rejected')` calls
(`frontend/src/api.ts`, hitting `handleFinalizeKnowledge` from PR 1). Both
tracks render simultaneously when both are pending — a review verdict and a
knowledge proposal are decided independently, confirmed by a dedicated
test.

**Known limitation, accepted deliberately rather than built around:** a
page reload re-derives the pending secondary draft from "the most recent
`propose_knowledge` tool call in conversation history," the same way the
main draft already does — but unlike the main draft (whose Finalize
advances the task's stage, so the whole panel stops rendering once truly
done), accepting/rejecting a knowledge draft leaves no mark on the
conversation history itself. A reload after an already-decided proposal
re-shows it as pending. Not a correctness bug: `KnowledgeStore.Put` is a
whole-file replace (re-accepting just re-writes the same content) and
reject was always a no-op — but it is a UX rough edge. Worth a persisted
"decided" marker if it proves annoying in practice; not built here,
matching this milestone's general bias toward not inventing structure
ahead of a demonstrated need.

Proven via `KnowledgeDraftForm.test.tsx` (10 cases: rendering, per-field
edits, frontmatter-key preservation) and new `ReviewPanel.test.tsx`
coverage (the secondary draft surfacing independently of the review
verdict, Accept calling `finalizeKnowledge`, and both tracks pending and
decided independently at once) — 176 frontend tests pass in total, plus a
clean `tsc -b`, `oxlint`, and `vite build`. Live-verified in the browser
that the app and task/project navigation still render with no console
errors after the change; the live `propose_knowledge` round trip itself
was not exercised against a real model in this pass (no task was at
`stage: review` in the seeded dev data) — covered instead by the
React-Testing-Library suite above, which renders the real component tree
and dispatches real DOM events rather than mocking the UI layer.

## What shipped (PR 4, 2026-07-23)

`data/knowledge/model-behavior/local-tool-loop-pathologies.md` — the
bundle's first-ever concept document, resolving the one remaining open
question (concept ID / directory layout: `model-behavior/` grouping, as
suggested during scoping). `type: Domain Note`, with `title`/
`description`/`tags`/`timestamp` frontmatter. Synthesized from ADR-0011
plus the fuller primary-source evidence in the never-merged
`milestone8-phase0-spike` branch's `spike/NOTES.md` (raw per-run findings
ADR-0011 itself only summarizes) — not a copy-paste, a genuine
distillation: the four pathologies (duplicate tool calls, repetition
spirals, tool-call-XML-in-the-wrong-channel, announce-then-stall), a
related wire-format gotcha (gpt-oss's `reasoning` vs `reasoning_content`
field naming), each pathology's concrete engine-level guard mapped to the
real code that implements it today (`internal/toolloop/engine.go`'s
`dedupeCalls`/`capCalls`/`executeCall`, `Config.MaxTokens`,
`Result.Exhausted`, `AgentRunner.CheckHealth`), and the reliability
data point that model choice dominates framework choice (qwen3.6 15-75%
depending on config; `gpt-oss-20b` 6/6 clean). ADR-0011 itself is
unchanged except for a short forward-pointer note — it remains the
historical record of *why* hand-rolled was chosen; the new concept is the
standalone, query-tool-accessible version of *what the pathologies are*,
written for reuse outside that specific decision (the motivating second
consumer: `data/projects/agent-shell/tasks/concept-notes/`'s own planned
local-model agent runner).

**Live-verified end to end, for real** — not simulated: built
`cmd/draftmcp` fresh, pointed a `--strict-mcp-config` `claude` CLI
invocation at it with `--knowledge-root data/knowledge`, and asked a
freshly-started Claude Code session (no other context, `--allowedTools`
scoped to only the two knowledge tools) to find and summarize "the
concept about tool-loop reliability." It correctly called
`list_knowledge_concepts`, then `get_knowledge_concept` with
`concept_id: model-behavior/local-tool-loop-pathologies`, and accurately
summarized the four pathologies and the duplicate-call guard
(`dedupeCalls`/`capCalls`/`MaxToolCallsPerTurn`) — proof that a real
executor, through the real `cmd/draftmcp` binary, against the real
`data/knowledge/` bundle, can discover and cite content nobody hand-fed
it. Also verified directly against `internal/knowledge.FileStore`/
`internal/knowledgetool.ExecuteList`/`ExecuteGet` (the same code path
`ChatClientRunner`/`ClaudeRunner` use) before the live CLI check, to
confirm the document's frontmatter/body parse cleanly. `go build ./...`,
`go vet ./...`, and `go test ./...` all pass unchanged (this PR is
content-only; no Go code changed).

## Open questions for whoever executes this milestone

None remaining — the concept ID/layout question above is resolved, and
every PR in this milestone's phasing has shipped. The three items named
in "Content migration" as deliberate fast-follows (the OS-sandboxing
evaluation from ADR-0010, the Go scaffolding-conventions concept, and
full-text search/a fixed `type` taxonomy/assistive search from "Out of
scope") remain exactly that: real, named, not silently dropped, but
outside this milestone's own scope — pick them up as their own
`/grill-with-docs` sessions if and when they're actually needed, per this
milestone's own stated bias against building structure ahead of
demonstrated need.

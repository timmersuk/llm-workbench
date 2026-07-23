# Milestone 8b — Handler Dependency Injection (`internal/api`)

**Status:** Identified 2026-07-22, while reviewing Milestone 8a PR 2's diff.
Scoped via a `/grill-with-docs` session on 2026-07-23 — see "What this
milestone does" below for the resolved design, and
`docs/adr/0016-api-handlers-become-methods-on-an-internal-server-struct.md`
for the pattern decision and rejected alternatives. **Shipped (2026-07-23)**
in a single pass — see "What shipped" below.

## Why now

Milestone 8a PR 2 added `defaultBranchResolver` as a new parameter to
`NewRouter` and to every handler that resolves an execution/review
workspace — the same shape of change PR 1 (`GitHubPRClient`), and Milestone
6/7 before it, already made more than once. Reviewing that diff surfaced
the pattern directly: `handleStartExecution` now takes 6 parameters,
`handleStartStageConversation`/`handlePostStageMessage`/
`handleRegenerateStageMessage` each take 7, and `NewRouter` itself takes 9
— and the overwhelming majority of those parameters are **the same handful
of dependencies, invariant for the lifetime of the server process**
(`ProjectStore`, `TaskStoreFactory`, `agentRunners`, `reposRoot`,
`prClient`, `defaultBranchResolver`, `knowledgeReader`), re-passed
identically to every one of them from `NewRouter`'s single call site. Every
future cross-cutting dependency this codebase adds — and milestones 6, 7,
8, and 8a each added at least one — makes every one of these signatures
longer, not just the one function that actually needs the new thing.

The standard fix for this shape of problem in a Go HTTP service is
unsurprising: a struct holding the invariant dependencies as fields,
constructed once, with the handlers as methods on it instead of free
functions closing over a fresh copy of the same parameter list each time.
This doc doesn't commit to that shape yet — that's what the grilling
session is for — but it's the obvious direction, and worth naming so the
session starts from "how" rather than "whether."

## Codebase scan: where else does this pattern show up?

A full scan of `internal/` and `cmd/` for functions with 4+ parameters
where a meaningful subset are call-invariant (as opposed to functions that
just take a lot of genuinely per-call data, which is a different, benign
shape) found `internal/api`'s handler layer is clearly the dominant and
worst offender — no other package in this codebase shows the same problem.

### `internal/api` — primary candidate, all wired from one `NewRouter` call

* `NewRouter` (`router.go:91`) — 9 params: `projects, taskStores,
  knowledgeReader, agentRunners, reposRoot, prClient,
  defaultBranchResolver, frontendFS, buildId`. All invariant. 1 call site
  (`cmd/server/main.go:77`).
* `handleStartExecution` (`execution.go:79`) — 6 params, all the shared
  deps except `knowledgeReader`.
* `handleReviewDiff` (`review.go:50`) — 4 params: `projects, factory,
  reposRoot, defaultBranchResolver`.
* `handleStartStageConversation` / `handlePostStageMessage` /
  `handleRegenerateStageMessage` (`stage_conversation.go:385,300,532`) —
  **three near-identical 7-parameter signatures**, each adding
  `knowledgeReader` to the same base set. This is the single clearest piece
  of evidence in the scan: a shared-deps struct with methods would collapse
  this duplication, not just shorten each line individually.
* `handleFinalizeRequirements` (`finalize.go:65`) — 4 params: `projects,
  factory, agentRunners, reposRoot`. `handleFinalizePlan`/
  `handleFinalizeReview` (`finalize.go:112,152`) are the same family at 3
  params each — below this doc's 4+ bar, but structurally identical.
* `handlePushPR` (`pr.go:22`) — 4 params: `projects, factory, reposRoot,
  prClient`.
* `resolveStageStreamTarget` (`stage_conversation.go:212`) — 7 params;
  `projects`/`factory`/`agentRunners` invariant, `w`/`executorKey`/
  `projectId`/`taskId` genuinely per-call. 4 call sites within the same
  file.
* `resolveStageRun` (`stage_conversation.go:767`) — 9 params; invariant
  candidates `reposRoot`, `knowledgeReader`, `prClient`, `projects`,
  `defaultBranchResolver`; per-call `ctx`, `proj`, `store`, `t`, `stage`. 4
  call sites.
* `buildReviewContext` (`stage_conversation.go:870`) — 6 params, only
  `reposRoot` invariant. `buildRejectedReviewContext`
  (`stage_conversation.go:824`) — 5 params, only `prClient` invariant.
  `buildStagePrompt` (`stage_conversation.go:700`) — 4 params, only
  `knowledgeReader` invariant.

### `internal/agentrunner` — a real but weaker second instance

* `ResolveExecutionWorkspace` (`worktree.go:97`, 7 params) /
  `ResolveReviewWorkspace` (`worktree.go:155`, 5 params) /
  `ResolveWorkspace` (`runner.go:261`, 3 params). `reposRoot` is
  process-invariant, but `repositories` varies **per-project**, not just
  per-call within one project — a genuinely different shape from
  `internal/api`'s "same value on every single call" case. Worth a lighter
  look in the same session, not necessarily the same struct-with-methods
  treatment.

### Explicitly not a good fit — named so a future session doesn't waste time

* `PushAndOpenPR` (`internal/agentrunner/pr.go:126`) — 9 params including
  `ctx`, but only `client GitHubPRClient` is invariant; everything else
  (`dir, newBranch, title, body`, three existing-PR fields) is genuinely
  per-call data. Many parameters, but not this problem.

### Checked, no matches

`internal/task`, `internal/project`, `internal/knowledge`,
`internal/gitutil`, `internal/toolloop`, `internal/chat`,
`internal/drafttool`, `cmd/server/main.go` — no function found with 4+
parameters where a meaningful subset is call-invariant rather than
per-call data.

## What this milestone does (resolved via `/grill-with-docs`, 2026-07-23)

* **One `Server` struct**, not split by route family. The chat handlers are
  the only family with a visibly smaller dependency set, and even they need
  only a strict subset of the same union `NewRouter` already takes —
  nothing needs a dependency *outside* that union, so splitting would only
  add a classification problem with no realized benefit.
* **Named `Server`** — idiomatic for the pattern
  (`type Server struct { ... }`, handlers as `func (s *Server)
  handleFoo(w, r)` methods), and nothing in the codebase already claims
  that name.
* **The five internal helpers become `Server` methods too**
  (`resolveStageRun`, `buildReviewContext`, `buildRejectedReviewContext`,
  `buildStagePrompt`, `resolveStageStreamTarget`) — leaving them as free
  functions taking the struct as a parameter would just move the same
  re-passing problem one level down the call stack.
* **`NewRouter`'s public signature is unchanged.** `Server` stays
  package-internal; `NewRouter` builds it internally and returns the
  `http.Handler` as before. `cmd/server/main.go` doesn't change. Only one
  call site exists and only ever will, so exporting `Server` would buy
  flexibility nothing is positioned to use.
* **Migrated in one pass, single PR.** Purely internal and
  behavior-preserving — nothing about it is independently shippable, so
  there's no partial-delivery value to capture by staging it, only a
  straddling period where two calling conventions coexist in one package.
  The existing handler test suite is the safety net.
* **No test-builder helper.** Bare `Server{...}` named-field struct
  literals already solve what a builder would solve — a test omits
  whatever fields it doesn't need, instead of today's positional calls
  padding irrelevant args with `nil`/`""`. A local builder is a cheap
  follow-up if real repetition shows up later.
* **`internal/agentrunner`'s workspace resolvers stay out of scope.**
  Their `repositories` parameter varies per-*project*, not per-call — a
  structurally different shape from `internal/api`'s "identical value on
  every call" case, so they don't share the problem this milestone solves.

Full rationale and rejected alternatives:
`docs/adr/0016-api-handlers-become-methods-on-an-internal-server-struct.md`.

## Out of scope

* Forcing the struct-with-methods shape onto candidates that don't
  genuinely fit it — `PushAndOpenPR` (see scan above), and
  `internal/agentrunner`'s resolvers' `repositories` parameter (per-project,
  not invariant).
* Exporting `Server`/`NewServer` or otherwise changing `NewRouter`'s public
  signature or `cmd/server/main.go`.
* A test-builder helper for constructing `Server` values.

## Phasing

Single PR — the whole `internal/api` package migrates from free-function
handlers to `Server` methods in one pass, per the migration-strategy
decision above.

## What shipped (2026-07-23)

`router.go` gained the unexported `Server` struct exactly as scoped —
`Projects`, `TaskStores`, `KnowledgeReader`, `AgentRunners`, `ReposRoot`,
`PRClient`, `DefaultBranchResolver`, `FrontendFS`, `BuildId` — constructed
once inside `NewRouter`, whose own signature is byte-for-byte unchanged.
Every route registration became `s.handleFoo()` instead of
`handleFoo(deps...)`; `cmd/server/main.go` required no changes.

Every HTTP handler across all 13 non-test `internal/api` files
(`agent_executors.go`, `chat.go`, `execution.go`, `finalize.go`, `pr.go`,
`projects.go`, `review.go`, `revise.go`, `router.go`'s own
healthcheck/version handlers, `stage_conversation.go`, `task_context.go`,
`tasks.go`, `workspace_status.go`) is now a `*Server` method, plus the five
helpers named during scoping (`resolveStageRun`, `buildReviewContext`,
`buildRejectedReviewContext`, `buildStagePrompt`, `resolveStageStreamTarget`).

Four more functions turned out to fit the exact same shape and were folded
in during implementation, beyond the five originally named in the scoping
scan — each mixes an invariant dependency with per-call data and is called
from enough sites that leaving it a free function would have just moved
the re-passing problem down one level, the same reasoning ADR-0016 already
makes for the five named helpers:

* `resolveTaskStore` (`tasks.go`) — `projects`/`factory` were invariant,
  called from a dozen-plus handlers across nearly every file in the
  package.
* `ensureDefaultBranch` (`default_branch.go`) — `projects`/`resolver` were
  invariant.
* `writePRCommentsFile` (`pr_comments.go`) — `prClient` was invariant.
* `closeSessions` (`finalize.go`) — `agentRunners` was invariant, called
  from five different handler methods.

A fifth, `appendWorkspaceAdvisories` (`stage_conversation.go`), also
converted — it postdates this milestone's original scoping scan (it
shipped in Milestone 8a PR 3, after the scan ran) but mixes `reposRoot`
(invariant) with `repositories` (per-call) the identical way, and is
called from the now-method `resolveStageRun`.

`internal/agentrunner`'s workspace resolvers were left untouched, as
scoped. No test-builder helper was added — every test file's positional
handler calls became `(&Server{Field: value, ...}).handleFoo()(w, req)`
literals, omitting whatever fields that test doesn't need.

**Verified:** `go build ./...`, `go vet ./...`, and `go test ./...` all
pass across the whole module, including `internal/api`'s full existing
test suite (unit + integration) — this refactor is internal-only and
behavior-preserving, so the pre-existing tests are the correctness proof;
no new tests were needed or added.

# Provider Abstraction

The domain models (Task, Project, Knowledge, Execution) define the
workbench's stable semantics — what a task *is*, what a review decision
*means*. The systems that actually do the work behind those models —
which LLM answers a chat message, which tool executes a task, where
knowledge documents physically live — are implementation details, and
implementation details should be swappable without touching the
semantics above them. A **provider** is the name for that swappable
boundary: any external system reached through a narrow, consumer-defined
interface rather than depended on directly.

This is not a new domain object alongside Task/Project/Knowledge/
Execution — it's a pattern applied *to* those models where they reach
out to something replaceable. See `docs/architectural invariants.md`
("Providers are replaceable").

---

## What counts as a provider

| Provider           | Realizes                                             | Status                  |
|--------------------|-------------------------------------------------------|--------------------------|
| LLM / Chat API     | `internal/chat/client.go` — the OpenAI-compatible chat completions shape | Implemented |
| Task / Project storage | Git-backed `FileStore`s, reached only through `TaskLister`/`ProjectLister` | Implemented |
| Executor           | Execution's `input → executor → output` (`project_summary.md` §7) — Claude Code, Codex CLI, local LLMs, human developers | Black-box by design today; no Go interface yet (`docs/milestones/milestone5.md` — "executor abstraction") |
| Knowledge store    | Parses/serves the OKF bundle described in `docs/knowledge schema v0.md` | Spec only — no store built yet |
| Git / repository backend | Where Task/Project/Knowledge data physically lives | Speculative only — not being built; today this is just local git with no abstraction layer |

---

## The mechanism

The Go-level shape a provider takes is already established in
`docs/engineering conventions.md` under "Interface-based dependency
injection": a small interface declared in the *consuming* package, with
a doc comment naming the concrete production type it's satisfied by.
Constructors return concrete structs; callers accept the narrow
interface, never the concrete type. Existing examples:

- `TaskLister` — `internal/api/router.go:17` (satisfied by `*task.FileStore`)
- `ProjectLister` — `internal/api/router.go:24` (satisfied by `*project.FileStore`)
- `ChatCompleter` — `internal/api/router.go:30`
- `Completer` — `internal/chat/client.go:14`

Any future provider — a knowledge store, an executor abstraction, a
non-git repository backend — should follow this same shape rather than
inventing a new one.

---

## Related docs

- `docs/architectural invariants.md` — the "Providers are replaceable" invariant
- `docs/engineering conventions.md` — "Interface-based dependency injection", "Chat / LLM provider integration"
- `docs/knowledge schema v0.md` §6 — the knowledge store's future provider shape
- `docs/project_summary.md` §7 (Executors), §9.3 (Providers Are Replaceable)

# Milestone 8 — Chat Client Agent Runner as Execution Harness

**Status: Not started** — scoped via a `/grill-with-docs` session on
2026-07-10.

## Why now

`ChatClientRunner.Execute` (`internal/agentrunner/chat_client_runner.go:49-51`)
returns `ErrExecuteNotSupported` unconditionally today — the wrapped
`chat.ChatClient` has no tool-execution loop: after a tool call it appends
a synthetic acknowledgement and stops, rather than actually running the
tool and feeding results back (see that method's doc comment, and
`data/projects/llm-workbench/tasks/chatclient-tool-loop/`). This means the
`local` executor cannot yet do real Implementation-stage work the way
`ClaudeRunner` and `CodexRunner` already can — it's the one executor in
`cmd/server/main.go`'s registry that can be selected but not actually used
to execute a task.

This is also exactly the condition `docs/adr/0005-defer-agenticgokit-adoption.md`
named as its own revisit trigger: "if the workbench later needs genuine
autonomous multi-step agent orchestration (an executor that plans and runs
several sub-steps itself without a human turn in between, rather than one
`Run` call per human chat message) ... this decision should be revisited
then." Giving `Execute` a real loop is that situation — so this milestone
is also the point to weigh two frameworks surfaced via research
(`github.com/deepnoodle-ai/dive`, `github.com/cloudwego/eino`) against the
same bar `docs/adr/0005-defer-agenticgokit-adoption.md` and
`docs/adr/0006-defer-langchaingo-retain-openai-go-sdk-chat-client.md`
already applied to AgenticGoKit and langchaingo, not evaluate them from a
blank slate.

## Introduces

* A shared, generic tool-call-loop engine — parameterized by toolset,
  workspace, and stop condition — used by **both** `ChatClientRunner.Run`
  and `ChatClientRunner.Execute`, rather than two independent
  implementations.
* Real `Execute` capability for the `local` executor: a Write/Edit/Bash
  tool loop against the execution worktree, producing `ExecuteOutput`/
  `ExecuteEvent` values in parity with `ClaudeRunner`'s shape.
* Closure of the standalone `chatclient-tool-loop` task
  (`data/projects/llm-workbench/tasks/chatclient-tool-loop/task.yaml`) as
  a side effect — it becomes the read-only instantiation of the same
  engine, not a separately-built feature. That task file is left
  untouched; this document is the cross-reference (its schema has no
  "superseded" status value to set — see `docs/task schema v0.md`).
* Phase 0: a spike comparing hand-rolled, `dive`, and `eino` as the
  engine's implementation vehicle, gated by explicit evaluation criteria
  (below) rather than a pre-made pick.
* Two new ADRs (`0009`, `0010`) covering the shared-engine architecture
  and the deferred Bash-sandboxing posture.

## The shared tool-loop engine

One engine, two instantiations:

| | `Run` (stage conversations) | `Execute` (implementation) |
|---|---|---|
| Toolset | Read/Grep/Glob (`agentrunner.readOnlyTools`) | Read/Grep/Glob/Write/Edit/Bash (`agentrunner.executionTools`) |
| Workspace | shared checkout (`ResolveWorkspace`) | isolated execution worktree (`ResolveExecutionWorkspace`, `worktree.go`) |
| Turn bound | ~30, matching `claudeRunnerMaxTurns` | ~100, matching `claudeExecutionMaxTurns` |
| Stop condition | text response, or a Draft-tool-call | model finishes the run autonomously |

Building this as one parameterized engine (rather than Execute's loop
first, Run's read-only loop as an afterthought) is what makes "absorbs
`chatclient-tool-loop`" true rather than aspirational: Run's read-only
trust boundary (no Write/Edit/Bash — the same boundary `readOnlyTools`'s
doc comment already establishes for `ClaudeRunner`) is enforced by
construction, because the toolset is a parameter, not by a second
implementation that could quietly drift from Execute's. See
`docs/adr/0009-shared-tool-loop-engine-for-run-and-execute.md`.

## Bash: scope and posture

Bash is in this milestone's scope, not deferred to a later one — but as
an explicit second phase within it: the loop plus Read/Grep/Glob/Write/Edit
land first, Bash once the loop itself is proven against those lower-risk
tools. Bash execution is confined only to the execution worktree's cwd —
no OS-level sandboxing (no Landlock, no containers) in v1, matching the
trust level `ClaudeRunner`/`CodexRunner` already operate at (both already
run arbitrary bash inside the worktree via their own CLI's tools, with no
deeper confinement either).

Two candidates are on record for a future hardening pass, footnoted here
rather than built now:

* **Landlock** (`landlock-lsm/go-landlock`, or shelling out via
  `Zouuup/landrun`) — real kernel-level path allow-listing. Linux 5.13+
  only.
* **Sandboxie** (Windows-only) — a different isolation model by default:
  it redirects writes into a separate sandbox folder for later, optional
  recovery, rather than confining them to a directory allowlist. Getting
  allowlist-like behavior out of it needs explicit "Open Path" rules
  configured — not a drop-in wrapper.

See `docs/adr/0010-defer-bash-sandboxing-for-execution-harness.md`.

## Phase 0: framework spike

Before any of the above is implemented, a time-boxed spike compares
hand-rolled, `dive`, and `eino`, scored against these criteria in
priority order. Each gates the next; a candidate failing criterion 1 is a
**soft** disqualifier — worth a harder second look only if **both**
framework candidates fail it, before falling back to hand-rolled:

1. **Client fidelity** — can it be backed by the existing `internal/chat`
   client (or a custom `ChatModel`/provider of equivalent fidelity)
   without regressing incremental tool-call-delta streaming or
   `reasoning_content` support — the exact axis
   `docs/adr/0006-defer-langchaingo-retain-openai-go-sdk-chat-client.md`
   already rejected langchaingo over? `eino`'s `ChatModel` interface
   (`Generate`/`Stream`, `ToolCallingChatModel.WithTools`) is documented
   for exactly this kind of wrapping; `dive`'s `providers/<name>`
   auto-registration model's external-extensibility is unconfirmed from
   docs alone. The spike verifies this hands-on for both.
2. **Shared-engine fit** — can the candidate support the two-instantiation
   shape above without fighting its own session/agent opinions?
3. **MCP tool-sourcing** — can it consume an MCP filesystem server
   (scoped via `--allowed-directories` to the execution worktree) for
   Read/Write/Edit/Glob/Grep, and an MCP LSP-bridge server (e.g.
   `isaacphi/mcp-language-server`) for code navigation — leaving only
   Bash needing bespoke, natively-confined implementation? This is
   largely framework-agnostic (any MCP client gets it, hand-rolled
   included), so it mainly derisks the toolset question rather than
   differentiating candidates.
4. **Output parity** — can it produce/compute the same `ExecuteOutput`/
   `ExecuteEvent` shape `ClaudeRunner` already establishes
   (`tool_call`/`tool_result` events, `NumTurns`, `TokensUsed`,
   `CostEstimate`, `DurationSeconds` — see `processExecuteMessage`,
   `claude_runner.go:431-486`)?
5. **Dependency weight** (stars/maintenance/transitive deps — `dive`
   ~128 stars vs `eino` ~12.2k, hand-rolled zero new deps) — a tiebreaker
   only, if 1–4 come out roughly even.

Hand-rolled is the implicit baseline throughout: it trivially passes 1
and 2 (it's already this repo's client, already a loop this repo
controls), and criterion 3 being largely MCP-sourced (rather than
framework-native) means its remaining cost is mostly the loop itself
(small, per prior art on hand-rolled Go agent harnesses) plus a bespoke,
cwd-confined Bash tool.

The spike concludes with its own ADR (the framework pick) — written after
it runs, not here.

## Out of scope

* **The framework pick itself.** Deferred to Phase 0's own ADR.
* **OS-level Bash sandboxing** (Landlock, Sandboxie, containers). See
  "Bash: scope and posture" above and
  `docs/adr/0010-defer-bash-sandboxing-for-execution-harness.md`.
* **Merge automation and knowledge-base promotion** — unrelated to this
  milestone's scope; still `docs/milestones/milestone7.md`.

## Open questions for whoever executes this milestone

* Where the shared engine actually lives (a new `internal/toolloop`-style
  package, vs. living inside `internal/chat` alongside the existing
  streaming/session code) — not designed in detail here.
* Whether MCP tool-sourcing (the filesystem/LSP-bridge servers from
  criterion 3) is wired into the initial implementation, or left as an
  explicit fast-follow after Bash lands.
* The spike's own output format (a written comparison, throwaway code for
  each candidate, or both) — left to whoever runs Phase 0.

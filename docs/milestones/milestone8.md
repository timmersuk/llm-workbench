# Milestone 8 — Chat Client Agent Runner as Execution Harness

**Status: In progress** — scoped via a `/grill-with-docs` session on
2026-07-10. **Phase 0 complete** (2026-07-11): the framework spike ran and
concluded in favour of a hand-rolled engine — see
`docs/adr/0011-hand-roll-tool-loop-engine-over-eino-or-dive.md`. **PR 2
shipped** (2026-07-11, #18): `internal/toolloop` engine plus the read-only
`Run` instantiation, live-verified — see "What shipped" below. **PR 3
shipped** (2026-07-12): `Execute` now drives the engine with a
Read/Grep/Glob/Write/Edit toolset, live-verified — see "What shipped (PR 3)"
below. Bash is next.

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

* ✅ **Shipped (PR 2, #18).** A shared, generic tool-call-loop engine —
  parameterized by toolset, workspace, and stop condition —
  `internal/toolloop`, used by `ChatClientRunner.Run` today and intended
  for `ChatClientRunner.Execute` once that instantiation lands, rather
  than two independent implementations.
* ✅ **Shipped (PR 3, 2026-07-12).** Real `Execute` capability for the
  `local` executor: a Write/Edit tool loop against the execution worktree
  (Bash still pending), producing `ExecuteOutput`/`ExecuteEvent` values in
  parity with `ClaudeRunner`'s shape — see "What shipped (PR 3)" below.
* ✅ **Shipped (PR 2, #18).** Closure of the standalone
  `chatclient-tool-loop` task
  (`data/projects/llm-workbench/tasks/chatclient-tool-loop/task.yaml`) —
  `ChatClientRunner.Run` is now the read-only instantiation of the shared
  engine, not a separately-built feature. That task file is left
  untouched; this document is the cross-reference (its schema has no
  "superseded" status value to set — see `docs/task schema v0.md`).
* ✅ **Shipped (Phase 0, #17).** A spike comparing hand-rolled, `dive`,
  and `eino` as the engine's implementation vehicle, gated by explicit
  evaluation criteria (below) rather than a pre-made pick. Concluded:
  hand-rolled — `docs/adr/0011-hand-roll-tool-loop-engine-over-eino-or-dive.md`.
* ✅ **Shipped (Phase 0, #17).** Three new ADRs (`0009`, `0010`, `0011`)
  covering the shared-engine architecture, the deferred Bash-sandboxing
  posture, and the framework-vs-hand-rolled verdict.

## The shared tool-loop engine

One engine, two instantiations:

| | `Run` (stage conversations) | `Execute` (implementation) |
|---|---|---|
| Status | ✅ shipped (PR 2, #18) | ✅ shipped (PR 3, Write/Edit); Bash pending |
| Toolset | Read/Grep/Glob (`toolloop.ReadOnlyTools`) | Read/Grep/Glob/Write/Edit (`toolloop.ExecutionTools`); Bash to follow |
| Workspace | shared checkout (`ResolveWorkspace`) | isolated execution worktree (`ResolveExecutionWorkspace`, `worktree.go`) |
| Turn bound | 30 (`claudeRunnerMaxTurns`) | ~100, matching `claudeExecutionMaxTurns` |
| Stop condition | text response, or a Draft-tool-call | model finishes the run autonomously |
| Turn exhaustion | degrades gracefully, returns partial content | fails loudly (returns an error alongside partial `ExecuteOutput`) |

Building this as one parameterized engine (rather than Execute's loop
first, Run's read-only loop as an afterthought) is what makes "absorbs
`chatclient-tool-loop`" true rather than aspirational: Run's read-only
trust boundary (no Write/Edit/Bash — the same boundary `readOnlyTools`'s
doc comment already establishes for `ClaudeRunner`) is enforced by
construction, because the toolset is a parameter, not by a second
implementation that could quietly drift from Execute's. See
`docs/adr/0009-shared-tool-loop-engine-for-run-and-execute.md`.

## What shipped (PR 2, 2026-07-11, #18)

`internal/toolloop` is a stateless `Engine` (messages in → `Result` out):
stream a completion, forward text/reasoning deltas, execute any tool
calls, feed results back, repeat until a natural stop or the turn budget.
It carries the guards the Phase 0 spike's model-behaviour findings called
for — none of which any framework would have supplied for free (see ADR
0011): a `MaxTokens` ceiling (spiral guard), per-turn de-duplication of
identical tool calls with the assistant message recording exactly the
calls answered (so every `tool_call` always has a matching result), a
per-turn call cap, graceful turn-exhaustion (partial content preserved,
`Exhausted` flag), and tool errors returned to the model as text rather
than aborting the run. The native read-only toolset (Read with
offset/limit paging, Grep, Glob) is workspace-confined with the
conservative output caps from "Further decisions taken" below.

`ChatClientRunner.Run` now drives this engine instead of the old
`StreamSessionTurn` synthetic-ack stub — closing the
`chatclient-tool-loop` task. Any run with a usable workspace gets the
read-only toolset (the unified rule: stage conversations *and* free chat
both gain repo grounding, matching `ClaudeRunner`'s free-chat behavior at
`reposRoot`); `RunInput.Tool` (the Draft-proposing tool) is the loop's
stop condition, surfaced as `RunOutput.ToolCall`. Because the engine is
stateless, `ChatClientRunner` now owns per-`SessionKey` history itself:
only the human turn and the assistant's final text persist across turns
(a Draft proposal folded into the assistant text, matching
`api.conversationHistoryToChatMessages`'s shape so a rehydration and the
live store are identical); the loop's intermediate tool-call/result
messages are ephemeral, keeping durable context flat per the
no-hidden-state invariant.

Also landed, the `internal/chat` foundation the loop needs: a `MaxTokens`
request field, and dual reasoning-key parsing (`reasoning_content` *or*
`reasoning`) so `gpt-oss`-family reasoning isn't silently dropped.

Verified end-to-end against a live LM Studio endpoint (not just unit
tests): the real runner drove `gpt-oss-20b` to grep and read
`internal/agentrunner/runner.go` and correctly ground an answer about the
`AgentRunner` interface entirely from the repository's own contents.

## What shipped (PR 3, 2026-07-12)

`ChatClientRunner.Execute` no longer returns `ErrExecuteNotSupported`: it
builds a `system` + fixed kickoff-message (`executionKickoffMessage`,
shared with `ClaudeRunner`) conversation and drives `internal/toolloop`'s
engine with `toolloop.ExecutionTools()` (`ReadOnlyTools()` plus two new
native tools, `write_file` and `edit_file`), `claudeExecutionMaxTurns`, and
no `StopTool` — the loop's only natural stop is the model finishing without
a further tool call, matching an autonomous Implementation-stage run.
`edit_file` follows this repo's own `Edit` tool convention: an exact
`old_string`/`new_string` replace that must be unique in the file unless
`replace_all` is set, rather than a line-numbered or diff-based patch
format — simpler for a small local model to emit correctly.

Turn exhaustion is Execute's one meaningful failure mode, distinct from
Run's graceful degradation (per the "Turn exhaustion" decision above): it
now surfaces as a Go error, with whatever partial `ExecuteOutput`
accumulated still returned alongside it, so a caller records a failed
`execution.yaml` rather than a silently incomplete one.

`ExecuteOutput`/`ExecuteEvent` parity with `ClaudeRunner` is achieved via
two additions to the shared engine rather than a second loop
implementation (`docs/adr/0009`'s bar): `toolloop.Config` gained optional
`OnToolCall`/`OnToolResult` hooks fired around each executed tool call —
`Run` leaves both nil (unaffected), `Execute` uses them to emit
`tool_call`/`tool_result` `ExecuteEvent`s — and `toolloop.Result` gained a
`TokensUsed` field. That closes the previously-open `internal/chat`
streaming-usage gap (below): `chat.CompletionRequest` now carries a
`StreamOptions` field `StreamChatCompletion` forces to
`{include_usage: true}`, and the final usage-only chunk a
`stream_options`-enabled server sends is decoded and surfaced as a new
`chat.Delta.Usage`, which the engine sums across turns. `CostEstimate`
stays 0, per the "further decisions taken" note below (no marginal cost
for local inference).

Verified end-to-end against a live LM Studio endpoint (`openai/gpt-oss-20b`,
matching PR 2's verified model): the real runner read a Go file containing
a stubbed function via `read_file`, used `edit_file` to implement it
correctly, and reported a non-zero `TokensUsed` — proving both the
write-enabled loop and the new usage-accounting path work against a real
model, not just a scripted fake in unit tests.

**Deliberately deferred, not dropped:** retiring
`chat.ChatClient.StreamSessionTurn`/`SeedSessionHistory` (dead code since PR
2) stayed out of this PR to keep its diff focused on `Execute` — see
"Follow-ups" below, unchanged from PR 2's note.

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

**Outcome (2026-07-11):** hand-rolled, with `eino` recorded as the
fallback — `docs/adr/0011-hand-roll-tool-loop-engine-over-eino-or-dive.md`.
The decisive finding was not the criteria table (where `eino` beat `dive`
on adapter cost and MCP support) but that the engine's real adversary is
local-model misbehaviour — spirals, duplicate calls, tool-call XML as
text, announce-but-stall turns — whose guards no framework provides, and
which `eino` actively fought (fatal-on-tool-error; `MaxStep` exhaustion
discards all progress). The spike's throwaway probes and full working
record stay on the unmerged `milestone8-phase0-spike` branch. Its most
reusable output — the model-behaviour findings — was packaged as the
standalone `cmd/modelprobe` qualification harness.

## Out of scope

* ~~**The framework pick itself.** Deferred to Phase 0's own ADR.~~
  Resolved: hand-rolled —
  `docs/adr/0011-hand-roll-tool-loop-engine-over-eino-or-dive.md`.
* **OS-level Bash sandboxing** (Landlock, Sandboxie, containers). See
  "Bash: scope and posture" above and
  `docs/adr/0010-defer-bash-sandboxing-for-execution-harness.md`.
* **Merge automation and knowledge-base promotion** — unrelated to this
  milestone's scope; still `docs/milestones/milestone7.md`.

## Open questions — resolved during the grill/spike (2026-07-10/11)

* **Where the shared engine lives** → a new `internal/toolloop` package,
  not inside `internal/chat`. `internal/chat` stays a clean
  OpenAI-compatible provider (wire format only); `internal/toolloop` holds
  the loop driver plus native Go tool implementations; `agentrunner`
  composes the two. This keeps the existing three-layer shape (`chat` →
  `toolloop` → `agentrunner`) rather than collapsing wire-format and
  tool-execution concerns together.
* **MCP tool-sourcing** → fast-follow, not initial. Read/Grep/Glob/Write/
  Edit are implemented natively in Go first (matching the
  `chatclient-tool-loop` task's own assumption and giving full control
  over the small-context result-shaping caps that matter for local
  models); the engine's toolset is a parameter, so an MCP-sourced tool —
  the LSP-bridge specifically, for its semantic-navigation token savings —
  slots in later without restructuring. The MCP *filesystem* server is not
  planned at all: native tools with controlled output win on tokens.
* **The spike's output format** → both a written comparison and throwaway
  per-candidate code, retained on the unmerged `milestone8-phase0-spike`
  branch (`spike/NOTES.md` + `spike/einoprobe`/`spike/diveprobe`); the
  durable conclusion is
  `docs/adr/0011-hand-roll-tool-loop-engine-over-eino-or-dive.md`.

## Further decisions taken (grill, 2026-07-10)

Settled during scoping, ahead of implementation:

* **Native Bash tool** → Git-for-Windows `bash.exe` (already a hard
  dependency via worktrees) on Windows, system `bash` elsewhere; cwd
  pinned to the execution worktree; **2-minute default per-command
  timeout**; no OS sandboxing per
  `docs/adr/0010-defer-bash-sandboxing-for-execution-harness.md`.
* **Turn exhaustion** → distinct terminal outcome preserving partial
  results: `Run` returns accumulated text and degrades gracefully;
  `Execute` fails loudly into `execution.yaml`.
* **`ExecuteOutput` metrics for a local model** → `NumTurns` from the loop
  counter, `DurationSeconds` wall-clock, `TokensUsed` summed from the
  OpenAI `usage` field (needs `stream_options.include_usage`),
  `CostEstimate` = 0 (local inference has no marginal API cost).
* **Tool-output caps** → conservative fixed defaults sized for a ~32k
  local model (Read ~1000 lines with offset/limit paging, Grep ~100
  matches, Bash ~20KB), each truncation explicit to the model. A
  configuration mechanism is deferred to a separate, as-yet-undefined
  task (`data/projects/llm-workbench/tasks/tool-output-caps-config/`).

## Known `internal/chat` gaps the engine must close (from the spike)

Surfaced hands-on during Phase 0; needed regardless of the (hand-rolled)
vehicle:

* ✅ **Closed (PR 2).** `chat.CompletionRequest` had no `MaxTokens` field —
  without it a spiralling or duplicate-call model generates unbounded and
  can wedge the endpoint. Now a request field, and the engine sets it on
  every completion.
* ✅ **Closed (PR 3).** No usage was surfaced on the streaming path —
  `stream_options: {include_usage: true}` is now forced on every streamed
  request and the final usage chunk is decoded into `chat.Delta.Usage`, so
  `Execute` reports real `TokensUsed` metrics (summed across turns by the
  engine).
* ✅ **Closed (PR 2).** Reasoning arrived under **`reasoning`** for some
  model families (e.g. `gpt-oss`) and `reasoning_content` for others
  (qwen/deepseek); `internal/chat` parsed only the latter. Now decodes
  both, preferring `reasoning_content`.

## Follow-ups

Tracked here so they aren't lost between PRs; none block Milestone 8's
remaining scope.

* **`Execute`'s Bash tool** — PR 4, per "Bash: scope and posture" above.
  Write/Edit landed in PR 3; Bash is the remaining tool, plus wiring it
  into `toolloop.ExecutionTools()`.
* **Retire `chat.ChatClient.StreamSessionTurn`/`SeedSessionHistory`** —
  dead code now that `ChatClientRunner.Run` drives the engine directly and
  owns its own session history (PR 2). Deliberately left out of PR 3 too,
  to keep that PR's diff focused on `Execute`; no task filed yet, still
  small enough to fold into the Bash PR or do as a standalone cleanup.
* **`tool-output-caps-config`** — the deferred configuration mechanism
  for the fixed output caps PR 2 shipped, now also covering PR 3's
  write/edit confirmation strings. Tracked at
  `data/projects/llm-workbench/tasks/tool-output-caps-config/`.
* **`modelprobe-toolloop-refactor`** — move `cmd/modelprobe`'s private
  loop onto `internal/toolloop` now that the engine exists, making it the
  engine's first non-runner consumer. Tracked at
  `data/projects/llm-workbench/tasks/modelprobe-toolloop-refactor/`.

---
type: Domain Note
title: Local-Model Tool-Loop Pathologies
description: The four tool-loop misbehaviors small/local LLMs exhibit under sustained agentic tool use, and the engine-level guards that mitigate each.
tags: [local-llm, tool-loop, agent-reliability, model-behavior]
timestamp: 2026-07-23T00:00:00Z
---

Small/local models driven through a multi-turn, tool-calling agent loop
fail in a small, recurring set of ways — independent of which orchestration
framework (or hand-rolled loop) drives them. This was the decisive finding
of a hands-on spike comparing a hand-rolled tool-loop engine against two
frameworks (`eino`, `dive`): the frameworks differed in adapter cost and
API shape, but **neither one prevented, detected, or recovered from any of
these failures** — the guards are custom logic that has to live in the
loop/toolset layer regardless of vehicle. See `local-tool-loop-pathologies`
below for the four failure modes, evidenced against real local models on
LM Studio, and the guards this workbench's own tool loop
(`internal/toolloop`) implements in response.

## The four pathologies

1. **Duplicate tool calls.** The model emits the same `(name, arguments)`
   pair more than once within a single turn — observed as low as 2-4
   repeats and as high as 421+ identical calls in one response, when given
   a terse system prompt and no output cap. Bounded only by the response
   length limit; a "call each tool at most once" system-prompt instruction
   tames but does not cure it.
2. **Repetition spirals.** The model gets stuck repeating an n-gram (a
   short word window) past any useful length, in either the content or
   reasoning channel — e.g. "Let me just answer it once more" ad infinitum
   — until it hits the output cap or the human/loop stops it. Distinct
   from duplicate tool calls: this is prose repetition, not a repeated
   structured call.
3. **Tool-call XML leaking into the wrong channel.** Instead of emitting a
   structured `tool_calls` entry, a thinking-mode model sometimes writes
   `<tool_call>...</tool_call>`-shaped XML as plain text inside its
   reasoning or content channel. The wire protocol only parses tool calls
   from the dedicated `tool_calls` field, so this is silently
   unparseable — the turn ends with no tool actually invoked and no error
   raised. Turning thinking mode off eliminates this specific failure
   (there is no reasoning channel left to hide the XML in), but does not
   fix the other three.
4. **Announce-then-stall.** The model narrates an intended tool call in
   prose ("Let me look for...", "I'll check the file...") and then ends
   its turn without actually emitting the call — an ungrounded stop
   disguised as progress.

A run can succeed (produce a correct, grounded final answer) while still
having exhibited one of these along the way — e.g. a duplicate call the
loop deduped past. The four are tracked independently of overall success
in this workbench's own qualification tool, `cmd/modelprobe`
(`runResult.dupCall`/`.spiral`/`.toolXML`/`.stall`,
`cmd/modelprobe/loop.go`), which scores a candidate OpenAI-compatible
model/endpoint by running it through a bounded read/grep loop and
classifying which pathologies it exhibits.

## A related, separate wire-format gotcha

Not a loop pathology (it never causes a bad turn), but a real
model-family inconsistency worth knowing before it looks like one: LM
Studio surfaces `gpt-oss-20b`'s reasoning text under a `reasoning` field,
not `reasoning_content` — the field name most other reasoning-capable
models (e.g. the Qwen3 family) use. A client that only reads
`reasoning_content` silently drops that model's reasoning with no error.
Check both field names when consuming a new model family's reasoning
output.

## The guards, and where they live

None of the four pathologies are solved by picking a different
orchestration framework — the fix is loop-level engineering that has to
exist regardless of vehicle:

- **Per-turn duplicate-call deduping and a per-turn call cap** — bounds
  pathology 1. `internal/toolloop/engine.go`'s `dedupeCalls`/`capCalls`,
  gated by `Config.MaxToolCallsPerTurn` (default
  `defaultMaxToolCallsPerTurn`).
- **A `max_tokens` ceiling on every generation** — the load-bearing guard
  against pathology 2 (and a backstop on 1): without it, a spiral or a
  duplicate-call run has no natural end. `internal/toolloop.Config.MaxTokens`
  (`ChatClientRunner`'s `chatClientMaxResponseTokens`).
- **Per-request and whole-run timeouts** — a spiral or a hung endpoint
  must not consume unbounded wall-clock time. `AgentRunner.Run`/`Execute`
  callers set these; `internal/agentrunner`'s `AGENT_TIMEOUT`.
- **A defined turn-exhaustion outcome that preserves partial results,
  never a hard failure that discards progress** — this was a genuine
  framework failure mode found in the spike (one framework's step-count
  exhaustion raised a hard error that discarded everything the run had
  done), the opposite of what pathology 4's "the model tried and ran out
  of room" case actually needs. `internal/toolloop.Result.Exhausted` — Run
  degrades gracefully with whatever content accumulated; Execute treats
  exhaustion as a reportable failure but still keeps the partial output.
- **Tool errors fed back to the model as a normal tool result, never
  treated as fatal to the run** — a misbehaving or exploratory model that
  hits an error (e.g. reads a path that doesn't exist) needs the error as
  text it can read and recover from, not a crashed loop. Also a genuine
  framework failure mode found in the spike (one framework killed the
  entire run on a tool's Go error). `internal/toolloop/engine.go`'s
  `executeCall`.
- **A pre-flight health probe** before committing to a run — cheap
  insurance against burning a full turn budget against an endpoint that's
  currently in a broken state (see "Endpoint instability" below).
  `AgentRunner.CheckHealth`.

## Reliability varies enormously by model, not just by framework

The spike's clearest data point: swapping the model fixed both frameworks
simultaneously, while swapping the framework never fixed a failing model.
`qwen3.6-35b-a3b`'s reliability ranged from roughly 15% (base) to 60-75%
(a specific community finetune) depending on thinking mode and sampling
config, and was still capable of the announce-then-stall and XML-in-text
failures at that improved rate. `openai/gpt-oss-20b`, by contrast, was
clean across every framework/config combination tested (6/6), the most
reliable target found. Before trusting a new local model in an agentic
tool loop, qualify it with `cmd/modelprobe` rather than assuming
reliability from a chat-only impression of the model.

## Endpoint instability (operational, not model-specific)

Separately from any single model's own behavior, the LM Studio endpoint
itself was observed oscillating between healthy and broken states
(stray `</think>` leaking into plain content, empty responses, indefinite
hangs) across otherwise-identical requests. This is why a pre-flight
health probe and hard per-request timeouts are non-negotiable regardless
of which model or framework is in use — a request against a currently-sick
endpoint must fail fast and visibly, not hang until a human notices.

# Citations

[1] [ADR-0011: Hand-roll the shared tool-loop engine; keep eino on record as the fallback](../../../docs/adr/0011-hand-roll-tool-loop-engine-over-eino-or-dive.md) — the decision this catalog's evidence backs, including the full framework-comparison criteria.

[2] [ADR-0009: Shared tool-loop engine for Run and Execute](../../../docs/adr/0009-shared-tool-loop-engine-for-run-and-execute.md) — the engine design (toolset/workspace/turn-bound/stop-condition) these guards are built into.

[3] `cmd/modelprobe` (`internal/checks.go`, `loop.go`) — the qualification tool that packaged these findings into a repeatable per-model/endpoint scoring harness, with an LM-Studio-specific fleet mode for sweeping every downloaded tool-capable model.

[4] `milestone8-phase0-spike` branch, `spike/NOTES.md` — the full hands-on working record (raw per-run evidence, wire-level dumps, and the framework-specific failure modes) this catalog is synthesized from. Never merged to `main`; kept for archaeology.

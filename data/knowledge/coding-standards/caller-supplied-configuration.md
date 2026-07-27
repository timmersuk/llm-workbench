---
type: Coding Standard
title: Caller-Supplied Configuration, Never Inferred or Runner-Defaulted
description: Per-context values (turn budgets, timeouts, any knob that legitimately varies by call site) must be explicit values the caller computes and passes in — never guessed by a lower layer from an unrelated flag, and never backstopped by a hardcoded per-implementation constant.
tags: [agentrunner, code-review, anti-pattern, spaghetti-prevention]
timestamp: 2026-07-27T00:00:00Z
---

A lower layer (a runner, a client, a store) must never decide a
per-context configuration value on its own — not by inferring it from
some other field that happens to correlate today, and not by
substituting a hardcoded constant when the caller left it unset. The
value belongs to whichever layer actually knows the context, computed
explicitly, and passed down. This came out of a real incident and a
real near-miss in this codebase, both worth remembering in detail
because the two failures are opposite-looking but the same root cause.

## The incident: one constant, reused past its original context

`internal/agentrunner/claude_runner.go` bounded every agent call's
tool-call round-trips with a hardcoded `const claudeRunnerMaxTurns =
30`, shared by Requirements, Planning, Review, and free chat — and a
second constant, `claudeExecutionMaxTurns = 1000`, shared across both
`ClaudeRunner.Execute` and `ChatClientRunner.Execute`. Review was
originally a read-only, interview-shaped conversation like
Requirements/Planning, so 30 turns was plenty. Milestone 6 later
widened Review to run the confined bash tool over the executed change
— tests, live smoke-testing, multi-step verification — a workload
shaped like Execute's, not like an interview. Nobody revisited the
turn cap when that widening happened, because the cap lived in the
runner, not in the code that actually knew Review's shape had changed.
It failed for real: a Review conversation ran a long build/test/verify
loop and hit the 30-turn ceiling mid-verification.

## The near-miss: the first proposed fix would have repeated it

The obvious-looking quick fix was to give Review a bigger cap by having
`ClaudeRunner`/`ChatClientRunner` check `RunInput.EnableBashTool` (the
flag Review sets to unlock the bash tool) and pick a higher constant
when it's true. This was rejected before it was written, because it's
the identical failure shape one layer down: a runner *inferring*
config from a flag whose actual, documented purpose is something else
entirely (toolset widening, not turn budgeting). It happens to work
only because Review is currently the only caller that sets
`EnableBashTool` true. The moment anything reuses that flag for an
unrelated reason, or a new stage sets it for its own purposes, the
turn budget silently changes for the wrong reason — no error, just
quietly wrong behavior waiting to be rediscovered the same way the
original bug was.

## The actual fix: explicit value, one owner, zero means unbounded

`agentrunner.RunInput`/`ExecuteInput` gained an explicit `MaxTurns int`
field. The *caller* — `internal/api/stage_conversation.go`'s
`resolveStageRun` (which already branches on stage) and
`internal/api/execution.go` (which knows it's building an Execute
call) — computes the right value and passes it down. No
`AgentRunner` implementation infers it from another field, and none
substitutes a hardcoded constant when it's left at zero: zero means no
turn-count limit at all (the call is still bounded by the runner's own
configured timeout, just not by a turn count), never "silently use
some other default." `internal/toolloop/engine.go`'s loop condition
was changed to match that contract (`MaxTurns <= 0` runs unbounded,
rather than the previous `for turn := 1; turn <= 0` accidentally
running zero turns).

This also surfaced a real cross-executor inconsistency that the
constant-sharing had been quietly papering over: `CodexRunner` has no
turn-cap mechanism at all today (bounded only by wall-clock timeout).
Making the value explicit, rather than baked into one runner's
constant, is what made that gap visible instead of assumed away — see
`data/projects/llm-workbench/tasks/configurable-execution-max-turns`
for the still-open decision on whether Codex needs one too.

## Checklist — red flags when reviewing or writing this shape of code

- A hardcoded constant reused across more than one call site or
  purpose ("just bump the number" is a stopgap, not a fix — it buys
  time, it doesn't remove the shared-constant coupling).
- A lower layer reading a flag or field for anything other than its
  one documented purpose, to infer some *other* behavior. If two
  concerns correlate today, that is not a reason to let one imply the
  other — pass both explicitly.
- "It happens to be true right now that only X calls this with Y set"
  reasoning. That's a fact about today's callers, not an invariant the
  callee can rely on.
- Fixing the one reported instance without asking whether the same
  root cause exists anywhere else. Here, checking turned up the
  identical bug already latent in `ChatClientRunner` (same shared
  constant) and a related, differently-shaped gap in `CodexRunner` (no
  cap mechanism at all) — both found only by asking "where else does
  this pattern live" instead of patching the one call site that failed.

# Citations

[1] [Engineering conventions — Requirements/Planning agent executors](../../../docs/engineering%20conventions.md#requirementsplanning-agent-executors) — the concrete convention this doc's principle backs.

[2] `internal/agentrunner/runner.go` (`RunInput.MaxTurns`, `ExecuteInput.MaxTurns`), `internal/api/stage_conversation.go` (`resolveStageRun`, `requirementsPlanningMaxTurns`/`reviewMaxTurns`), `internal/api/execution.go` (`executionMaxTurns`), `internal/toolloop/engine.go` (turn-loop condition) — the actual code this doc describes.

[3] `data/projects/llm-workbench/tasks/configurable-execution-max-turns` — the still-open follow-up (per-task configurability, auto-derivation, the Codex turn-cap asymmetry).

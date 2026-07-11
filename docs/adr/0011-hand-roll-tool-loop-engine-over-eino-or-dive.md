# Hand-roll the shared tool-loop engine; keep eino on record as the fallback

Milestone 8's Phase 0 called for a time-boxed spike comparing three
implementation vehicles for the shared tool-loop engine of
`docs/adr/0009-shared-tool-loop-engine-for-run-and-execute.md` —
hand-rolled, `github.com/cloudwego/eino`, and
`github.com/deepnoodle-ai/dive` — scored against five criteria in priority
order (client fidelity, shared-engine fit, MCP tool-sourcing, output
parity, dependency weight), and to conclude with its own ADR naming the
pick (`docs/milestones/milestone8.md`). That spike ran. We chose to
**hand-roll** the engine and **not** adopt either framework, recording
`eino` as the designated fallback should orchestration needs later outgrow
a hand-rolled loop.

The spike wired a throwaway adapter and two-tool (Read/Grep) agent loop
for each framework over the existing `internal/chat` client, and ran them
hands-on against local models on LM Studio. Its full working record lives
on the unmerged `milestone8-phase0-spike` branch (`spike/NOTES.md` and the
`spike/einoprobe`/`spike/diveprobe` probes) — retained for archaeology,
never merged, so the framework dependencies it pulled into `go.mod` do not
reach `main`.

## What the criteria showed

Both frameworks *can* be backed by `internal/chat` without regressing
incremental tool-call-delta streaming or `reasoning_content` — the exact
axis `docs/adr/0006-defer-langchaingo-retain-openai-go-sdk-chat-client.md`
rejected langchaingo over — so criterion 1 was not a disqualifier for
either, and dive's provider-extensibility (unconfirmed from docs at
scoping time) resolved positive: `AgentOptions.Model` takes the interface
directly, no registry or fork. But the two adapters were not equal in
cost. `eino`'s `ToolCallingChatModel` is OpenAI-shaped, matching
`internal/chat`; the adapter was ~150 lines. `dive`'s core types are
Anthropic-shaped (content-block messages; `Stream` must emit a full
`message_start → content_block_* → message_delta → message_stop` event
sequence its accumulator reassembles), making its adapter a ~380-line
shape translation. On criterion 3, `eino` has a ready-made
`eino-ext/components/tool/mcp` component that surfaces MCP servers as
client-side tools — exactly the LSP-bridge fast-follow shape; `dive` has
no client-side MCP at all (its MCP fields are pass-through config for
hosted Anthropic/OpenAI connectors, inert with a custom provider). On
criterion 5, `eino` (~12.2k stars) outweighs `dive` (~128, plus a `wonton`
transitive dependency). So between the two frameworks, `eino` was clearly
ahead — which is why it, not `dive`, is the recorded fallback.

## Why hand-rolled beat the winning framework

The decisive evidence was not in the criteria table but in what the spike
revealed by running real local models through both loops. Across three
models (`qwen3.6-35b-a3b`, a finetune of it, and `openai/gpt-oss-20b`),
the engine's real adversary is **local-model misbehaviour**: repetition
spirals, the same tool call emitted several times in one turn, tool-call
XML written into the reasoning/content channel instead of as a structured
call, and turns that narrate an intended tool call and then stop. The
guards these demand — per-turn duplicate-call deduping and caps, a
`max_tokens` ceiling so a spiral cannot run unbounded, per-request and
whole-run timeouts, spiral detection, a pre-flight health probe, and a
defined turn-exhaustion outcome that preserves partial results — are
custom logic **no framework provides**. They live in the toolset/loop
layer we own regardless of vehicle.

Worse, the winning framework actively fought two of them. `eino`'s react
agent treats a Go error returned from a tool as fatal to the entire run
(`NodeRunError`), where a misbehaving model wants the error fed back as a
tool result so it can self-correct — `dive`'s loop did exactly that and
recovered where `eino` died. And `eino`'s `MaxStep` counts graph
node-steps rather than model turns, with exhaustion raising a hard
`GraphRunError` that discards all progress — the direct opposite of the
turn-exhaustion semantics
`docs/adr/0009-shared-tool-loop-engine-for-run-and-execute.md` and this
milestone require (partial results preserved; `Run` degrades gracefully,
`Execute` fails loudly). Both are surmountable by wrapping, but each
wrapper is engine logic we would write and maintain *around* the framework
rather than *instead of* it.

Set against that, a hand-rolled loop is small and fully ours: an
OpenAI-compatible tool-calling round-trip against a scoped workspace,
bounded by a turn count and a stop condition — the shape
`docs/adr/0009-shared-tool-loop-engine-for-run-and-execute.md` already
specifies, parameterised by toolset/workspace/turn-bound/stop-condition.
Prior art on hand-rolled Go agent harnesses (and this repo's own existing
streaming/tool-call-accumulation code in `internal/chat`) puts the loop
itself at a few hundred lines with zero new dependencies. Given that the
misbehaviour guards must be hand-written either way, taking on a framework
dependency to supply only the small, well-understood loop — while fighting
its error and exhaustion opinions — is a poor trade.

This is the situation `docs/adr/0005-defer-agenticgokit-adoption.md` named
as its own revisit trigger (genuine autonomous multi-step orchestration),
now evaluated hands-on rather than deferred: at this milestone's scope the
orchestration is a bounded single-agent loop, which does not clear the bar
for a framework.

## The fallback, and when to revisit

This is a decision for the engine as scoped now, not a rejection of `eino`
for all time. `eino` is the recorded fallback: if a later milestone needs
genuinely autonomous multi-step orchestration beyond a bounded
single-agent loop — sub-agent fan-out, graph-structured multi-step plans,
or MCP tool-sourcing rich enough that its `eino-ext` component saves more
than the hand-rolled loop costs — revisit then, with this spike's adapter
on the `milestone8-phase0-spike` branch as the starting point rather than
a blank slate.

## Side effect: the modelprobe harness

The spike's most reusable output was not the framework verdict but the
model-behaviour findings, which were packaged into a standalone
qualification tool (`cmd/modelprobe`) that scores an OpenAI-compatible
model/endpoint for tool-loop usability — wire-shape checks plus an N-run
loop with the pathology detectors above. It is provider-agnostic, with an
LM-Studio-specific fleet mode for sweeping every downloaded tool-capable
model. Milestone 8 PR 2 refactors it onto the hand-rolled engine this ADR
selects, as that engine's first consumer.

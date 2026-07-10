# Milestone 8 Phase 0 spike notes

Working notes for the hand-rolled vs `dive` vs `eino` comparison. Feeds ADR 0011.
Validation target: `qwen3.6-35b-a3b-mtp` on LM Studio at `192.168.1.240:1234`.

## Endpoint baseline (raw curl, before any framework)

- ✅ Structured tool calls emitted, non-streaming and streaming. Streaming
  delta chunks carry `index`/`id`/`name`/`arguments` in exactly the shape
  `internal/chat/toolcall.go` already accumulates.
- ✅ `reasoning_content` streams token-by-token as its own delta stream,
  before content/tool calls.
- ✅ Tool-result round-trip (assistant tool_calls msg → tool-role msg →
  final answer) produces a correct grounded answer. ~8s warm.
- ⚠️ `tool_choice: "required"` hangs LM Studio indefinitely (>120s, no
  response; server recovers after). The engine must never send it.
- ⚠️ Runaway tool-call repetition observed: one response emitted 421+
  copies of the same call when given a terse system prompt and no
  `max_tokens`. Per-turn tool-call caps and `max_tokens` are load-bearing.
- ⚠️ Cold-start latency exceeds 60s; generous client timeouts required.
  Short-timeout requests that get abandoned wedge the queue behind them.

## eino (github.com/cloudwego/eino v0.9.12)

**Criterion 1 (client fidelity): PASS, hands-on.**

- Custom `model.ToolCallingChatModel` over `chat.ChatClient` is ~150 lines
  (`spike/einoprobe/chatmodel.go`), compiled first try, no type fights.
  `chat.ChatClient.StreamChatCompletion(ctx, req, onDelta)` is the natural
  seam — messages-list in, delta callback out.
- `schema.Message.ReasoningContent` is a first-class field; reasoning
  streamed through adapter → react agent → consumer without loss.
- `schema.ToolCall.Index` exists specifically for stream-chunk merging;
  since `internal/chat` emits only fully-accumulated tool calls, the
  adapter assigns indexes itself — no impedance mismatch.
- One accommodation required: eino's default stream tool-call detection
  checks only the FIRST chunk, which fails for reasoning-first models. The
  `StreamToolCallChecker` hook exists for exactly this; a ~20-line
  reasoning-aware checker fixed it. Documented in the config's own
  comments — supported extension point, not a fight.

**Criterion 2 (shared-engine fit): promising, react agent probe ran.**

- `react.NewAgent` two-tool (read_file, grep_search) loop over the repo
  completed a real multi-step exploration autonomously in 1m25s and
  produced a correct, grounded answer (identified `AgentRunner` + all 5
  methods from actually reading `runner.go`).
- Parameterization points map to the engine's needs: toolset via
  `ToolsConfig` / `WithTools` (immutable binding), turn bound via
  `MaxStep`, stop condition via `ToolReturnDirectly` (a Draft-tool-call
  stop maps onto this) + natural stop on no-tool-call. Workspace scoping
  stays in our tool implementations, as it would hand-rolled.
- Not yet assessed: whether the react agent's graph machinery gets in the
  way of Run's "stop on Draft tool-call and surface it as RunOutput"
  shape vs Execute's run-to-completion shape.

**Criterion 4 (output parity): gap found in internal/chat, not eino.**

- `chat.Delta` carries no usage field and `chat.Message` has no
  reasoning_content field (non-streaming path drops reasoning). For
  TokensUsed the client must set `stream_options: {include_usage: true}`
  and surface the final usage chunk — an internal/chat change needed
  REGARDLESS of framework choice.

## dive (github.com/deepnoodle-ai/dive v1.14.0)

**Criterion 1 (client fidelity): adapter buildable, but ~2.5x the surface of eino's.**

- `AgentOptions.Model` accepts the `llm.LLM` interface directly — the
  milestone doc's open question ("providers/<name> auto-registration
  external-extensibility unconfirmed") resolves POSITIVE: no registry,
  no fork needed.
- BUT dive's core types are Anthropic-shaped, not OpenAI-shaped: messages
  are content-block lists (`TextContent`/`ToolUseContent`/
  `ToolResultContent`/`ThinkingContent`), and `Stream` must emit a full
  Anthropic SSE event sequence (`message_start` → `content_block_start/
  delta/stop` per block → `message_delta` → `message_stop`) that dive's
  `ResponseAccumulator` reassembles. The adapter
  (`spike/diveprobe/llm.go`) is a genuine OpenAI→Anthropic shape
  translation with a block-state machine — ~380 lines vs eino's ~150.
- Reasoning has a home: `EventDelta.Thinking` (`thinking_delta`) and
  `ThinkingContent` blocks. `Usage` is first-class on events/responses.
- Options-pattern API (`Generate(ctx, opts ...Option)` resolved via
  `Config.Apply`) — fine, but the adapter must handle a large Config of
  which we honor a fraction (Prefill, ReasoningBudget, Caching etc. are
  hosted-provider concepts).

**Criterion 3 (MCP tool-sourcing): dive has NO client-side MCP.**

- dive's go.mod has no MCP dependency at all. Its `MCPServerConfig` /
  `MCPToolConfiguration` fields are pass-through config for Anthropic/
  OpenAI *hosted* MCP connectors — inert with a custom provider. An MCP
  filesystem/LSP-bridge server would need a hand-rolled MCP-client →
  `dive.Tool` bridge.
- eino by contrast has `eino-ext/components/tool/mcp` (v0.0.8): MCP
  servers surface as client-side `tool.BaseTool`s — exactly the
  fast-follow shape.

**Criterion 5 (dependency weight):** dive pulls `deepnoodle-ai/wonton`
and misc CLI deps (ansi, colorable, term) into go.mod; eino pulled a
similar-sized set (kin-openapi etc.). Neither is heavy; eino ~12.2k
stars vs dive ~128 remains the maintenance-risk gap.

**Live runs: CONFOUNDED by endpoint instability — rerun needed.**

- Two dive runs (same tools/question/system prompt as eino's successful
  run) produced zero tool calls and a degenerate reasoning spiral ("Let
  me just answer it once more" ad infinitum) until timeout. Stripping
  dive's unconditionally-appended system-prompt priming rule
  (`agent.go:489`) did NOT fix it.
- BUT a control rerun of the previously-successful einoprobe then ALSO
  hung >10min, and a raw health check ("reply with one word") returned
  literal `</think>\n` as content — the endpoint itself had degraded to
  the same broken state seen in the first morning probes. The dive
  spirals are therefore not attributable to dive until both probes are
  rerun back-to-back against a healthy endpoint.
- dive quirk found regardless: it unconditionally appends a
  `<system-reminder>` priming sentence to every system prompt — no
  opt-out short of stripping it in the adapter.

## Post-reload A/B series (2026-07-10 afternoon, presence_penalty=1.5, temp=1.0)

Six runs after model reload + Qwen-recommended sampling reconfig:

| run | structured tool calls | outcome |
|---|---|---|
| eino #1 | 0 | model wrote `<tool_call>` XML inside its THINKING block; LM Studio can only parse tool calls from the content channel; run ended unanswered (39.7s) |
| dive #1 | 2 | ✅ clean: grep → read_file → correct grounded 5-method answer (53.9s) |
| eino #2 | 0 | same XML-in-thinking failure (20.4s), plus confabulated "previous search" |
| eino #3 | 0 | answered from priors without tools (34.5s) |
| dive #2 | 0 | reasoning spiral to 5m timeout — EVEN WITH presence_penalty=1.5 |
| eino #4 | 0 | answered without tools (11.5s) |

Wire-level diff between the two adapters' requests is negligible (verified
by dumping both: same tool schemas modulo `additionalProperties` and key
order; dive appends one priming sentence). Conclusion: **the variance is
the model, not the frameworks.** qwen3.6's thinking mode emits tool-call
XML inside reasoning (unparseable), answers from priors, or spirals —
stochastically, under the model card's own recommended sampling. Both
frameworks drove correct loops when the model cooperated (eino in the
morning run, dive in dive #1).

Next probe step: `/no_think` (or `chat_template_kwargs.enable_thinking:
false`) — non-thinking mode should stabilize structured tool emission;
reasoning-fidelity criterion is already proven from the healthy-state runs.

## Endpoint instability (cross-cutting finding)

The LM Studio endpoint oscillates between healthy (correct tool calls,
grounded multi-turn loops) and broken (stray `</think>` in content, empty
responses, degenerate repetition spirals, indefinite hangs). Engine
implications, regardless of framework choice:

- Per-request timeouts AND `max_tokens` are mandatory — a spiral without
  them costs 30 minutes of box time (dive's default `ResponseTimeout`).
  `chat.CompletionRequest` currently has NO MaxTokens field — needs
  adding for the engine regardless of framework.
- Repetition-spiral detection (same content repeated N times) is worth
  considering as a loop guard.
- A pre-flight health probe (cheap completion, sane response) before an
  Execute run would avoid burning a 100-turn budget on a sick endpoint.

## Hand-rolled baseline

- Trivially passes criteria 1–2 by construction (it IS this repo's client).
- The runaway-tool-call and tool_choice findings above apply to any
  implementation.

## /no_think round (2026-07-10 late afternoon)

- `/no_think` in the system prompt is NOT honored by LM Studio's current
  chat template for this model — reasoning blocks still stream. Disabling
  thinking needs the template-level `enable_thinking=false` or
  `chat_template_kwargs` route instead.
- dive: another clean run (2 structured calls, grounded answer, 32s).
  Post-reload dive tally: 2 clean / 1 spiral / 1 killed.
- eino: two more failures — XML-in-thinking again, then a degenerate
  "definition of definition of" mini-spiral (post-reload tally 0/6).
- Canonicalizing eino's tool-schema key order (type-first, matching
  dive's) did not fix it, ruling out the last observable wire-level
  difference. Remaining hypothesis: per-run stochastic behavior + a
  template that parses tool calls only from the content channel, with
  qwen3.6 thinking mode frequently emitting them into reasoning instead.
- eino's react agent surfaced one design difference under failure: when
  the model emits no structured call, eino returns whatever streamed
  (including reasoning) and stops — dive's loop retries/timeouts. Both
  behaviors are configurable; neither is disqualifying.

## Status / next steps

- Framework-architecture evidence is complete (criteria 1, 3, 5 resolved;
  criterion 2 partially — both drove correct loops when the model
  cooperated). Criterion 2's remaining question is fairness-blocked on a
  stable endpoint, not on framework behavior.
- Options: (a) disable thinking at the TEMPLATE level in LM Studio and
  rerun the A/B; (b) drop temperature to 0.6; (c) conclude the spike on
  existing evidence, noting model-reliability dominates framework choice.

## Thinking-disabled round (template-level, temp 0.6, repeat 1.0, presence 1.5)

- Thinking OFF confirmed working (reasoning_content empty, 0 reasoning
  tokens). Structured tool calls PARSE again — the XML-in-thinking failure
  mode is eliminated because there is no thinking channel to hide in.
- NEW pathology signature confirmed: the model duplicates the same tool
  call N times in one response (4+ identical read_file calls). Bounded
  only by max_tokens; tamed (not cured) by a "call each tool at most
  once" system-prompt instruction. ENGINE MUST dedupe identical tool
  calls per turn and cap calls per turn.
- Both framework probes streamed NOTHING in this configuration and hung/
  timed out — because neither probe (nor internal/chat!) can set
  max_tokens, the unbounded duplicate-call generation never finished.
  chat.CompletionRequest needs a MaxTokens field before ANY further live
  probing (also needed for the engine regardless — already noted).
- Streaming + thinking-off + max_tokens=300 via raw curl: tool-call
  delta chunks stream correctly (3 chunks: id/name, args, finish).

## Spike verdict (draft, for ADR 0011)

All five criteria now have evidence:
1. Client fidelity: both wrappable; eino ~150-line adapter vs dive ~380
   (Anthropic-shape translation). eino better.
2. Shared-engine fit: both drove correct multi-step grounded loops when
   the model cooperated (eino morning run; dive 2x post-reload). Both
   need config accommodations; neither disqualified.
3. MCP: eino has eino-ext component; dive has none client-side. eino.
4. Output parity: blocked in internal/chat for all candidates equally
   (no usage, no max_tokens, no reasoning on non-streaming Message).
5. Weight: eino 12.2k stars vs dive 128 + wonton dep.

Meta-finding that dominates: local-model misbehavior (spirals, duplicate
calls, tool-calls-as-text, template sensitivity) is the engine's real
adversary. The guards needed (per-turn dedupe + caps, max_tokens,
timeouts, spiral detection, health probes, template-quirk tolerance) are
custom logic NO framework provides — they live in our tools/loop layer
either way. This weakens the case for taking on a framework dependency
for the loop itself (small, well-understood) and strengthens hand-rolled
with eino as the fallback if orchestration needs grow. ADR to argue this
properly.

## New-model round: qwen3.6-35b-a3b-uncensored-hauhaucs-aggressive

Raw probe: exactly ONE tool call (no duplication), concise coherent
reasoning, clean finish. Framework A/B, one run each:

- dive: ✅ 3 structured calls (grep → grep → read runner.go), correct
  grounded 5-method answer, 33.4s.
- eino: ✅ grounded 5-method answer with reasoning referencing prior
  reads, 49.3s. (einoprobe's "[tool call]" counter shows 0 because
  react agent.Stream only surfaces the FINAL turn's stream — intermediate
  tool turns need callbacks; logging artifact, not absence.)

Confirms the spike's meta-finding A/B-style: swapping the model fixed
both frameworks simultaneously. Model choice dominates framework choice
for loop reliability. This finetune is a viable validation target for
PR 2-4 engine work.

## hauhaucs repeats (3x alternating + initial round)

Tally: eino 2/4, dive 3/4. Failures are the same two qwen-isms at lower
frequency: (1) tool-call XML emitted as text mid-loop, (2) early stop —
model narrates its next tool call ("Let me look for...") then ends the
turn without calling. Both frameworks fail identically when the model
does this. ~60-75% loop reliability vs base qwen3.6's ~15%.

Engine implication: the loop must handle "model announced intent but
stopped" — either a bounded auto-continue nudge or surfacing a clear
partial-result state. (Matches the turn-exhaustion design from the
grilling: distinct terminal outcome, partial results preserved.)

## Non-qwen control: openai/gpt-oss-20b

Raw probe: single clean tool call, terse reasoning (7 tokens). WIRE
FINDING: LM Studio surfaces gpt-oss reasoning under `reasoning`, NOT
`reasoning_content` — internal/chat parses only the latter, so reasoning
is silently dropped for this model family. The client must parse both
keys (engine requirement, framework-independent).

A/B series: **dive 3/3 clean** (~35s each, first try). eino 0/3 then
3/3 after two probe fixes that exposed real framework semantics:

1. **Tool errors are fatal in eino by default** — a Go error from an
   InvokableTool kills the whole run (NodeRunError). dive's ToolResult
   carries is_error back to the model, which self-corrects (gpt-oss
   probed a speculative path `agentexecutor.go?maybe?`; dive recovered,
   eino died). Fix: eino tools must return errors as result text.
2. **eino MaxStep counts graph node-steps (~2 per model turn), not
   turns** — MaxStep 12 ≈ 6 turns, exhausted by gpt-oss's thorough
   grep/read habits, and **exhaustion is a hard GraphRunError discarding
   all progress** — directly violating the milestone's turn-exhaustion
   design (partial results preserved). Hand-rolled or wrapped, the
   engine must own this semantics.

gpt-oss-20b is the most reliable validation target tested (6/6 across
frameworks once configured; ~30s/run) and de-risks the "building against
qweny behaviour" concern: both adapters + loops work unchanged against a
non-qwen family, with only the reasoning-key naming differing on the wire.

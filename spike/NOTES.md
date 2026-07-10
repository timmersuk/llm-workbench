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

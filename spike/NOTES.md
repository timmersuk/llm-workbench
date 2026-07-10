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

## dive (github.com/deepnoodle-ai/dive)

(pending)

## Hand-rolled baseline

- Trivially passes criteria 1–2 by construction (it IS this repo's client).
- The runaway-tool-call and tool_choice findings above apply to any
  implementation.

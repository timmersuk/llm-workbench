# ClaudeRunner sets `--tools`, not just `--allowedTools`, to actually gate its tool surface

`internal/agentrunner/claude_runner.go`'s `clientFor` (the `Run`/stage-conversation
path) and `Execute` only ever called `claudecode.WithAllowedTools(...)` when
constructing a session, built from `readOnlyTools = {"Read","Grep","Glob"}` (plus
`Bash` for Review, plus Draft/knowledge MCP tool names) or `executionTools`
(`readOnlyTools` + `Write`/`Edit`/`Bash`). The surrounding comments, and
docs/adr/0013, treated that allow-list as the enforcement of the read-only trust
boundary in docs/architectural invariants.md ("can read files in the reference
repo, not can modify").

That was a mistaken reading of the underlying `claude` CLI. `WithAllowedTools`
maps to `--allowed-tools`/`--allowedTools`, which the CLI's own `--help` describes
as "list of tool names to allow" — paired with `--disallowed-tools`/
`--disallowedTools`, "list of tool names to deny". Together these are a
*permission* auto-approve/deny list: whether a call needs an interactive prompt.
They are not a restriction on which tools exist in the model's toolset at all. The
flag that actually does that is `--tools` — "Specify the list of available tools
from the built-in set. Use \"\" to disable all tools, \"default\" to use all
tools, or specify tool names (e.g. \"Bash,Edit,Read\")" — exposed by the SDK
(`github.com/severity1/claude-agent-sdk-go@v0.6.22`) as `WithTools`, which nothing
in `internal/agentrunner` called.

The practical consequence: every `ClaudeRunner`-backed session — Requirements/
Planning/Review stage GrillMe chats, and `Execute` — ran with the CLI's full
default built-in tool surface (`--tools` omitted = "default" = all built-in
tools), including `Task`/`Agent` subagent spawning, `Bash`, `Write`, `Edit`,
`WebFetch`, and skill-provided tools like `ScheduleWakeup` (from the `/loop`
skill), regardless of `readOnlyTools`/`executionTools`. This was directly
observed: a Requirements-stage GrillMe conversation made a real `Task`/`Agent`
call (a genuine background subagent, with a real agent ID and output file) and a
real `ScheduleWakeup` call (which failed its own schema validation — `'prompt' is
required when stop is not true` — rather than being blocked). It's also what
`agentrunner-subagent-support` (task tracker) was already asking for a repro of.

We're closing the gap by also calling `WithTools` alongside every existing
`WithAllowedTools` call, using the same tool list minus MCP-qualified names —
`--tools`' own `--help` example (`"Bash,Edit,Read"`) only names built-in tools,
unlike `--allowed-tools`/`--disallowed-tools`, which the code already treats as
MCP-qualified-name-aware (`mcp__<server>__<tool>`). MCP tool availability is
governed independently by which SDK MCP servers get registered
(`WithSdkMcpServer`), not by `--tools`.

This resolves `agentrunner-subagent-support`'s open "block outright vs. give
subagent spawning first-class tracked support" question in favor of blocking it
outright: a structured, single-turn Requirements/Planning/Review interview has no
legitimate use for spawning a subagent or scheduling a `/loop`-style wakeup, so
there's no reason to build UI/persistence support for something that was never an
intended capability. If a future task wants agentrunner-driven subagents as a
real feature, that's a new, explicit `WithTools`/`WithAgents` opt-in, not a side
effect of an unset flag.

Considered and rejected:

* **Rely on `WithDisallowedTools` to deny `Task`/`ScheduleWakeup` specifically**,
  leaving the rest of the CLI's default tool surface (`WebFetch`, `WebSearch`,
  `NotebookEdit`, `SlashCommand`, `Skill`, ...) untouched. Rejected: it's a
  deny-list that has to be kept in sync with every new built-in/skill tool the CLI
  ships in the future, where `WithTools` is an allow-list that's correct by
  construction — anything not named stays unavailable, matching the "can read
  files, not can modify (or anything else)" boundary directly instead of
  approximating it.
* **Fix only the `Run`/stage-conversation path**, leaving `Execute` alone.
  Rejected: `Execute` has the identical gap (only `WithAllowedTools` set), and
  while it deliberately widens to `Write`/`Edit`/`Bash` already, there's no reason
  an autonomous Implementation-stage run should have `Task`/`Agent` or
  `ScheduleWakeup` available either — the same argument applies.

## Update (2026-08-06): `Task`/`Agent` block reversed in favor of first-class, scoped subagents

The `--tools`-vs-`--allowed-tools` **diagnosis above stands unchanged** — it is
correct and remains the reason `ClaudeRunner` sets `WithTools` alongside
`WithAllowedTools`, and the reason `ScheduleWakeup`/`Skill`/`Workflow` and the
rest of the CLI's default built-in surface stay gated by the `WithTools`
allow-list. What this Update reverses is only the narrower **conclusion** the
section above drew from that diagnosis: that `Task`/`Agent` subagent spawning
should be blocked outright, as an incidental consequence of an unset flag.

That "block outright" call is superseded. `Task` is now re-admitted at both
call sites (`clientFor`/`buildAndConnectClient` for Run, `Execute` for the
Implementation stage), but as **first-class, tool-scoped support** — exactly
the "new, explicit `WithTools`/`WithAgents` opt-in" the original section named
as the acceptable path, not the unset-flag side effect it rightly rejected.
The design:

* **Scoped custom agent, not the built-in presets.** Each call registers a
  single custom `AgentDefinition` via `WithAgents` — `workbench-readonly-agent`
  for Run, `workbench-execution-agent` for Execute — whose `Tools` is exactly
  that stage's own boundary (`readOnlyTools`, `+Bash` for Review, or
  `executionTools`). A spawned subagent's tool access therefore **cannot exceed
  the calling stage's scope**, and that parity is enforced by the subagent's
  own `Tools`, not by reopening the parent's `WithTools`. `Task` is
  deliberately excluded from the subagent's own tools so it can't recurse.
* **Built-in presets stay closed.** The CLI's own agents (`Explore`, `Plan`,
  `general-purpose`) are denied via `WithDisallowedTools`' `Task(<name>)`
  scoped-deny syntax, so a model can't route around the scoped custom agent by
  spawning a preset that would inherit the full default surface — preserving
  the allow-list-by-construction posture the diagnosis argued for.
* **No hidden state; turn-based await preserved.** `SubagentStart`/
  `SubagentStop` hooks let `Run()`/`Execute` synchronously await a spawned
  subagent's completion before the turn returns (bounded by the call's existing
  timeout), and the subagent's **real output — read from
  `AgentTranscriptPath` on `SubagentStop`** — is persisted into `Conversation`
  state as inspectable `ToolActivity`, replacing the CLI's opaque "launched"
  acknowledgment (which is suppressed). This satisfies docs/architectural
  invariants.md's "No hidden state" and keeps a subagent's existence, status,
  and output part of persisted turn state rather than out-of-band.

Live-verified naming caveat: the CLI's arg syntax and its runtime stream use
**different names** for the same tool. `--tools`/`--allowed-tools`/
`--disallowed-tools` (and the `Task(<name>)` scoped-deny) take `"Task"`, but the
live CLI streams the spawn as a `ToolUseBlock`/`tool_call` named `"Agent"`
(observed against `claude` 2.1.206; the vendored SDK pins neither — it's
CLI-runtime-determined). Runtime correlation of a subagent's result back to its
originating call must therefore match the **streamed** name (`"Agent"`), not the
flag name (`"Task"`); conflating the two silently breaks the correlation (the
result surfaces orphaned, keyed by `AgentID` rather than the parent
`tool_use_id`) even though every unit test and the flag-level preset denial
still pass. `claude_runner.go` keeps the two as `subagentToolName` (flags) and
`subagentToolCallName` (stream), matched via `isSubagentToolCall`.

Why reverse: `agentrunner-subagent-support` established that a scoped,
tracked subagent is a genuinely useful capability (delegating a bounded
read-only investigation, or an isolated execution sub-task) once its output is
first-class and its tool access can't escape the stage boundary — which the
mechanisms above now guarantee. The original section's objection was to
*unscoped, untracked* spawning as an accident of an unset flag; that objection
is fully addressed, not overridden. See
`internal/agentrunner/claude_runner.go` (`withSubagentSupport`, `scopedSubagent`,
`subagentTracker`, `readAgentTranscript`).

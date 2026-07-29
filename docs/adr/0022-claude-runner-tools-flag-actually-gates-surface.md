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

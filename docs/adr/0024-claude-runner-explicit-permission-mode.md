# ClaudeRunner sets an explicit permission mode, replacing the CLI's unset default

`internal/agentrunner/claude_runner.go` connected every `claude` CLI session —
`Run` (Requirements/Planning/Review GrillMe chats) and `Execute` — without ever
passing `--permission-mode`, leaving the CLI's own unset default in place. This
ADR replaces that implicit behavior with a single, explicit, inspectable
permission-mode decision (`claudePermissionMode = PermissionModeDefault`) plus
two scoped `WithCanUseTool` callbacks, alongside `readOnlyTools`/`executionTools`.

## What the SDK/CLI documentation actually says

Sourced from `github.com/severity1/claude-agent-sdk-go@v0.6.22`'s own code and
its `examples/11_permission_callback/main.go` header, and confirmed live against
`claude` CLI 2.1.206:

- The permission modes are `default`, `acceptEdits`, `plan`,
  `bypassPermissions` (`internal/shared/options.go`), emitted to the CLI as
  `--permission-mode <value>` (`internal/cli/discovery.go`, only when non-nil).
- The permission decision flow is:
  `PreToolUse Hook -> Deny Rules -> Allow Rules -> Ask Rules -> Permission Mode -> canUseTool Callback`.
- `--allowed-tools` is the **Allow Rules** step: a tool on that list is
  auto-approved and **never reaches the `canUseTool` callback**. A tool that is
  *visible* (via `--tools`) but *absent* from `--allowed-tools` falls through to
  `Permission Mode -> canUseTool Callback`.
- Per the SDK example's own header: read-only tools (Read/Glob/Grep) are
  auto-approved and never trigger callbacks; Write/Edit/Bash trigger the callback
  **only under `PermissionModeDefault`**. `acceptEdits` auto-approves Write/Edit/Bash
  *without* invoking the callback; `bypassPermissions` bypasses all checks.
- `WithCanUseTool` auto-sets a permission-prompt tool so the CLI routes
  permission prompts back through the control protocol to our Go callback
  (`client.go`). Callbacks require the streaming Client API, which this codebase
  already uses.

## The reproduced block, and a live footgun

A throwaway `go run` program (real CLI 2.1.206, SDK v0.6.22) connected a session
with `--tools Read,Write`, `--allowed-tools Read` (Write visible but not
auto-approved) and asked the agent to create a file:

- **CASE A — the block.** Permission mode unset, no callback (today's `Run`/`Execute`
  posture for any visible-but-unapproved tool): the `Write` tool_result was
  `isError=true` — *"Claude requested permissions to write to …repro.txt, but you
  haven't granted it yet."* The model gave up and reported it could not create the
  file. **The specific blocked action is `Write`** (and by the same rule `Edit`).
- **CASE B — the footgun.** `PermissionModeDefault` + the SDK's own
  `NewPermissionResultAllow()`: the callback fired, but the CLI returned
  `tool_result isError=true` — a **ZodError**, *"updatedInput: expected record,
  received undefined"* — and the tool call failed. CLI 2.1.206 requires the allow
  response's `updatedInput` to be a record, but `NewPermissionResultAllow()` omits
  it. The model interpreted this as a harness bug and gave up.
- **CASE C — the fix.** `PermissionModeDefault` + returning
  `PermissionResultAllow{Behavior:"allow", UpdatedInput: input}` (echoing the
  proposed input back unchanged): the callback fired and the write succeeded. This
  is captured in `allowToolUse` and covered by a test; the SDK helper is never
  used for an allow.

## Decision

`PermissionModeDefault`, set explicitly and uniformly on every `Run` and
`Execute` connection. It is the **only** mode that lets us make an inspectable,
per-call decision in our own Go code: it is the sole mode that routes a
visible-but-not-auto-approved tool through `WithCanUseTool`. `acceptEdits` and
`bypassPermissions` skip that callback; `plan` is a read-only planning mode. The
scope is therefore uniform in the *mode value* and differentiated per stage in
*what the callback does and which tools are visible-but-not-approved* — a choice
the repro forces, not one guessed in advance. Setting it explicitly to `default`
is behaviorally identical to the CLI's own unset default wherever `--allowed-tools`
already covers every visible tool (the plain read-only turn), so it changes
nothing there while making the decision explicit.

`bypassPermissions` is deliberately **not** used as an unscoped blanket default:
its blast radius is unbounded, which the task constraints forbid.

Two scoped callbacks build on that mode:

1. **Execute — `executeWriteGuard`.** `Execute` already runs against an isolated
   git worktree, but the CLI does not confine `Write` to cwd — an absolute
   `file_path` can name anywhere on disk (live-verified). So `Write`/`Edit` are
   kept *visible* (`--tools`) but *off* `--allowed-tools`, and the guard approves
   a write only when its resolved `file_path` stays inside the worktree, denying
   and logging (`slog.Warn`) any that would escape. This *tightens* Execute's
   write blast radius to the worktree — bounded by the existing Workspace, never
   widened. Bash stays auto-approved (gating commands by path is infeasible and
   running them is Execute's purpose; the worktree isolation is the bound there).

2. **Run — human escalation (`RunInput.OnPermissionRequest`).** A deliberate new,
   opt-in capability (not a fix for a naturally-occurring block: today `Run`'s
   `--tools` omits Bash entirely, so nothing is silently blocked — the tool is
   simply invisible). A human-paced turn (Requirements/Planning, Review's ordinary
   chat) that supplies `OnPermissionRequest` makes Bash *visible* (`--tools`)
   without *auto-approving* it, so a Bash call reaches the human for an explicit
   allow/deny under `PermissionModeDefault` instead of never being offered.
   Review's unattended automated-checks turn (`EnableBashTool`) keeps Bash
   auto-approved with no human, and free-chat/rehydration callers keep today's
   strictly read-only, Bash-invisible surface. The hook is looked up lazily by
   `SessionKey` (`permissionRequesters`) rather than captured at connect time,
   because a cached `Run` client outlives the turn that created it — the same
   reason `subagentTrackers` is indirected.

## Consequences

- The permission decision is now explicit code next to `readOnlyTools`/
  `executionTools`, inspectable and unit-tested, not an implicit CLI default.
- `NewPermissionResultAllow()` is avoided in favor of `allowToolUse` (the
  `updatedInput` footgun), covered by a test.
- The human-escalation path is wired end-to-end: `RunInput.OnPermissionRequest`
  in the runner, an `escalationRegistry` + `/permission` endpoint (foreign-id
  rejection) in `internal/api`, a `permission_request` SSE event, and an
  Approve/Deny control in `StageConversationPanel`. Backend and frontend units
  cover it (`internal/api/stage_permission_test.go`, `frontend/src/GrillMePanel.test.tsx`).
- Live verification against `claude` CLI 2.1.206: CASE A/B/C above were reproduced
  directly (throwaway `go run`, deleted after use). The escalated command must be
  one the CLI itself flags as needing approval (a mutating command like `rm …`):
  the CLI auto-approves *safe* Bash (e.g. bare `echo`) before the callback, so a
  read-only probe would never reach `OnPermissionRequest`.

## Supersedes

An earlier evaluation deferred the Run human-escalation path as "not built"
(nothing is silently blocked today, since `Run`'s `--tools` omits Bash). This ADR
reverses that: the escalation path is built as a deliberate new capability so a
human-paced turn can run a bounded, in-the-loop command, rather than the tool
being permanently invisible.

## Update: free chat wired into the same escalation path

The "free-chat/rehydration callers keep today's strictly read-only,
Bash-invisible surface" line above is now only true for rehydration. Free chat
(`handleChatCompletions`, `internal/api/chat.go`) now supplies
`RunInput.OnPermissionRequest` via the same `stagePermissionRequestHook` stage
conversations use — `buildAndConnectClient`'s `escalate` check
(`in.OnPermissionRequest != nil`) already generalized over the caller, so no
`claude_runner.go` change was needed, just wiring the hook up on the free-chat
call site plus a session-keyed `/api/v1/chat/permission` decision endpoint
(free chat has no `taskId`/`stage` route to derive the owning session from,
unlike `handleStagePermissionDecision`, so the session key travels in the
decision request body instead) and an Approve/Deny control in `ChatPanel`.

This does widen free chat's blast radius versus a stage conversation: a stage
escalation is bounded to one task's resolved workspace, but free chat's
workspace is `s.ReposRoot` itself (see `handleChatCompletions`'s comment) —
an approved Bash command can run anywhere under the whole sibling-repos
directory, not just one repo. Accepted as the same "human clicks approve"
trust model already extended to Bash generally, not a new mechanism.

## Update: escalatedTools widened to Write, Edit, PowerShell

Bash was the only tool ever admitted through the human-escalation path. It's
now `escalatedTools = []string{"Bash", "Write", "Edit", "PowerShell"}`
(`claude_runner.go`) — every entry is made visible (`WithTools`) without being
auto-approved (`WithAllowedTools`) on a human-escalation turn, the same way
Bash already was. No change was needed to `permissionEscalationCallback`
itself: it was already tool-name-agnostic, forwarding whatever `toolName`/
`input` the CLI proposes to the human requester and echoing the input back via
`allowToolUse` on approval.

Two notes on what this does and doesn't change:

- **PowerShell** was previously excluded from the built-in tool surface
  entirely (`readOnlyTools`'s doc comment called it "redundant with Bash").
  It's now a second command-execution surface a human can approve — not
  auto-run anywhere, same as Bash.
- **Write/Edit get no worktree-boundary guard** the way Execute's
  `executeWriteGuard` confines its own writes. `Run`'s workspace is the
  shared checkout (`ResolveWorkspace`), not an isolated worktree, so an
  approved write lands there directly, visible to any other stage's
  read-only agent browsing the same checkout. This isn't a new gap: Bash
  already had no path confinement on this turn type (an approved `rm` or
  `git commit` was never bounded either) — the human reading the proposed
  `file_path`/content before approving is the same check Bash's command
  string already relied on, not a weaker one.

  **Superseded below** — this gap is now closed for Write/Edit specifically.

## Update: Write/Edit gain the same workspace-boundary backstop as Execute

`permissionEscalationCallback` now takes `workspace string` and, for
`guardedWriteTools` (`Write`/`Edit`), checks the proposed `file_path` against
it via `withinWorkspace` — the same lexical check `executeWriteGuard` already
used for Execute — *before* consulting the human. An out-of-workspace path is
denied automatically, the human is never asked, and this is unconditional: an
empty workspace (Requirements/Planning with no configured repository) denies
every Write/Edit outright rather than treating `""` as "anywhere is fine."
An in-workspace path still requires an explicit human Approve, unchanged.

This doesn't touch Bash/PowerShell — there's no `file_path` to bound a shell
command against, the same reasoning `executeWriteGuard` already applies (Bash
stays auto-approved-but-cwd-confined in Execute, human-approved-but-unbounded
in Run). The asymmetry called out in the previous Update — "Bash never had
path confinement either, so Write/Edit having none wasn't a new gap" — is
still true of Bash, but is no longer used to justify leaving Write/Edit
unguarded: a structural backstop under the human step was cheap (the same
`withinWorkspace` helper, already tested) and removes one class of mistake
(an absolute path escaping the intended workspace) the human approving a
`file_path` might not notice at a glance.

## Update: Review unified into the human-escalation shape; Execute gains PowerShell

Down from three practical access shapes to two:

- **Shape A — unattended, auto, structurally bounded** (Execute only): Bash/
  Write/Edit/PowerShell all auto-run; Write/Edit `executeWriteGuard`-bounded,
  Bash/PowerShell cwd-bounded only. PowerShell added to `executionTools`
  purely for parity with Review's own tool set below — same treatment as
  Bash there (auto-approved, no path guard, gating a shell command's targets
  by path being infeasible).
- **Shape B — attended, human-gated, workspace-bounded** (Requirements/
  Planning/Review/free chat, uniformly): Bash/Write/Edit/PowerShell all
  visible-but-not-auto-approved, routed through the one
  `permissionEscalationCallback`, Write/Edit workspace-bounded.

Review previously auto-approved Bash unconditionally for the whole stage
(`resolveStageRun` set `EnableBashTool: true` stage-wide — a leftover from an
"unattended automated-checks turn" design the code never actually
distinguished from the rest of Review) and never offered Write/Edit/
PowerShell at all. `resolveStageRun` still sets `EnableBashTool: true` for
Review — removing it outright broke Bash for `ChatClientRunner`/`CodexRunner`,
which have no escalation concept whatsoever and treat `EnableBashTool` as
their *only* Bash toggle. Instead, `buildAndConnectClient`'s priority changed:

```go
autoApproveBash := in.EnableBashTool && in.OnPermissionRequest == nil
...
escalate := in.OnPermissionRequest != nil
```

A live `OnPermissionRequest` hook now wins over `EnableBashTool`'s
auto-approval whenever one is present — `EnableBashTool` only auto-approves
Bash for a caller with no human to ask at all. Since every real
stage-conversation entry point (`handleStartStageConversation`,
`handlePostStageMessage`, `handleRegenerateStageMessage`) now sets the hook
uniformly (previously only the ordinary-reply path did — Review's *opening*
turn needs Bash immediately per `reviewSystemPrompt`, so the hook had to be
wired into kickoff/regenerate too, not just replies), Review's ClaudeRunner
turns escalate the full `escalatedTools` bundle exactly like Requirements/
Planning/free chat, with Write/Edit bounded to the execution's isolated
worktree (Review's own workspace) — tighter confinement than Requirements/
Planning's shared checkout ever had. ChatClientRunner/CodexRunner-backed
Review conversations are unaffected: they never set `OnPermissionRequest`, so
`autoApproveBash` still fires for them exactly as before.

Deliberately deferred, tracked as separate tasks rather than bundled in:
session-persisted ("approve once for this session") escalation decisions via
the SDK's `PermissionResultAllow.UpdatedPermissions`/`Destination: "session"`
(mitigates Review's now-real per-call Bash approval cost during its
automated-checks phase), and whether Execute should ever gain its own
`OnPermissionRequest`-equivalent (a product-behavior decision about breaking
Execute's "unattended run to completion" contract, not just plumbing).

# Milestone 5 — Execution

Only now introduce:

* Claude Code
* Codex CLI
* executor abstraction

## Open questions (gathered 2026-07-09, to grill through before Requirements)

Context gathered from `docs/provider abstraction.md`, `docs/project_summary.md`
§7/§8, `docs/task schema v0.md`'s `execution.yaml`, and the existing
`internal/agentrunner` package (built for Requirements/Planning stage
conversations, not execution). None of these are answered yet — this is the
raw list to work through, not a plan.

### Is this a new interface, or does AgentRunner extend?

- `AgentRunner.Run` (`internal/agentrunner/runner.go`) is shaped for one
  conversational turn — a system prompt + user message in, streamed text +
  optional single Draft tool call out. Execution per `project_summary.md`
  §3.4/§7 is `input → executor → output + metrics`, producing
  `execution.yaml` (artifacts, git branch, commits, duration, tokens, cost).
  Is Execution a new `Executor` interface, or does `AgentRunner` grow a
  second method? They likely can't be the same shape — is there any code to
  actually share (workspace resolution, health checks, the claude-CLI
  session plumbing) or is this closer to a parallel package?
- `execution.yaml`'s `executor.type` enum includes `human`. What does the
  system actually do for a human executor — is it just a manual
  status/metrics entry form, with zero automation? Does it belong in this
  milestone at all, or is it schema-only until someone asks for it?

### Claude Code as an *implementation* executor, not a read-only interviewer

- `ClaudeRunner`'s `readOnlyTools` allow-list (`claude_runner.go:23-28`)
  hard-denies Write/Edit/Bash today — a deliberate guardrail for
  Requirements/Planning ("agents can read reference repos but never modify
  them," per prior session notes). Execution is the first place the system
  *wants* an agent to write code, run tests, and commit. Is that a separate
  code path with a different (still-bounded?) allow-list, a config flag on
  the same runner, or a genuinely new runner type?
- Does execution run fully autonomously to completion (all steps, no
  human-in-the-loop between them) and present a diff afterward, or does it
  pause for approval at some granularity? `project_summary.md`'s "Humans
  own intent" invariant governs *what* gets built, not obviously *how much
  autonomy* an executor gets while building it — worth pinning down
  explicitly rather than assuming.
- `claudeRunnerMaxTurns` currently bounds tool round-trips as defense against
  a runaway read-only loop. What's the equivalent bound (turns? wall clock?
  cost cap?) once Bash/Write/Edit are in play and a runaway loop can do real
  damage (delete files, force-push, etc.)?

### Codex CLI

- No Codex integration exists anywhere in the repo today (checked — only a
  forward-reference comment in `main.go:41`). Does it have a subprocess/SDK
  story comparable to `severity1/claude-agent-sdk-go`, or does it need to be
  driven as a raw CLI subprocess with hand-rolled message parsing? Needs
  actual research before estimating, not an assumption of symmetry with
  Claude Code.
- Auth/config model for Codex — same "must be installed and authenticated
  on the machine running the server" constraint as `claude` (see: agent
  execution is fundamentally local-subprocess-bound, not remote-capable)?
  If so, does the executor picker need a per-machine capability check
  independent of the two other in-flight tasks that touch process
  lifecycle (`graceful-shutdown`, `windows-background-service`)?

### Workspace isolation & concurrency

- `ResolveWorkspace` (`runner.go:107`) resolves to a single shared directory
  per repository (`AGENT_REPOS_ROOT/<repo-name>`) — fine for a read-only
  interview, not fine for an executor that's about to `git commit` on that
  checkout. Does each execution attempt get its own branch, its own git
  worktree, or does the whole repo get locked for the duration (blocking a
  second task's execution against the same repo)?
- `execution_id` is meant to be append-only per task (`exec-001`,
  `exec-002`, ...) — does a re-run reuse the same branch/worktree or always
  cut a fresh one? What happens to an execution's branch on failure — left
  for human inspection, or cleaned up?
- Two tasks in the same project executing concurrently against the same
  repo: allowed, serialized, or out of scope for v0 (single global
  execution lock, revisit later)?

### Lifecycle, UX, and failure/metrics

- Stage conversations stream over SSE and are inherently short (one human
  turn at a time). An execution could run for many minutes unattended —
  does it still stream live tool-use/output to a panel, run as a background
  job the UI polls, or both? Does a mid-execution server restart need the
  same rehydration treatment `RunInput.History` gives stage conversations,
  or is a restart during execution just a hard failure to be resumed
  manually?
- `execution.yaml`'s `failure.type` enum (specification/infeasible/
  execution/resource/quality) needs a real classifier somewhere — who
  decides which bucket a given failure falls into, the executor itself via
  prompt discipline, or a wrapper that inspects exit codes/exceptions?
- `metrics.tokens_used`/`cost_estimate` — does the `claude` CLI (via the
  Go SDK) actually expose per-run token/cost figures today? Same question
  for Codex once its integration shape is known. If not exposed, do we
  leave those fields zero for v0?
- Does a successful execution auto-advance `task.yaml`'s `stage` to
  `review` (mirroring Finalize's forward-only auto-advance for
  Requirements/Planning), or is entering Review always a separate human
  action?

### Scope boundary for this milestone

- Milestone 5's one-line scope is "Claude Code, Codex CLI, executor
  abstraction" — does that include any Review-stage UI (viewing/approving
  the diff an execution produced), or is Review still out of scope and this
  milestone stops at "execution ran, `execution.yaml` was written, a branch
  exists"?
- `git-backed-storage` (still `requirements` stage) migrates the
  *workbench's own* task/project YAML storage to git — separate from the
  *target* code repo an execution commits to, but worth confirming that's
  understood as unrelated rather than a hidden dependency.
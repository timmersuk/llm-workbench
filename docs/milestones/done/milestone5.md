# Milestone 5 — Execution

**Status: Done** — Claude Code executor shipped 2026-07-09 (PR #11); Codex
CLI executor and closing decisions on the open questions below landed
2026-07-09.

Introduces:

* Claude Code as an implementation (write-enabled) executor
* Codex CLI as a second implementation executor, at full parity with Claude Code
* the executor abstraction those two share

## What shipped 2026-07-09 (Claude Code, v0)

`AgentRunner` gained a second method, `Execute`, alongside the existing
`Run` (Requirements/Planning conversations) — not a separate `Executor`
interface, since workspace resolution, health checks, and session-locking
turned out to be directly shareable. `ClaudeRunner.Execute` runs the
`claude` CLI with a widened, still-bounded tool allow-list
(Read/Grep/Glob/Write/Edit/Bash) fully autonomously to completion (no
mid-run approval checkpoints — the isolated worktree is the safety
boundary, not human-in-the-loop), bounded by `claudeExecutionMaxTurns`
(100, vs. 30 for read-only conversations). Each execution attempt gets its
own git worktree and branch (`task-exec/<task>/<execution-id>`,
`internal/agentrunner/worktree.go`), never reusing one on re-run and never
auto-cleaning up on failure (left for human inspection). `execution.yaml`
persists real duration/token/cost metrics from the SDK's `ResultMessage`,
with a deterministic failure classifier (deadline/cancel → `resource`,
anything else → `execution`). A successful execution auto-advances
`task.yaml`'s stage to `review`, mirroring Finalize's forward-only
auto-advance. `ExecutePanel` streams live tool activity over SSE. Any
Review-stage diff UI stayed explicitly out of scope.

## What shipped 2026-07-09 (Codex CLI + closing the open questions)

**Codex CLI is a full second executor**, not an Execute-only stopgap:
`CodexRunner` (`internal/agentrunner/codex_runner.go`) implements the same
`AgentRunner` interface via `github.com/hishamkaram/codex-agent-sdk-go`,
which drives `codex app-server` over JSON-RPC — chosen over the simpler
`codex exec --json` subprocess after live testing showed the SDK's MCP
tool-calling support (needed for the Draft-proposal mechanism below) only
works through `app-server` mode.

- **Draft-tool proposals work through Codex, closing the gap this
  milestone almost shipped without.** Codex has no in-process "SDK MCP
  server" helper the way Claude's SDK does — a real external MCP server is
  the only way to offer it a custom tool. `cmd/draftmcp` is that server: a
  small, static, stateless MCP stdio process exposing `propose_context`/
  `propose_plan` (schemas shared with `internal/api/stage_conversation.go`
  via the new `internal/drafttool` package, so both call sites can't drift
  apart). `CodexRunner.ensureRegistered` registers it once per process
  lifetime via `Client.WriteConfigBatch` — verified live that this,
  combined with a persisted per-tool `approval_mode: "approve"`, lets a
  brand-new, never-before-approved tool succeed on its very first
  non-interactive call, with no human approval click required on a fresh
  machine (confirmed with a throwaway server/tool name that had never been
  granted trust before).
- **Executor abstraction question, resolved**: `AgentRunner` (one shared
  interface, five methods) plus a health-checked `map[string]AgentRunner`
  registry (`internal/api/agent_executors.go`) was already sufficient —
  `CodexRunner` slots in as a second implementation with no interface
  changes. `StageConversationPanel` and `ExecutePanel` (previously
  hardcoded to `claude-code`) both now populate their executor pickers
  from the same live-health-checked registry.
- **Real bug found and fixed during end-to-end verification**: a git
  worktree's admin metadata (HEAD, index, refs — what `git commit` writes
  to) always lives under the *original* repo's `.git/worktrees/<id>/`,
  never inside the worktree's own working directory. Codex's
  `workspace-write` sandbox correctly denied writing there
  (`Permission denied` on `index.lock`), since it's outside the sandboxed
  workspace. Fixed via `-c sandbox_workspace_write.writable_roots=[...]`
  (the `app-server`-mode config equivalent of `codex exec`'s `--add-dir`
  flag, which `app-server` silently rejects rather than a `--add-dir` CLI
  arg, which caused a silent 5-minute hang instead of a fast failure).
  Verified with a real commit landing after the fix. This only affects
  `CodexRunner` — `ClaudeRunner`'s Bash tool has no OS-level sandbox layer
  to hit this.
- **Toolchain bumped** to Go 1.26.4 (from 1.24.7) to satisfy
  `codex-agent-sdk-go`'s minimum version, while we were at it.
- **Claude Agent SDK auth model, checked and ruled a non-issue**:
  `severity1/claude-agent-sdk-go` already drives the real `claude` CLI as
  a subprocess (`--print --output-format stream-json`), the same binary a
  human logs into — not a separate API-key-only path. No code change
  needed there.

Verified end-to-end against the real stack (a scratch git repo, not this
project's own history): a real Requirements-stage conversation via Codex
explored the repo, asked a real interview question, and correctly called
`propose_context` with a well-formed proposal after human confirmation; a
real Execute run created `hello.txt`, committed it
(`task-exec/<task>/<execution-id>`), and correctly auto-advanced the task
to `review`.

## Follow-ups (deferred, not blocking this milestone)

* **`human` executor type stays schema-only** — deferred; tracked in
  `docs/milestones/milestone-orphans.md`.
* Review-stage UI (viewing/approving an execution's diff) — **✅ Resolved —
  Milestone 6 PR 3 (#26).**
* `git-backed-storage` (workbench's own task/project YAML storage
  migrating to git) is unrelated to the git repos an execution commits
  to — confirmed not a hidden dependency. Item itself deferred; tracked
  in `docs/milestones/milestone-orphans.md`.
* Two tasks in the same project executing concurrently against the same
  repo: allowed by construction (each execution gets its own worktree),
  not something we had to add explicit serialization for.

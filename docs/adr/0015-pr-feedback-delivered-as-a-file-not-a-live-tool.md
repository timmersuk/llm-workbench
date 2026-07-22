# PR review feedback is pre-fetched to a file, not exposed as a live tool call

`docs/milestones/done/milestone7.md` originally scoped this as "a new GitHub
PR-comment read tool" — a `toolloop.Tool` a reopened Requirements/
Implementation-stage agent could call to pull GitHub's review comments on
demand, mirroring PR 6's `read_file_at_ref`/`list_files_at_ref`. Scoping it
in detail (`/grill-with-docs`, 2026-07-16) surfaced that this doesn't work
across all three executors the way PR 6's tools do: `internal/toolloop`
tools are only ever driven by the `local` executor — `claude-code` and
`codex` gate their *own* built-in tools through entirely separate
mechanisms (an `--allowedTools` allow-list; a coarse sandbox toggle), so a
new `toolloop.Tool` would only ever be usable by `local`, silently leaving
Claude Code/Codex conversations with nothing.

We instead fetch the PR's comments, reviews, and inline code comments once
in Go — `GitHubPRClient.Comments` (`internal/agentrunner/pr.go`), merged
into one normalized, chronologically-sorted YAML document — and write that
to a file the model reads back with the plain `read_file`/`Read` tool every
executor already has at every stage (Claude Code's `readOnlyTools` allow-
list includes `Read`; Codex's `SandboxReadOnly` still permits filesystem
reads; `local`'s `ReadOnlyTools()` has `read_file`). This works identically
across all three executors with zero executor-specific tool wiring, at the
cost of one architectural asymmetry with PR 6: PR 6's ref-aware tools let
the model choose *when* to read, on demand, mid-conversation; this fetches
unconditionally up front, whether or not the model ever looks at the file.

We considered building the live tool anyway, scoped to `local` only (PR 6's
own precedent for an executor-limited capability) and rejected it: unlike
PR 6's ref-aware tools (which read local git objects `local` already has
exclusive practical use for, since GrillMe's Requirements-stage prompt only
ever names a branch, not external state), GitHub PR feedback is exactly the
kind of context a human is likely to expect *any* executor to have when
reopening a rejected/needs-changes task — leaving two of three executors
silently worse off felt like the wrong default to ship.

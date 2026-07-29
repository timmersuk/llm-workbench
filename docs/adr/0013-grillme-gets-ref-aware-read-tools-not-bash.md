# GrillMe gets narrow ref-aware read tools, not `bash`

Milestone 6 PR 5 surfaces a rejected review's execution branch name in the
Requirements-stage (GrillMe) system prompt, but left it inert: GrillMe has no
way to actually inspect that branch's code. PR 6 closes that gap for the
`local` executor by adding two new `internal/toolloop` tools —
`read_file_at_ref(ref, path)` (`git show <ref>:<path>`) and
`list_files_at_ref(ref)` (`git ls-tree -r --name-only <ref>`) — rather than
giving GrillMe the same confined `bashTool` the Review stage already uses.

The two stages look superficially similar (both are read-only, git-aware
conversations), but their workspaces have a different trust shape.
Review's workspace is a disposable per-execution worktree
(`ResolveReviewWorkspace`) — nothing else ever reads or writes it, so `bash`
there can only affect a directory that's about to be discarded either way.
GrillMe's workspace is the *shared* checkout (`ResolveWorkspace`) — the one
directory every Requirements/Planning conversation for the project uses
concurrently, and the one a human might have open too.  `bashTool`'s only
confinement is pinning its working directory (ADR 0010); it does no path
allow-listing or command filtering. Given to the shared checkout, a model
could run `git checkout <branch>`, `git reset --hard`, `rm -rf`, or an
arbitrary network call against state everyone else depends on — not a risk
Review's disposable worktree ever carries.

We chose two new tools, single-purpose like `read_file`/`grep_search`/`glob`,
that shell out to `git` via argv (`exec.Command("git", "show", ref+":"+path)`)
rather than a shell string — there is no injection surface the way
`bashTool`'s `bash -c args.Command` has, because the model never supplies a
command string, only a `ref` and a `path`. Reading a git object doesn't touch
the working tree at all, so it's safe to run concurrently with anything else
happening in the shared checkout, including another conversation's own
`read_file`/`grep_search` calls or a human's active `git status`.

We considered restricting `ref` to only the current task's own execution
branches and rejected it: it's all the same project's own git history, and a
model reading a sibling task's branch isn't a meaningfully different trust
boundary than reading `main`'s history — validating branch-name patterns
would add real complexity for a boundary that isn't protecting anything a
`bash`-style command could actually corrupt.

We considered adding a diff/changed-files view (mirroring Review's
`CollectExecutionPatch`) and rejected it for this PR: the actual ask (PR 5)
was "let a model read the rejected code," not re-derive Review's diff
summary — a real gap remains (a model can't discover a brand-new path the
branch added that `main` doesn't have without already knowing to ask for it),
addressed instead by adding `list_files_at_ref` rather than a full diff.

Scoped to the `local` executor only: `claude-code` and `codex` gate their
*own* built-in tools through entirely different mechanisms (an
`--allowedTools` name allow-list over Claude Code's tools; a coarse sandbox
toggle over Codex's), so extending this to them is a separate design
question, not a natural extension of this change.

**Correction (docs/adr/0022):** the claim above that `--allowedTools` gates
Claude Code's tool surface was wrong — it's a permission auto-approve list,
not a restriction on which tools the model can see/call. The actual gate is
`--tools`, which `internal/agentrunner/claude_runner.go` didn't set at all
until docs/adr/0022's fix.

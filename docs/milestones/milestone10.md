# Milestone 10 — Human-Judgment Verification Needs a Live Instance of the Execution, Not `main`

**Status:** Identified 2026-07-24, while reviewing execution `exec-003`
(task `structured-ask-question-tool`). Not yet scoped — needs a
`/grill-with-docs` session before implementation; this doc names the gap
and some candidate shapes, not a resolved design.

## Why now

Reviewing `exec-003`, the reviewing agent reached its `human_judgment`
verification-step phase (`docs/milestones/done/milestone6.md`'s Review
mechanism, phase 3) and asked:

> Could you run through it (e.g. start a Requirements-stage GrillMe session
> and get it to ask a question with options) and confirm: clickable chips
> render, the recommendation is visually highlighted, free text stays
> available and isn't auto-submitted...

This request was unactionable as written. The only running instance of
this app — then and now — is built from `main`, at whatever ports the dev
launch configs (`.claude/launch.json`, `.vscode/launch.json`) point at.
`exec-003`'s `ask_question` tool exists only on
`task-exec/structured-ask-question-tool/exec-003`, an isolated worktree
`ResolveExecutionWorkspace` created and left in place — nothing serves
*that* code to a browser. Confirmed directly: grepping `internal/` on
`main` for `ask_question`/`AskQuestionName` today returns nothing, and the
task itself is still `status: draft, stage: review` — per this project's
own workflow model, unmerged and not live anywhere `main`-built.

`agent_executable` steps don't have this problem — Review's `bash` is
already confined to the execution's own worktree (`milestone6.md`'s PR 2),
so the reviewing agent can build/test/curl *that* code directly. The gap is
specific to steps that need a human's eyes on a rendered UI.

The only way to actually honor the request above today is a manual,
undocumented workaround: `cd` into the worktree, build the frontend, build
and run the Go binary on a free port, and point a browser there by hand
(done once, live, during this same review — see the session transcript).
Nothing in `docs/milestones/done/milestone6.md`, `ReviewPanel.tsx`,
`CONTEXT.md`'s **Review** entry, or the reviewing agent's own system prompt
(`reviewSystemPrompt`, `internal/api/stage_conversation.go`) mentions this
step, tells the human how to do it, or automates any part of it.

## What already exists, and what doesn't

* Execution leaves the worktree/branch in place after a run
  (`internal/agentrunner/worktree.go`'s `ResolveExecutionWorkspace` doc
  comment) — the code is always there to run.
* Review's `bash` tool already runs arbitrary commands confined to that
  same worktree (`ResolveReviewWorkspace`) — starting a server from it is
  technically no different from running `go test`.
* Nothing picks a port, starts a process, keeps it alive for the
  conversation's duration, tears it down afterward, or surfaces a link to
  the human. `ReviewPanel.tsx` only ever shows the diff/patch and the
  Conversation — never a way to run what the diff produces.
* `docs/milestones/milestone-orphans.md`'s "decision-only tasks" entry
  (`native-desktop-window`) is adjacent but distinct: that's about tasks
  with no code diff at all. This is about tasks that *do* produce a running
  app, where nothing lets a human actually run it during Review.

## Candidate shapes (not decided — for the grilling session)

* **Automate a preview.** `ReviewPanel` gains a "Run this execution" action
  that starts the worktree's frontend+backend on auto-assigned ports (the
  same way `.claude/launch.json`'s configs do today, just execution-scoped)
  and surfaces a link, torn down when the review conversation ends or the
  task leaves `stage: review`.
* **Document the manual steps instead of automating them.** Cheapest
  option: `reviewSystemPrompt` includes the worktree path and exact
  build/run commands whenever a `human_judgment` step exists, so the human
  isn't left to reverse-engineer them the way this session did. No new
  infrastructure, no process-lifecycle management to get wrong.
* **Something in between:** a documented `make preview` (or similar)
  target parameterized by worktree path, run by hand but at least a single
  known command instead of ad hoc `cd`/build/run steps.

Open questions a grilling session would need to resolve: which shape is
worth the build cost given how often `human_judgment` steps are actually
UI-facing vs. conceptual; port allocation/collision with the main dev
servers; process lifecycle and cleanup; whether this only matters for
`claude-code`/local dev today or needs to hold up in a hosted/CI context
too.

## Out of scope (for this doc)

* Resolving which candidate shape to build — that's the grilling session.
* Merge automation (`docs/milestones/done/milestone7.md`, already shipped)
  — this is about previewing *before* a merge decision, not the merge
  itself.

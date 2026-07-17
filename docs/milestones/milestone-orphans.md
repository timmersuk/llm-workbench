# Milestone Orphans — swept-up follow-ups and unscoped gaps

**Status: Tracking doc, not a build plan.** Not a numbered milestone —
nothing here is scoped into PRs, and nothing here is claimed to be blocking
any other work. This is a single place to land every "deferred, not
blocking this milestone" item and every silently-dropped gap found across
the project's closed-out milestone docs, so they stop being scattered
across `docs/milestones/done/*.md` (and, in one case, not tracked
anywhere at all). Each entry below replaces a mention that used to live
only in its origin milestone doc; that doc now points here instead.

Swept in on 2026-07-17, prompted by finding that Milestone 7 itself closed
out with two open sections (its own "Follow-ups", and two items nested in
its "Out of scope") that had no future milestone or backlog task attached —
the same pattern an audit then found repeated in Milestones 2, 3, 4, and 5.

Deliberately excluded: **Milestone 6's knowledge-base promotion item**.
Unlike everything below, it isn't a silent drop — M6 deferred it to M7,
and M7's own scoping explicitly re-examined and re-deferred it again,
on the record, pending its own dedicated `/grill-with-docs` session. It's
open, but continuously and visibly tracked, so it stays where it is
rather than being swept in here.

---

## From Milestone 2 — Git-backed task/project storage

Tasks and projects persist as plain files under `data/` (`WORKSPACE_ROOT`),
read/written directly via `os.ReadFile`/`os.WriteFile` — no version
history, no `.git/` of their own. Milestone 2 deliberately walked back an
earlier "git-backed" framing (`docs/project_summary.md`, `CLAUDE.md`) to
this simpler flat-file `FileStore`, deferring the git-backed version.

Already has a backlog stub:
`data/projects/llm-workbench/tasks/git-backed-storage/task.yaml`
(`status: draft`, `stage: requirements`, created 2026-07-06). Never
scheduled into any milestone or PR since. Confirmed still true in code —
`internal/task/store.go` and its siblings (`execution.go`, `review.go`,
`context.go`, `conversation.go`, `plan.go`) are all plain-file I/O; no git
library anywhere in the repo.

## From Milestone 3 — Chat streams don't survive a refresh

Milestone 3's streaming design relays a completion live over a single HTTP
response per browser tab; nothing about an in-progress generation is held
anywhere once that response is written. A refresh, dropped connection, or
navigation away mid-stream loses the in-progress response with no way to
reconnect and resume or replay it.

Already has a backlog stub:
`data/projects/llm-workbench/tasks/resumable-chat-streams/task.yaml`
(`status: draft`, `stage: requirements`, created 2026-07-06). Its own
`assumptions` explicitly asked to be "revisited once [Milestone 4's]
shape is known" — Milestone 4 shipped `conversation-{stage}.yaml`
persistence, but that only durably stores finished Draft-tool turns, not
in-progress streaming chunks, and nobody revisited the question afterward.
Still open: does Milestone 4's persistence solve most of this for free, or
is a dedicated in-memory resume mechanism still needed?

## From Milestone 4 — Structured `ask_question` tool

A structured `ask_question` tool (options + a recommendation as a
first-class object, rendered as clickable choices) instead of the current
prompt-only interview discipline (one question per turn, a recommended
answer written into the prompt text, no structured UI affordance).

Named once, in Milestone 4's own "Follow-ups" section, and never mentioned
again anywhere in the repo — no backlog task stub was ever created for it,
unlike every other item on this page. This is the one entry here that had
no tracking at all before today.

## From Milestone 5 — `human` executor type stays schema-only

`execution.yaml`'s `executor.type` enum (`docs/task schema v0.md`) lists
`human` alongside `claude-code`/`codex`, but nothing implements it — no
UI, no API handler, no schema validation beyond the enum entry itself.
Deliberate, not an oversight: Milestone 5 chose not to build speculative
support ahead of a real use case.

Already has a backlog stub:
`data/projects/llm-workbench/tasks/human-executor/task.yaml`
(`status: draft`, `stage: requirements`, created 2026-07-09). Never
scheduled into any milestone or PR since.

## From Milestone 7 — Testing-practices re-review

Surfaced while scoping PR 3's test strategy: PR 3 followed existing
precedent (`handleReviewDiff`'s handler test exercising real git rather
than a narrower mocked boundary) deliberately, but "that's what the
codebase already does" was flagged as too weak a bar on its own — the same
kind of gap a second-pass review (the Opus pass that caught PR 2's real
mechanism gaps during Milestone 7's initial scoping) is meant to catch
before an unexamined pattern hardens into unquestioned convention. No
backlog stub existed for this before today; needs its own dedicated look
at this project's test-layering habits.

## From Milestone 7 — `FinalizeReview`/`RecordExecution` coupling

`FinalizeReview`'s "no new execution can have been recorded since Review"
comment (`internal/task/lifecycle.go:127-132`) holds today only because
`RecordExecution` separately guards success writes to `StageImplementation`
(`internal/task/execution.go:180-198`) — true by construction across two
functions, not verified by either one. No bug today; worth a second look
if either function changes.

## From Milestone 7 — No repo auto-clone

The system presumes a project's repository is already cloned at
`AGENT_REPOS_ROOT` — `ResolveWorkspace`
(`internal/agentrunner/runner.go:227-233`) errors if the local directory
doesn't exist rather than cloning it. Whether the fix is a `remote` field
on `Project`, or only a one-time "clone if absent" bootstrap, is still
open.

## From Milestone 7 — No staleness check on the shared checkout

Nothing checks that the shared checkout is on `main`/up to date before a
GrillMe or Planning conversation runs against it. A task built against a
stale or wrong-branch checkout is guesswork as to its validity against
`main`. Needs at minimum a warning surfaced; exact mechanism undecided.

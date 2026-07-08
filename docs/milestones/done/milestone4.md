# Milestone 4 — Planning

**Status: Done** — core shipped 2026-07-07 (GrillMe, Planning Mode, plan
artifact, task-attached conversations); extended and closed out 2026-07-08.

Now you can introduce:

* GrillMe
* planning mode
* plan artifact
* conversation attached to task (moved from `docs/milestones/milestone3.md`
  — a persisted, task-scoped conversation only earns its keep once GrillMe
  can synthesize it into `context.yaml`/`plan.yaml`)

Now you're finally using AI to add value.

## What shipped 2026-07-07

`GrillMePanel`/`PlanningModePanel` over a shared `StageConversationPanel`,
backed by per-stage persisted `conversation-{stage}.yaml`
(`internal/task/conversation.go`), the `propose_context`/`propose_plan`
Draft-tool mechanism, and the Finalize/Revise lifecycle into
`context.yaml`/`plan.yaml`.

## What this extension added 2026-07-08

* **Interview discipline** (`internal/api/stage_conversation.go`'s
  `grillMeSystemPrompt`/`planningModeSystemPrompt`): one question per turn,
  every question paired with a recommended answer and why, decisions
  resolved in dependency order, no proposal until the human has confirmed
  shared understanding.
* **Codebase-grounded grilling**: the prompt instructs the agent to explore
  the repository first and answer its own questions from code before asking
  the human; the frontend now preselects the `claude-code` executor when
  it's healthy. A repo-less project no longer hard-fails a stage
  conversation — `local` proceeds with an empty workspace, `claude-code`
  reports a clear, actionable error instead of a generic one.
* **Session rehydration**: `agentrunner.RunInput.History` carries the
  persisted conversation into a fresh `AgentRunner` session (seeded for
  `ChatClientRunner`, prepended as a transcript for a new `ClaudeRunner`
  client) — a server restart no longer wipes the agent's memory of an
  in-progress interview, verified live by restarting the server mid-session
  and confirming recall.
* **Draft iteration loop**: a reload/restart now rehydrates the most
  recently proposed draft from the persisted transcript instead of losing
  it, and a new "Request changes" action sends the human's edited draft
  back to the model as a single message (comment + fenced JSON), so the
  model's revision starts from what the human actually changed.

Verified against the real stack: real local LLM backend, real `claude` CLI,
a live server restart mid-conversation. See the branch history for the
supporting unit/integration test additions across
`internal/agentrunner`, `internal/api`, `internal/chat`, and the frontend.

## Follow-ups (deferred, not blocking this milestone)

* A structured `ask_question` tool (options + recommendation as a
  first-class object, rendered as clickable choices) instead of prompt-only
  discipline — the conversation schema is additive-friendly, no migration
  needed when this lands.
* `chatclient-tool-loop` (`data/projects/llm-workbench/tasks/chatclient-tool-loop/`):
  give the local/`ChatClientRunner` path its own Read/Grep/Glob tool loop,
  so codebase-grounded grilling isn't claude-code-only.

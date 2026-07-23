# LLM Workbench

A workflow control plane that coordinates humans, LLMs, and coding tools around versioned Tasks. See `docs/vision.md` and `docs/project_summary.md` for the broader pitch; this file is the glossary of terms that don't already have a crisp definition there.

## Language

**GrillMe**:
The requirements-stage interview: an AI-led conversation, scoped to one Task, that interrogates the Task's terse `task.yaml` fields until they're coherent enough to draft `context.yaml`. Distinct from **Planning Mode**, which is the same underlying mechanism (conversation → Draft → Finalize) applied one stage later, targeting `plan.yaml` instead.
_Avoid_: "the interview" (ambiguous — say GrillMe or Planning Mode)

**Planning Mode**:
The planning-stage interview, structurally identical to GrillMe (conversation → Draft → Finalize) but scoped to synthesizing `plan.yaml` from an already-finalized `context.yaml`.

**Draft**:
A structured proposal produced when the LLM calls a dedicated tool (e.g. `propose_context`, `propose_plan`) mid-conversation, editable by the human before **Finalize**. Not yet persisted — it's discarded if rejected, and the Conversation continues. The LLM proposing a Draft is how the "I think I have enough" readiness signal and the synthesis step collapse into a single event, rather than being two separate mechanisms. A Draft's schema covers whichever fields the current stage is responsible for: during GrillMe, that's `task.yaml`'s `objective`/`constraints`/`assumptions`/`success_criteria` *and* `context.yaml`'s fields together (GrillMe is the primary way these `task.yaml` fields get set, not just context.yaml); during Planning Mode, that's `plan.yaml`'s fields only.

**Finalize**:
The human action of accepting a Draft (after optionally editing it), which writes it to disk — during GrillMe, both the relevant `task.yaml` fields and `context.yaml`; during Planning Mode, `plan.yaml`; during Review, a `reviews/review-NNN.yaml` entry. Only a human can Finalize — the LLM proposing a Draft is never sufficient on its own (see "Humans own intent" in `docs/architectural invariants.md`). For GrillMe and Planning Mode, Finalize always advances `task.yaml`'s `stage` forward one step (context.yaml finalized → `stage: planning`; plan.yaml finalized → `stage: implementation`), since there's exactly one possible next stage. Review is the one place Finalize can move `stage` in either direction, because its Draft encodes a three-way decision (`approved | rejected | needs_changes`) about which direction is correct — see **Review**. This is still a human decision recorded via Finalize, not a **Revise**: Revise is reserved for a human reopening a stage's Conversation directly, without going through that stage's own Draft/decision mechanism.
_Avoid_: "approve" (reserve for `review.yaml`'s `decision` field — see **Review**)

**Revise**:
The explicit, human-triggered action of moving a Task's `stage` backward (e.g. "Revise Plan" from implementation/review back to planning; "Revise Requirements" from planning back to requirements) to reopen an earlier stage's Conversation, without going through that stage's own Draft/Finalize decision. Distinct from Finalize's `needs_changes`/`rejected` outcomes (see **Finalize**), which also move `stage` backward but do so as the direct result of a Review Draft's decision, not a standalone override. Revisiting a stage resumes its existing persistent Conversation (see below) rather than starting a new one — the LLM retains full context from every prior visit to that stage.

**Review**:
The review-stage interview (Milestone 6): structurally the same Conversation → Draft → Finalize mechanism as GrillMe/Planning Mode (`propose_review`), but its Draft is a three-way decision (`approved | rejected | needs_changes`) about a *prior* artifact — the execution's diff — rather than a proposal for a new one. `approved` advances `stage` to `pr_review` (Milestone 7 PR 2 — a human then pushes the branch and opens a GitHub PR, eventually reaching `merged`); `needs_changes` moves it back to `implementation` (a new execution attempt, primed with the review's notes); `rejected` moves it back to `requirements` (reusing **Revise**'s existing mechanism, with the review's notes surfaced into the reopened conversation). `needs_changes`/`rejected` are also valid from `pr_review` itself, reusing this same mechanism. Verdicts are recorded append-only, one `reviews/review-NNN.yaml` per cycle, mirroring `executions/exec-NNN.yaml`. `pr_review` has a route/UI (`PRReviewPanel`, wired into `TaskDetailPanel`) shipped in Milestone 7 PR 3 (`docs/milestones/done/milestone7.md`).

**Verification step**:
An entry in `context.yaml`'s `verification: []` list, classified `agent_executable` (the executing agent attempts it directly — e.g. hitting an endpoint, running a command, driving a UI check — and reports what it observed) or `human_judgment` (the human performs it themselves; the agent only records their confirmation). Authored during GrillMe, consumed during **Review**'s per-step confirmation phase.

**Conversation**:
A persisted, append-only message history attached to a Task, scoped to a single stage (one Conversation for GrillMe/requirements, a separate one for Planning Mode/planning) rather than one continuous history for the Task's whole lifetime. A Conversation is reopenable: revisiting its stage (see **Revise**) resumes appending to the same Conversation rather than starting a fresh one.

**Tool Activity**:
The ordered list of read-only (or, for Review, Bash) tool calls and results an agent made while producing one Conversation turn — distinct from a **Draft**, which is the single proposal-ending tool call a turn may end with. Persisted on that turn's assistant message (capped in size), not a separate Conversation entry, and rendered collapsed as a single per-turn summary ("Used N tools") that expands to each call paired with its result. Never itself a Draft and never Finalized — it's a record of what the agent looked at on the way to its answer, not a proposal a human accepts.

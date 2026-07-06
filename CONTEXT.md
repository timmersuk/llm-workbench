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
The human action of accepting a Draft (after optionally editing it), which writes it to disk — during GrillMe, both the relevant `task.yaml` fields and `context.yaml`; during Planning Mode, `plan.yaml` — and auto-advances `task.yaml`'s `stage` forward one step (context.yaml finalized → `stage: planning`; plan.yaml finalized → `stage: implementation`). Only a human can Finalize — the LLM proposing a Draft is never sufficient on its own (see "Humans own intent" in `docs/architectural invariants.md`). Finalize never moves `stage` backward — see **Revise**.
_Avoid_: "approve" (reserve for `review.yaml`'s human review decision, a separate later concept)

**Revise**:
The explicit, human-triggered action of moving a Task's `stage` backward (e.g. "Revise Plan" from implementation/review back to planning; "Revise Requirements" from planning back to requirements) to reopen an earlier stage's Conversation. Distinct from Finalize, which only ever moves forward. Revisiting a stage resumes its existing persistent Conversation (see below) rather than starting a new one — the LLM retains full context from every prior visit to that stage.

**Conversation**:
A persisted, append-only message history attached to a Task, scoped to a single stage (one Conversation for GrillMe/requirements, a separate one for Planning Mode/planning) rather than one continuous history for the Task's whole lifetime. A Conversation is reopenable: revisiting its stage (see **Revise**) resumes appending to the same Conversation rather than starting a fresh one.

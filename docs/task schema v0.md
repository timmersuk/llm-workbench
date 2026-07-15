# Task Schema v0

A Task is a versioned intent object.

It represents a unit of work that moves through a structured workflow lifecycle.

A task belongs to exactly one project permanently: it is stored nested
inside that project's own directory and can never move to another project.

---

## 1. File Structure

Each task is a directory under its owning project's `tasks/` directory
(`WORKSPACE_ROOT` defaults to `data/`, so this repo's own tasks live at
`data/projects/<projectId>/tasks/`):

```
data/projects/llm-workbench/tasks/fix-login-bug/
    task.yaml
    context.yaml (optional, derived — see below)
    plan.yaml (optional, generated)
    executions/exec-NNN.yaml (append-only, one per execution attempt)
    reviews/review-NNN.yaml (optional, append-only, one per review cycle)
```

Only `task.yaml` is required at creation. A task's id is client-specified at
creation time and unique only within its owning project — the same id may
be reused by tasks in different projects.

---

## 2. Core Task Definition (task.yaml)

```yaml
id: fix-login-bug
title: ""
project: auth-service

status: draft  # draft | ready | in_progress | blocked | failed | complete

stage: requirements  # requirements | planning | implementation | review | complete

created_at: 2026-07-05T00:00:00Z
updated_at: 2026-07-05T00:00:00Z

objective: ""

constraints: []
assumptions: []
success_criteria: []

references:
  knowledge: []
  repo: []
```

---

## 3. Optional Derived Artifacts

### context.yaml

Derived context: the narrative detail behind a task that doesn't fit
`task.yaml`'s terse fields or `plan.yaml`'s short structured lists — e.g.
the depth a planning conversation would normally produce (rationale, file
references, alternatives considered, verification steps).

```yaml
summary: ""
background: ""
files: []
detail: |
  ""
verification:
  - description: ""
    kind: agent_executable | human_judgment
open_questions: []
```

Each `verification` entry is a structured step, not a bare string: a
human-readable `description` plus a `kind` classifying who performs it —
`agent_executable` (the reviewing agent attempts it directly — hit an
endpoint, run a command, drive a UI check — and reports what it observed) or
`human_judgment` (the human performs it themselves; the agent only records
their confirmation). Authored during GrillMe, consumed during Review's
per-step confirmation phase. See
`docs/adr/0008-structure-context-verification-entries.md` for why this field
was widened from `[]string`.

---

### plan.yaml

Generated structured plan for execution.

```yaml
approach: ""
steps: []
risks: []
estimated_complexity: low | medium | high
recommended_executor: ""
```

---

### execution.yaml

One per execution attempt.

```yaml
execution_id: exec-001
task_id: fix-login-bug

executor:
  type: claude-code | codex | local | human
  version: ""

input:
  plan_ref: ""
  context_refs: []
  review_feedback: ""  # prior review's notes, set only when this attempt
                        # was triggered by a needs_changes verdict

output:
  artifacts: []
  git_branch: ""
  commits: []

metrics:
  duration_seconds: 0
  tokens_used: 0
  cost_estimate: 0

status: success | failure | partial

failure:
  type: specification | infeasible | execution | resource | quality
  message: ""
```

---

### reviews/review-NNN.yaml (optional, append-only)

Review verdicts are stored one file per cycle under `reviews/`, append-only
and never overwritten — the same shape as `executions/exec-NNN.yaml` (§5.2). A
task that cycles review → implementation (`needs_changes`) → review again
records a fresh `review-NNN.yaml` each time, so the full history of every
verdict is a first-class queryable fact.

```yaml
review_id: review-001
task_id: fix-login-bug
decision: approved | rejected | needs_changes
notes: ""
created_at: 2026-07-09T00:00:00Z
```

`decision` drives the stage transition on Finalize: `approved` → `complete`,
`needs_changes` → `implementation` (a fresh execution attempt), `rejected` →
`requirements` (reopening GrillMe).

---

## 4. State Transitions (Logical Model)

```
draft
  ↓
ready
  ↓
in_progress
  ↓
review
  ↓
complete
```

Failure can occur at any stage and must produce a structured `execution.yaml`.

---

## 5. Key Constraints

### 5.1 No hidden state
All meaningful transitions must be recorded in files.

### 5.2 Execution is append-only
Executions are never overwritten, only added.

### 5.3 Tasks are immutable in identity
`id` never changes.

### 5.4 Derived artifacts are optional
Plans and executions are generated, not required at creation.

---

## 6. Relationship to Projects

Each task must belong to exactly one project:

```yaml
project: auth-service
```

This is structurally enforced, not just a matter of the `project:` field:
a task's directory lives nested inside its owning project's directory
(`data/projects/<projectId>/tasks/<taskId>/`), and the API only ever
exposes task routes nested under a project (`/api/v1/projects/{projectId}/tasks/...`)
— there is no route or field through which a task could move to a
different project.

Projects provide:
- context
- constraints
- knowledge links
- repository mapping

Tasks do not define global behaviour.

---

## 7. Design Intent

This schema is intentionally minimal to:

- enable early implementation
- avoid over-specification of workflow
- allow evolution of stages and policies
- support Git-native versioning
- preserve inspectability and reproducibility

---

## 8. One-Line Definition

> A Task is a versioned intent object that moves through explicit, inspectable stages, with all executions recorded as structured, append-only transformations.
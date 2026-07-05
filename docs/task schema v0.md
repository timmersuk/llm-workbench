# Task Schema v0

A Task is a versioned intent object stored in a git-backed task repository.

It represents a unit of work that moves through a structured workflow lifecycle.

---

## 1. File Structure

Each task is a directory:

```
tasks/TASK-0001/
    task.yaml
    context.yaml (optional, derived)
    plan.yaml (optional, generated)
    execution.yaml (one per execution attempt)
    review.yaml (optional, human or system generated)
```

Only `task.yaml` is required at creation.

---

## 2. Core Task Definition (task.yaml)

```yaml
id: TASK-0001
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
task_id: TASK-0001

executor:
  type: claude-code | codex | local | human
  version: ""

input:
  plan_ref: ""
  context_refs: []

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

### review.yaml (optional)

```yaml
decision: approved | rejected | needs_changes
notes: ""
```

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
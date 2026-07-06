# LLM Workbench — Project Summary

> A workflow control plane for coordinating humans, LLMs, and coding tools through explicit, inspectable processes.

---

## 1. Overview

LLM Workbench is a system for managing software engineering work as a set of structured, versioned workflows.

It provides:

- task lifecycle management
- explicit planning and execution stages
- pluggable LLM/tool execution
- inspectable decision and workflow history
- separation of intent, knowledge, and execution

It is not a chat app or agent framework. It is a **workflow control system**.

---

## 2. Core Idea

Software development is treated as a structured flow of:

```
Intent → Planning → Execution → Review → Completion
```

Each step is explicit, inspectable, and traceable.

LLMs and tools are treated as **replaceable workers**, not system controllers.

---

## 3. Key Abstractions

### 3.1 Task (Primary Unit of Work)

A Task is:

- a versioned intent object
- belonging to exactly one project permanently, stored nested under it
- progressed through structured workflow stages
- linked to code changes and execution outcomes

Tasks are the main unit of coordination in the system.

---

### 3.2 Project (Context Boundary)

A Project is a **stable grouping and context scope** for tasks.

It defines:

- shared domain context
- associated code repositories
- reusable constraints and conventions
- linked knowledge sources

Projects are NOT workflow objects.

They do NOT have lifecycle states.

They exist to provide **contextual boundaries for tasks**.

Example:

```yaml
project:
  id: auth-service
  repositories:
    - github.com/org/auth-service
  knowledge:
    - authentication.md
    - security-practices.md
  constraints:
    - no breaking API changes
```

Tasks reference a project:

```yaml
task:
  id: TASK-0012
  project: auth-service
```

---

### 3.3 Knowledge (Long-Lived Context)

Knowledge represents durable, reusable information:

- coding standards
- architecture decisions
- system design notes
- domain knowledge
- operational practices

Knowledge is separate from tasks and projects but may be referenced by both.

---

### 3.4 Execution (Work Performed by Workers)

Execution is a single run of an external or human/LLM worker.

It produces:

- output artifacts (code changes, commits, branches)
- metrics (time, tokens, cost)
- status (success/failure)
- optional summaries

Executions are treated as **opaque transformations**:

```
input → executor → output + metrics
```

---

### 3.5 Workflow Engine

The workflow engine coordinates:

- task state transitions
- stage enforcement
- policy decisions
- executor selection
- failure handling and recovery

It is fully deterministic and inspectable.

---

## 4. Project Structure

A workspace consists of:

```
Workspace
├── Projects
│   ├── auth-service
│   │   └── Tasks
│   │       ├── fix-login-bug/
│   │       ├── add-mfa-support/
│   │       └── ...
│   ├── billing-system
│   │   └── Tasks
│   │       └── ...
│   └── ...
│
├── Knowledge Base
│   ├── coding-standards.md
│   ├── architecture.md
│   └── domain/
│
└── Code Repositories
    ├── service-a
    ├── service-b
    └── ...
```

A task belongs to exactly one project permanently — it is stored nested
under that project and can never move to another.

---

## 5. Workflow Model

Tasks progress through structured stages:

```
Requirements
    ↓
Architecture
    ↓
Planning
    ↓
Implementation
    ↓
Review
    ↓
Completion
```

Each stage:
- produces explicit artifacts
- can be revisited
- can fail and trigger recovery flows

---

## 6. Failure Model

Failures are first-class outcomes.

Types include:

- specification failure (unclear intent)
- infeasible task (cannot be completed as defined)
- execution failure (tool/model failure)
- resource failure (timeouts, token limits, cost caps)
- quality failure (review rejection)

Each failure includes:

- classification
- metrics
- partial outputs (if any)
- recovery strategy

---

## 7. Executors

Executors are interchangeable workers:

- Claude Code
- Codex CLI
- local LLMs
- external automation tools
- human developers

They are treated as **black-box functions with observable outputs**.

---

## 8. Context Construction

Each execution receives an explicit context package:

- task definition
- project context
- relevant knowledge
- repository state
- constraints

No implicit or hidden context is allowed.

---

## 9. Design Principles

### 9.1 No Hidden State

The system must never rely on information that cannot be inspected.

All decisions, transitions, and inputs must be visible.

---

### 9.2 Separation of Concerns

| Concept | Responsibility |
|----------|----------------|
| Knowledge | Long-lived domain information |
| Project | Context boundary for tasks |
| Task | Unit of intent |
| Execution | Unit of work |
| Workflow | Coordination logic |

---

### 9.3 Providers Are Replaceable

The system depends on stable interfaces, not specific implementations.
Executors (§7) are the most fully-realized instance today; knowledge
stores and LLM/chat APIs follow the same pattern. See
`docs/provider abstraction.md`.

---

### 9.4 Tasks Are First-Class

Tasks are the primary unit of coordination, not chat sessions or prompts.

---

### 9.5 Prefer Explicit Over Implicit

All meaningful system behaviour must be:
- explicit
- traceable
- inspectable

---

## 10. One-Line Summary

> An inspectable workflow control system for coordinating software engineering tasks across humans and interchangeable LLM/tool executors.
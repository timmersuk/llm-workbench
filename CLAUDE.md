# CLAUDE.md

This file provides guidance to Claude Code (claude.ai/code) when working with code in this repository.

## High-Level Code Architecture and Structure

The LLM Workbench is a system designed for managing software engineering work through structured, versioned workflows. It acts as a workflow control plane that coordinates humans, LLMs, and various coding tools.

### Core Abstractions:

*   **Task**: The primary unit of work. Tasks are versioned intent objects, stored in a git-backed repository (`data/tasks/`). Each task is a directory (e.g., `data/tasks/TASK-0001/`) containing:
    *   `task.yaml`: Core definition (id, title, project, status, stage, objective, constraints, etc.).
    *   `context.yaml` (optional): Derived context — see `docs/task schema v0.md` for its shape.
    *   `plan.yaml` (optional): Generated structured plan.
    *   `execution.yaml`: One per execution attempt, detailing executor, input, output, metrics, and status.
    *   `review.yaml` (optional): Human or system-generated review.
*   **Project**: A stable grouping and context scope for tasks. Projects define shared domain context, associated code repositories, reusable constraints, and linked knowledge sources. They are NOT workflow objects themselves. Project definitions are expected to be found in the `data/projects/` directory.
*   **Knowledge**: Durable, reusable information such as coding standards, architecture decisions, system design notes, and domain knowledge. Knowledge is separate from tasks and projects but can be referenced by both. The `data/knowledge/` directory is intended for these files, stored as an [Open Knowledge Format (OKF)](https://github.com/GoogleCloudPlatform/knowledge-catalog/blob/main/okf/SPEC.md) bundle — markdown concept documents with YAML frontmatter, maintained as a compounding "LLM wiki" rather than re-derived from raw sources each time — see `docs/knowledge schema v0.md`.
*   **Execution**: A single run of an external or human/LLM worker, treated as an opaque transformation (`input → executor → output + metrics`).
*   **Workflow Engine**: Coordinates task state transitions, stage enforcement, policy decisions, executor selection, and failure handling. It is fully deterministic and inspectable.

### Project Structure:

A typical workspace within LLM Workbench is structured as follows, rooted at
`WORKSPACE_ROOT` (defaults to `data/` — see `docs/engineering
conventions.md`'s Configuration section):

```
data/                          (WORKSPACE_ROOT)
├── tasks/                     (git-backed Task Repository)
│   ├── TASK-0001/
│   └── ...
├── projects/
│   ├── auth-service/
│   └── ...
└── knowledge/
    ├── coding-standards.md
    └── ...
```

Code Repositories are external, referenced by projects.

### Workflow Model:

Tasks progress through explicit stages: `Requirements → Architecture → Planning → Implementation → Review → Completion`. Each stage produces artifacts and can be revisited or trigger recovery flows upon failure.

### Design Principles / Architectural Invariants:

*   **Humans own intent**: The system assists, but does not invent project goals.
*   **Tasks are first-class**: Chat and interactions are centered around tasks.
*   **No hidden state**: All system information, decisions, and transitions are inspectable.
*   **Separation of Concerns**: Clear boundaries between Knowledge, Project, Task, Execution, and Workflow.
*   **Executors are replaceable**: The system is independent of specific LLM or tool providers.
*   **Failures are first-class**: Structured outcomes with defined recovery paths.
*   **Prefer open standards**: Utilize existing protocols and libraries where applicable.
*   **Store durable semantics**: Persist decisions, artifacts, and metrics.

## Engineering Conventions

Small, cross-cutting implementation choices for this codebase (logging
library, healthcheck response shape, API error shape, etc.) are recorded in
`docs/engineering conventions.md`. Check it before introducing a new pattern
that overlaps with an existing one, and add to it whenever a new such
decision is made.

## Common Development Tasks

Based on the project summary, the primary development tasks involve managing the lifecycle of Tasks and Projects. As this repository is likely a "control plane" rather than a traditional code repository with build/test steps, common commands will revolve around:

*   **Task Management:**
    *   Creating new tasks.
    *   Updating task status and stages.
    *   Inspecting task details, plans, and execution history.
*   **Project Management:**
    *   Defining and updating project contexts.
    *   Linking repositories and knowledge sources to projects.
*   **Knowledge Management:**
    *   Adding and modifying knowledge base documents.

Specific commands for these tasks are not yet present in the codebase. Future instances of Claude Code should look for command-line tools or scripts within the `data/projects/` or `data/tasks/` directories that facilitate these operations. Given the "git-backed" nature, direct manipulation of YAML files within these directories using `Read`, `Write`, and `Edit` tools would also be a common approach for managing these structured workflows.

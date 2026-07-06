# Milestone 2 — Tasks

**Status: Done** — closed out 2026-07-06, implemented in PR #4.

Now add:

* create task (client-specified id, within a project)
* edit task (project is fixed at creation and can never change)
* list tasks (per-project only — no cross-project task list)
* create/edit project (basic CRUD)
* persist to plain on-disk directory structure, nested under the owning
  project (data/projects/{projectId}/tasks/{taskId}/task.yaml) — not a
  Git-backed repository (deferred; see the backlog task tracking that idea)

Still no AI. No in-app git operations.

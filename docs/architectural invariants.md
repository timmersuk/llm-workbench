# Architectural Invariants

These principles should only change with exceptional justification.

## Humans own intent

The system assists but does not invent project goals.

## Tasks are first-class

Chat is attached to tasks, not vice versa.

## No hidden state

The workflow engine never knows something it cannot show the user.

## Knowledge is separate from intent

Long-lived knowledge is distinct from task-specific planning.

## Providers are replaceable

The workflow engine depends on stable interfaces, not specific
implementations. Executors, knowledge stores, and LLM APIs are all
providers behind narrow interfaces — see `docs/provider abstraction.md`.

## Failures are first-class

Failure is a structured outcome with recovery paths.

## Prefer open standards

Use existing protocols and libraries where they fit. Build only where the project defines unique semantics.

## Store durable semantics

Persist decisions, artifacts and metrics—not transient internal reasoning.

Task-scoped agent selection is durable semantics: both independent task
defaults and every invocation's actual executor/model/effort are persisted.
Stale persisted selections are shown and blocked, never silently rewritten
or substituted. Migration is an explicit one-shot operation, never startup
or read behavior.

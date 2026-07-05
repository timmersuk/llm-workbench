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

## Executors are replaceable

The workflow engine coordinates work but is independent of the implementation technology.

## Failures are first-class

Failure is a structured outcome with recovery paths.

## Prefer open standards

Use existing protocols and libraries where they fit. Build only where the project defines unique semantics.

## Store durable semantics

Persist decisions, artifacts and metrics—not transient internal reasoning.
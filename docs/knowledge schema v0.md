# Knowledge Schema v0

Knowledge is durable, reusable information — coding standards,
architecture decisions, system design notes, domain knowledge,
operational practices (see `architectural invariants.md`: "Knowledge is
separate from intent", and `project_summary.md` §3.3). It is stored
separately from Tasks and Projects but may be referenced by both.

This schema adopts an existing open standard rather than inventing a
bespoke one, per the "prefer open standards" invariant.

---

## 1. Format: Open Knowledge Format (OKF)

The Knowledge layer stores documents using the
[Open Knowledge Format (OKF) v0.1](https://github.com/GoogleCloudPlatform/knowledge-catalog/blob/main/okf/SPEC.md) —
a vendor-neutral spec for representing knowledge as markdown files with
YAML frontmatter, organized into a directory hierarchy ("bundle"). OKF
was chosen over a bespoke format because it's:

- Readable with no tooling (`cat` a file) and parseable by agents with
  no bespoke SDK.
- Git-native: diffable, blameable, reviewable as normal pull requests.
- Portable: a bundle is just a directory; no API or service lock-in.
- Minimally opinionated: one required field (`type`), everything else
  optional or producer-defined — so it doesn't need to be re-specified
  as the kinds of knowledge this project captures evolve.

`data/knowledge/` (`WORKSPACE_ROOT/knowledge`) *is* the OKF bundle root
for this workspace. Each Project's `knowledge: []` list and each Task's
`references.knowledge: []` list (see `task schema v0.md` §2, §6) hold
**concept IDs** — an OKF concept's file path under the bundle root with
the `.md` suffix removed, e.g. `coding-standards/logging` for
`data/knowledge/coding-standards/logging.md`.

---

## 2. Concept documents

Every knowledge file is a concept: a UTF-8 markdown file with a YAML
frontmatter block followed by a markdown body.

```yaml
---
type: <Type name>                  # REQUIRED — e.g. Coding Standard,
                                    # Architecture Decision, Design Note,
                                    # Domain Note, Operational Practice,
                                    # Reference
title: <Optional display name>
description: <Optional one-line summary>
resource: <Optional canonical URI, e.g. a repo path or external doc>
tags: [<tag>, …]                   # Optional
timestamp: <ISO 8601 datetime>     # Optional, last meaningful change
# … any other producer-defined fields
---
```

Only `type` is required. `type` values are not centrally registered —
pick something descriptive; consumers (including this project's own
tooling, once built) must tolerate unknown types rather than rejecting
the document. Unknown extra frontmatter keys must be preserved, not
stripped, by anything that round-trips these files.

The body is free-form markdown. Two conventional section headings are
worth using when applicable, matching OKF §4.2: `# Examples` for
concrete usage, and `# Citations` for external sources backing a claim,
numbered (`[1] [label](url)`).

### Example

```markdown
---
type: Coding Standard
title: Logging
description: Structured logging conventions for the Go backend.
tags: [backend, observability]
timestamp: 2026-07-05T00:00:00Z
---

Backend logging uses `logrus`, configured globally in
`cmd/server/main.go`. Use `logrus.WithFields` for structured context
rather than string-formatted messages.

# Citations

[1] [Engineering conventions — Logging](../engineering%20conventions.md#logging)
```

---

## 3. Cross-linking

Concepts link to each other with standard markdown links, per OKF §5.
Prefer bundle-relative absolute links (starting `/`, relative to
`data/knowledge/`) since they survive a concept being moved within its
subdirectory: `[customers](/tables/customers.md)`. A link asserts *some*
relationship; the kind of relationship is conveyed by the surrounding
prose, not the link syntax. Broken links are not malformed — they may
represent knowledge that hasn't been written yet.

---

## 4. Index and log files

Two filenames are reserved at any level of `data/knowledge/`, per OKF
§6–§7, and must not be used for concept documents:

| Filename   | Purpose                                                        |
|------------|-----------------------------------------------------------------|
| `index.md` | Directory listing for progressive disclosure — no frontmatter, grouped link lists. |
| `log.md`   | Chronological, date-grouped history of changes to that scope.  |

Both are optional and may be hand-written or generated.

---

## 5. Relationship to the "LLM wiki" pattern

The bundle is meant to be maintained the way Karpathy describes an
["LLM wiki"](https://gist.github.com/karpathy/442a6bf555914893e9891c11519de94f):
a compounding, LLM-synthesized knowledge base rather than something
rediscovered from scratch on every query. That pattern maps onto three
layers, which correspond directly to this workbench's own abstractions:

- **Raw sources** — immutable inputs (code, docs, task executions,
  conversations). These are *not* stored as Knowledge; they live where
  they already do (the repo, `execution.yaml`s, chat history).
- **The wiki** — `data/knowledge/`'s OKF concept documents themselves:
  synthesized, cross-referenced, curated over time.
- **The schema** — this document plus `CLAUDE.md` /
  `docs/engineering conventions.md`, defining structure and conventions.

And onto three operations a future knowledge-maintaining executor
should support, rather than leaving the wiki to rot:

- **Ingest** — after a Task/Execution produces a durable insight,
  update the 1–2 relevant concept docs (or mint a new one) instead of
  letting the insight live only in that task's `context.yaml`.
- **Query** — answer questions from the bundle with citations, rather
  than re-deriving answers from raw sources each time.
- **Lint** — periodically check for contradictions, staleness
  (`timestamp` drift), and orphaned concepts (no inbound links).

Per the "humans own intent" invariant, a human still curates *which*
sources matter and directs what gets ingested; the mechanical
maintenance (writing, cross-linking, keeping current) is the part
suited to being executor-driven.

---

## 6. Status

No Go-side store for this bundle exists yet (see
`docs/engineering conventions.md` → Storage & file layout, "eventually
`knowledge/` nested under it"). This document fixes the on-disk format
so that the store, when built, has a spec to parse against rather than
inventing one ad hoc — the same reason `task schema v0.md` predates the
task store's full feature set.

---

## 7. One-line definition

> Knowledge is a bundle of OKF concept documents under `data/knowledge/`
> — markdown with YAML frontmatter, cross-linked, referenced by ID from
> Tasks and Projects — maintained as a compounding wiki rather than
> re-derived from scratch each time.

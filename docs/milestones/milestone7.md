# Milestone 7 — Merge and Knowledge Promotion

**Status: Not started** — scoped by Milestone 6's "Out of scope" section
(`docs/milestones/milestone6.md`); not yet designed in detail. Design
this properly via its own `/grill-with-docs` session once Milestone 6
ships, rather than assuming the sketch below.

## Introduces

* **Merging the execution branch into its base branch.** Once a review
  is `approved` and a task reaches `stage: complete` (Milestone 6), the
  worktree/branch Execution created (`internal/agentrunner/worktree.go`)
  still just sits there, human-mergeable by hand. No merge helper exists
  anywhere in `internal/agentrunner` today. See
  `docs/milestones/milestone6.md`'s "Out of scope" for why this wasn't
  built alongside Review: it has no existing machinery to extend
  (conflict-handling policy, push/PR strategy, etc. all need designing
  from scratch).
* **Knowledge-base promotion.** `docs/vision.md`'s closing beat ("the
  system suggests promoting a newly discovered architectural pattern
  into the knowledge base") has no code behind it — `internal/knowledge/knowledge.go`
  is deliberately read-only (`FileReader.Get(conceptID)` only, no
  Create/Update/List/index), and `docs/knowledge schema v0.md` §6
  confirms no Go-side store exists yet. This milestone would need to
  design and build that write path, plus whatever surfaces a promotion
  suggestion to a human at task completion.

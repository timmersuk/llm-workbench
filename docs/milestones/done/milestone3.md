# Milestone 3 — Chat

**Status: Done** — closed out 2026-07-06, implemented in PR #6 (stability fix in PR #7).

Only then:

* streaming responses

Chat UI (message history, model picker) and provider abstraction
(`ChatClient`, satisfied by `internal/chat`'s OpenAI-compatible client)
already shipped as part of Milestone 1 — this milestone's only remaining
deliverable is making chat completions stream token-by-token (including
reasoning content, for models that emit it) instead of blocking for the
full response.

"Conversation attached to task" moved to `docs/milestones/milestone4.md` —
a persisted conversation only has a real purpose once GrillMe exists to
synthesize it into `context.yaml`/`plan.yaml`; building the persistence
now, with nothing downstream to consume it, would be over-specifying ahead
of need.

This streaming design is deliberately non-resumable — a dropped
connection or refresh loses the in-progress response with no way to
reconnect. Accepted debt, not a defect; tracked in
`docs/milestones/milestone-orphans.md`.

Notice that by this point you're still building infrastructure.

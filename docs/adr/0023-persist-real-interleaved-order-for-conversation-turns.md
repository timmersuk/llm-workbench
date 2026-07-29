# Persist and render a Conversation turn's real interleaved order of narration and tool activity

A Review-stage turn can narrate between tool calls — "build passes, now
testing", `go test`, "tests pass, now checking the frontend", `npm test`,
and so on. Both the persisted record and the live-streaming render
previously destroyed that real chronological order: `runStageTurn`
(`internal/api/stage_conversation.go`, shared by every stage — Requirements,
Planning, Review) flattened a turn into two independent containers —
`ConversationMessage.Content` (one string) and `ConversationMessage.ToolActivity`
(one flat list) — and the frontend always rendered every tool call first,
then all the narration, regardless of when each actually happened.

This is a structural limit, not a populating bug: two independently-ordered
containers cannot record order *between* each other. A turn with narration
before, between, and after two tool calls produces multiple pieces of text,
but there is only one `Content` field to hold text in, so every piece gets
concatenated into it regardless of which calls it was adjacent to; the tool
calls go into their own list, ordered only among themselves. The two
straightforward ways to recover order — tagging each side with a
correlating position/offset so a reader can reconstruct the interleaving,
or replacing the two containers with one ordered sequence — were both
considered; see "Considered and rejected" below for why the former was
rejected here.

## Decision

Add `Segments []ConversationSegment` to `ConversationMessage`
(`internal/task/conversation.go`) — the turn's real chronological sequence,
each entry either a run of narration text (`Kind: SegmentKindText`) or a
maximal run of consecutive tool calls (`Kind: SegmentKindTools`, the same
"sequence" concept CONTEXT.md's Tool Activity entry already uses for
Execution runs). Read top to bottom, it *is* the turn, in the order it
happened — no correlating key, no cross-referencing required.

`Content`/`ToolActivity` remain on `ConversationMessage`, but are now
*derived* from `Segments` rather than independently tracked — other code
(`conversationHistoryToChatMessages`'s model-context replay,
`summarizeToolActivity`/`summarizeToolActivities`, `TaskDetailPanel`'s
failure-text search) keeps reading them unchanged. `runStageTurn` builds
`Segments` directly from the same `OnToolCall`/`OnToolResult`/`onDelta`
closures that already stream live SSE events, coalescing consecutive
same-kind entries; `content`/`activity` are then derived from the finished
`Segments` in one pass, rather than hand-maintained in parallel — this also
happens to fix a separate latent bug where one runner backend's per-round
text accumulator only reported its last round's text, silently dropping
earlier narration that had nonetheless streamed live.

A message persisted before this field existed has `Segments == nil`; real
order was never captured for it and cannot be recovered retroactively.
`ConversationMessage.EffectiveSegments()` returns `Segments` when present,
or else synthesizes `[{Kind: Tools, ToolActivity}, {Kind: Text, Content}]`
— today's exact bundled rendering, not a guessed real order — and
`MarshalJSON` uses it to guarantee every API response carries a populated
`segments` array. This is wire-only: `MarshalYAML` persists exactly what a
turn actually recorded (nil for legacy, real for new), never fabricating
data into the file on disk.

The frontend (`StageConversationPanel.tsx`) renders a message's `segments`
in order for both live streaming and historical/reloaded conversations —
one shared code path across every stage, mirroring `ExecutePanel.tsx`'s
already-proven `TraceBlock` pattern (`{kind:'text'} | {kind:'tools'}`,
built incrementally as events arrive).

## Supersedes

- ADR-0017's rejection of "one separate `ConversationMessage` per tool
  call/result" reasoned that "the underlying message stream already
  flattens a turn's text into one accumulated string with no preserved
  interleaving against tool calls... so there's no finer-grained ordering
  to actually preserve." That's no longer true: `runStageTurn` no longer
  trusts the flattened `out.Content` return value, and building `Segments`
  directly from the live event stream is exactly the finer-grained
  ordering that was assumed unavailable.
- ADR-0019's rendering-layer extraction stated "a Conversation turn is
  already one sequence by construction (tool activity is bundled onto a
  single closing assistant message, never interleaved with turn text)."
  That assumption is corrected here — a Conversation turn can now split
  into multiple sequences the same way an Execution run always could.

## Considered and rejected

* **Tag each `ToolActivity` entry with a `Content` offset** (how many
  characters of `Content` had been emitted when that call fired), instead
  of a new `Segments` field — a single `int` on an existing struct, no new
  type. Rejected: it still requires a reader to cross-reference two
  separate fields (split `Content` at the recorded offsets, splice in
  `ToolActivity` entries) to see the real order — not something a human
  can read directly out of the persisted YAML top to bottom. `Segments`
  costs one new type but is inspectable on its own; the offset scheme
  costs less schema but pushes the reconstruction work onto every reader,
  forever.
* **Unify `Segments`' shape with `ExecutePanel`'s Execution trace
  persistence.** Rejected as out of scope: Execution logs are a genuinely
  different persisted shape (`exec-NNN.log.yaml`, a flat replayable event
  log) for a genuinely different concept (an unattended run to completion,
  not a human-paced turn) — ADR-0019 already made this call for the
  rendering layer, and nothing here changes that. Only the *rendering
  pattern* (`{kind:'text'} | {kind:'tools'}` blocks, built the same way for
  live and historical data) is deliberately reused, not the underlying
  data model.

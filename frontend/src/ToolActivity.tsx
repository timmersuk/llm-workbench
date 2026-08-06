// ToolActivity is the rendering layer shared by StageConversationPanel and
// ExecutePanel (docs/adr/0019) — the only thing unified between them is how
// a sequence of tool calls is displayed; each panel keeps its own data feed
// (a Conversation turn's bundled ConversationToolActivity[] vs. an
// Execution's flat, interleavable SSE trace) and normalizes it into
// ToolActivityEntry before handing it to ToolActivitySequence.

// LIVE_VISIBLE_COUNT is how many of a still-in-progress sequence's most
// recent calls stay individually visible rather than folding into the
// "N earlier tool calls" summary — enough to show real incremental
// progress (docs/adr/0019's revision of docs/adr/0018's live-chip
// rejection) without the chip list growing unbounded on a long run.
const LIVE_VISIBLE_COUNT = 3

// PREVIEW_MAX_CHARS bounds the list-tier argument preview to a single
// scannable line — generic truncation of the raw arguments string, not a
// per-tool-type extraction (docs/adr/0019's "Considered and rejected").
const PREVIEW_MAX_CHARS = 60

export interface ToolActivityEntry {
  // id correlates this call to its later result — the provider's own
  // per-call id (e.g. the claude CLI's ToolUseID), threaded through so a
  // result can attach to the exact call it belongs to rather than
  // assuming results always arrive in the same order calls were declared
  // (false for a provider that can declare several calls before returning
  // any of their results — see toolActivityBlocks.ts). Render-only: never
  // persisted upstream of this type.
  id?: string
  name: string
  arguments?: string
  result?: string
  isError?: boolean
}

// normalizeArgs re-serializes arguments through JS's own JSON.stringify,
// falling back to the raw string unchanged when it isn't valid JSON (nothing
// here depends on that being guaranteed). This matters beyond formatting:
// the backend's arguments string comes from Go's encoding/json, which
// HTML-escapes `<`/`>`/`&` into `<`/`>`/`&` by default —
// JSON.stringify doesn't, so round-tripping through parse/stringify is what
// turns a literal "2>&1" back into the readable "2>&1" a human
// expects to see, for both the preview and the drill-in tier.
function normalizeArgs(text: string | undefined): string {
  if (!text) {
    return ''
  }
  try {
    return JSON.stringify(JSON.parse(text))
  } catch {
    return text
  }
}

function truncatePreview(args: string | undefined): string {
  const normalized = normalizeArgs(args).replace(/\s+/g, ' ').trim()
  return normalized.length > PREVIEW_MAX_CHARS ? normalized.slice(0, PREVIEW_MAX_CHARS) + '…' : normalized
}

function prettyJson(text: string | undefined): string {
  if (!text) {
    return ''
  }
  try {
    return JSON.stringify(JSON.parse(text), null, 2)
  } catch {
    return text
  }
}

function statusGlyph(activity: ToolActivityEntry): { char: string; className?: string } {
  if (activity.result === undefined) {
    return { char: '⋯' }
  }
  return activity.isError ? { char: '✕', className: 'tool-status-error' } : { char: '✓' }
}

// ToolActivityRow is the list tier (name + status + argument preview) and,
// nested inside it, the drill-in tier (full arguments/result) — one call,
// expandable one level deeper than the sequence-level summary.
function ToolActivityRow({ activity }: { activity: ToolActivityEntry }) {
  const pending = activity.result === undefined
  const glyph = statusGlyph(activity)

  return (
    <details className="thinking-panel tool-activity-row">
      <summary>
        <span className={glyph.className}>{glyph.char}</span>{' '}
        <span className="tool-call-chip">{activity.name}</span>
        <span className="tool-activity-preview">{truncatePreview(activity.arguments)}</span>
        {pending && <span className="tool-activity-pending"> running…</span>}
      </summary>
      <div className={activity.isError ? 'thinking-content error' : 'thinking-content'}>
        <strong>Arguments</strong>
        <div>{prettyJson(activity.arguments)}</div>
        {activity.result !== undefined && (
          <>
            <strong>Result</strong>
            <div>{activity.result}</div>
          </>
        )}
      </div>
    </details>
  )
}

function hasError(activities: ToolActivityEntry[]): boolean {
  return activities.some((a) => a.isError)
}

function countLabel(n: number): string {
  return `${n} tool${n === 1 ? '' : 's'}`
}

// ToolActivitySequence renders one sequence (docs/adr/0019: a maximal run
// of tool calls uninterrupted by assistant text — a Conversation turn's
// whole toolActivity list, or one tools block within an Execution's trace).
// `live` marks a sequence still being appended to (the current turn/run's
// last, still-streaming sequence); everything else renders "at rest".
export function ToolActivitySequence({ activities, live }: { activities: ToolActivityEntry[]; live: boolean }) {
  if (activities.length === 0) {
    return null
  }

  if (!live) {
    return (
      <details className="thinking-panel tool-activity-sequence">
        <summary>
          Used {countLabel(activities.length)}
          {hasError(activities) && <span className="tool-status-error"> ⚠</span>}
        </summary>
        {activities.map((activity, i) => (
          <ToolActivityRow key={i} activity={activity} />
        ))}
      </details>
    )
  }

  if (activities.length <= LIVE_VISIBLE_COUNT) {
    return (
      <>
        {activities.map((activity, i) => (
          <ToolActivityRow key={i} activity={activity} />
        ))}
      </>
    )
  }

  const splitIndex = activities.length - LIVE_VISIBLE_COUNT
  const earlier = activities.slice(0, splitIndex)
  const visible = activities.slice(splitIndex)

  return (
    <>
      <details className="thinking-panel tool-activity-sequence">
        <summary>
          {earlier.length} earlier tool call{earlier.length === 1 ? '' : 's'}
          {hasError(earlier) && <span className="tool-status-error"> ⚠</span>}
        </summary>
        {earlier.map((activity, i) => (
          <ToolActivityRow key={i} activity={activity} />
        ))}
      </details>
      {visible.map((activity, i) => (
        <ToolActivityRow key={i} activity={activity} />
      ))}
    </>
  )
}

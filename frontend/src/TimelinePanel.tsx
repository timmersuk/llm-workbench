import { useEffect, useState } from 'react'
import { getStageConversation, listReviews, listStageTransitions } from './api'
import { MarkdownMessage } from './MarkdownMessage'
import type { ConversationMessage, Review, StageTransition } from './types'

interface TimelinePanelProps {
  projectId: string
  taskId: string
}

type TimelineEntry =
  | { kind: 'transition'; key: string; created_at: string; transition: StageTransition }
  | { kind: 'review'; key: string; created_at: string; review: Review }

// conversationStages are the Stage values with a persisted Conversation
// (task.validateConversationStage, internal/task/conversation.go) — the
// only stages a "view conversation" link can ever resolve.
const conversationStages = new Set(['requirements', 'planning', 'review'])

function triggerLabel(trigger: StageTransition['trigger']): string {
  switch (trigger) {
    case 'finalize_requirements':
      return 'finalized requirements'
    case 'finalize_plan':
      return 'finalized plan'
    case 'finalize_review':
      return 'finalized review'
    case 'execution_success':
      return 'execution succeeded'
    case 'mark_pr_merged':
      return 'marked PR merged'
    case 'revise_requirements':
      return 'revised back to requirements'
    case 'revise_planning':
      return 'revised back to planning'
    default:
      return trigger
  }
}

// ConversationState tracks one transition entry's on-demand-fetched
// destination-stage conversation, keyed by the entry's key — same lazy-
// expand-then-cache shape as ExecutionHistoryList's PastExecutionLogState.
type ConversationState =
  | { status: 'loading' }
  | { status: 'loaded'; messages: ConversationMessage[] }
  | { status: 'error'; message: string }

// TimelinePanel merges every recorded stage transition with every recorded
// review verdict into one chronological list, so a task's real path through
// its lifecycle — including reversals — is visible, not just its current
// stage. Expanding a transition whose destination stage has a Conversation
// (requirements/planning/review) lazily loads that conversation, filtered to
// messages recorded at or after this transition, showing what was actually
// discussed during that particular pass rather than every pass merged
// together in one file.
export function TimelinePanel({ projectId, taskId }: TimelinePanelProps) {
  const [entries, setEntries] = useState<TimelineEntry[]>([])
  const [conversations, setConversations] = useState<Record<string, ConversationState>>({})

  useEffect(() => {
    let cancelled = false
    Promise.all([listStageTransitions(projectId, taskId), listReviews(projectId, taskId)])
      .then(([transitionsResult, reviewsResult]) => {
        if (cancelled) {
          return
        }
        const merged: TimelineEntry[] = [
          ...(transitionsResult.stage_transitions ?? []).map((t, i) => ({
            kind: 'transition' as const,
            key: `transition-${i}-${t.created_at}`,
            created_at: t.created_at,
            transition: t,
          })),
          ...(reviewsResult.reviews ?? []).map((r) => ({
            kind: 'review' as const,
            key: `review-${r.review_id}`,
            created_at: r.created_at,
            review: r,
          })),
        ]
        merged.sort((a, b) => a.created_at.localeCompare(b.created_at))
        setEntries(merged)
      })
      .catch(() => undefined) // no history yet, or failed to load — the rest of the task view still stands
    return () => {
      cancelled = true
    }
  }, [projectId, taskId])

  function handleToggle(entry: TimelineEntry, open: boolean) {
    if (!open || entry.kind !== 'transition' || conversations[entry.key]) {
      return
    }
    const stage = entry.transition.to_stage
    if (!conversationStages.has(stage)) {
      return
    }
    setConversations((prev) => ({ ...prev, [entry.key]: { status: 'loading' } }))
    getStageConversation(projectId, taskId, stage)
      .then((conv) => {
        const since = entry.transition.created_at
        const messages = (conv.messages ?? []).filter((m) => m.created_at >= since)
        setConversations((prev) => ({ ...prev, [entry.key]: { status: 'loaded', messages } }))
      })
      .catch((err) => {
        setConversations((prev) => ({
          ...prev,
          [entry.key]: { status: 'error', message: err instanceof Error ? err.message : String(err) },
        }))
      })
  }

  if (entries.length === 0) {
    return null
  }

  return (
    <div className="task-interview">
      <h4>Timeline</h4>
      <ul className="execution-history">
        {entries.map((entry) => {
          if (entry.kind === 'review') {
            const r = entry.review
            return (
              <li key={entry.key} className="timeline-review">
                review {r.review_id}: {r.decision}
                {r.notes && <p>{r.notes}</p>}
              </li>
            )
          }

          const t = entry.transition
          const convState = conversations[entry.key]
          const canViewConversation = conversationStages.has(t.to_stage)

          return (
            <li key={entry.key} className="timeline-transition">
              <details onToggle={(ev) => handleToggle(entry, ev.currentTarget.open)}>
                <summary>
                  {t.from_stage} &rarr; {t.to_stage} ({triggerLabel(t.trigger)})
                  {t.reason && <> &middot; &ldquo;{t.reason}&rdquo;</>}
                </summary>
                {canViewConversation && (
                  <div className="execution-log">
                    {convState?.status === 'loading' && <p className="execution-log-status">Loading conversation…</p>}
                    {convState?.status === 'error' && <p className="error">Could not load conversation: {convState.message}</p>}
                    {convState?.status === 'loaded' && (
                      <>
                        {convState.messages.length === 0 && (
                          <p className="execution-log-status">No messages recorded after this point.</p>
                        )}
                        {convState.messages.map((m, i) => (
                          <div key={i} className="chat-message">
                            <MarkdownMessage content={m.content} />
                          </div>
                        ))}
                      </>
                    )}
                  </div>
                )}
              </details>
            </li>
          )
        })}
      </ul>
    </div>
  )
}

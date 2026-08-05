import { useEffect, useState } from 'react'
import { finalizeKnowledge, finalizeReview, getReviewDiff, listExecutions, listStageTransitions } from './api'
import { DiffView } from './DiffView'
import { KnowledgeDraftForm } from './KnowledgeDraftForm'
import { ReviewDraftSummary } from './ReviewDraftSummary'
import { stageConversationOps } from './stageConversationOps'
import { StageConversationPanel } from './StageConversationPanel'
import type { AgentSelection, Execution, KnowledgeConceptDraft, Review, ReviewDraft, Task } from './types'

// needs_changes is the neutral default for a proposal that omits the field —
// it neither approves nor rejects, so a malformed tool call can't accidentally
// complete or reopen a task. The agent's propose_review always sets it in
// practice; this only fills a gap.
const EMPTY_DRAFT: ReviewDraft = { decision: 'needs_changes', notes: '' }

// EMPTY_KNOWLEDGE_DRAFT is propose_knowledge's neutral default — an empty
// concept_id/type/body just means there's nothing sensible to rehydrate or
// merge into, matching EMPTY_DRAFT's own role above.
const EMPTY_KNOWLEDGE_DRAFT: KnowledgeConceptDraft = { concept_id: '', type: '', frontmatter: {}, body: '' }

// proposeKnowledgeToolName mirrors drafttool.ProposeKnowledgeName
// (internal/drafttool/drafttool.go) — no shared codegen between the Go and
// TypeScript sides, so this string is kept in sync by convention, the same
// way every other tool_call.name string this frontend matches against is.
const proposeKnowledgeToolName = 'propose_knowledge'

interface ReviewPanelProps {
  projectId: string
  taskId: string
  onFinalized: (task: Task, review: Review) => void
  // onKnowledgeActivity, when supplied, is called with the refreshed task
  // (its knowledge_activity now including this decision's entry) after
  // every propose_knowledge accept/reject — best-effort, since
  // finalizeKnowledge's task field is itself best-effort (omitted if
  // recording the entry failed server-side, see finalizeKnowledgeResponse's
  // doc comment). Lets a caller (TaskDetailPanel) keep its own copy of the
  // task in sync so a Timeline showing knowledge_activity reflects a
  // just-decided proposal immediately, without a page reload.
  onKnowledgeActivity?: (task: Task) => void
  defaultSelection?: AgentSelection
}

// ReviewPanel is the Review-stage screen: a read-only summary of the execution
// under review (branch, commits, changed files) with the full patch available
// on demand, above a StageConversationPanel that runs the review conversation.
// autoStart is false — arriving here shows the diff and waits for an explicit
// "Start Review", since the agent's first turn runs the real test suite in a
// worktree, not just an opening question (docs/milestones/done/milestone6.md PR 3).
export function ReviewPanel({ projectId, taskId, onFinalized, onKnowledgeActivity, defaultSelection }: ReviewPanelProps) {
  const [latest, setLatest] = useState<Execution | null>(null)
  const [patch, setPatch] = useState<string | null>(null)
  // cycleStartAt marks when this task most recently entered the review
  // stage — the boundary StageConversationPanel needs to tell "this round's
  // pending draft" apart from an earlier round's already-finalized one still
  // sitting at the tail of the stage's continuously-appended Conversation
  // (see its cycleStartAt doc comment for the bug this prevents). Resolved
  // (to a real timestamp or definitively undefined) before
  // StageConversationPanel ever mounts — see cycleStartAtResolved below for
  // why that ordering matters.
  const [cycleStartAt, setCycleStartAt] = useState<string | undefined>(undefined)
  // cycleStartAtResolved gates StageConversationPanel's render until the
  // listStageTransitions call below has settled. StageConversationPanel's
  // own rehydration runs once, in its mount effect — mounting it before
  // cycleStartAt is known would let that effect capture cycleStartAt's
  // initial `undefined` and rehydrate against the *entire* history (the
  // exact bug cycleStartAt exists to prevent), since the prop updating
  // afterward doesn't re-trigger that one-time effect.
  const [cycleStartAtResolved, setCycleStartAtResolved] = useState(false)

  useEffect(() => {
    let cancelled = false
    listExecutions(projectId, taskId)
      .then((r) => {
        if (cancelled) {
          return
        }
        const list = r.executions ?? []
        setLatest(list.length > 0 ? list[list.length - 1] : null)
      })
      .catch(() => undefined) // no executions to summarize — the conversation still stands on its own
    getReviewDiff(projectId, taskId)
      .then((r) => {
        if (!cancelled) {
          setPatch(r.patch)
        }
      })
      .catch(() => undefined) // 404 / worktree gone — just omit the diff, don't block review
    listStageTransitions(projectId, taskId)
      .then((r) => {
        if (cancelled) {
          return
        }
        const intoReview = (r.stage_transitions ?? []).filter((t) => t.to_stage === 'review')
        setCycleStartAt(intoReview.length > 0 ? intoReview[intoReview.length - 1].created_at : undefined)
      })
      .catch(() => undefined) // no transitions yet — cycleStartAt stays undefined, same as the very first review ever
      .finally(() => {
        if (!cancelled) {
          setCycleStartAtResolved(true)
        }
      })
    return () => {
      cancelled = true
    }
  }, [projectId, taskId])

  return (
    <div className="review-panel">
      {latest && (
        <div className="review-summary">
          <h4>Execution under review</h4>
          <p>
            {latest.execution_id} &middot; {latest.status}
            {latest.output.git_branch && <> &middot; {latest.output.git_branch}</>}
            {latest.metrics.tokens_used > 0 && <> &middot; {latest.metrics.tokens_used} tokens</>}
            {latest.metrics.cost_estimate > 0 && <> &middot; ${latest.metrics.cost_estimate.toFixed(2)}</>}
          </p>
          {(latest.output.commits?.length ?? 0) > 0 && (
            <>
              <strong>Commits</strong>
              <ul>
                {latest.output.commits.map((c) => (
                  <li key={c}>{c}</li>
                ))}
              </ul>
            </>
          )}
          {(latest.output.artifacts?.length ?? 0) > 0 && (
            <>
              <strong>Changed files</strong>
              <ul>
                {latest.output.artifacts.map((f) => (
                  <li key={f}>{f}</li>
                ))}
              </ul>
            </>
          )}
          <DiffView patch={patch} />
        </div>
      )}

      {cycleStartAtResolved && (
        <StageConversationPanel<ReviewDraft, KnowledgeConceptDraft>
          conversationKey={`${projectId}:${taskId}:review`}
          ops={stageConversationOps(projectId, taskId, 'review')}
          title="Review"
          description="Start the review to have the agent run the tests, review the diff, and walk the verification steps — then finalize an approved, needs-changes, or rejected verdict. It may also propose a knowledge concept worth recording along the way; accept or reject that independently of the review verdict."
          emptyDraft={EMPTY_DRAFT}
          autoStart={false}
          startLabel="Start Review"
          defaultSelection={defaultSelection}
          cycleStartAt={cycleStartAt}
          draftIsEditable={false}
          renderDraft={(draft) => <ReviewDraftSummary draft={draft} />}
          onFinalize={async (draft) => {
            const result = await finalizeReview(projectId, taskId, draft)
            onFinalized(result.task, result.review)
          }}
          secondaryDraft={{
            toolName: proposeKnowledgeToolName,
            emptyDraft: EMPTY_KNOWLEDGE_DRAFT,
            heading: 'Proposed knowledge concept',
            renderDraft: (draft, onChange) => <KnowledgeDraftForm draft={draft} onChange={onChange} />,
            onAccept: async (draft) => {
              const result = await finalizeKnowledge(projectId, taskId, draft, 'accepted')
              if (result.task) {
                onKnowledgeActivity?.(result.task)
              }
              return result.note
            },
            onReject: async (draft) => {
              const result = await finalizeKnowledge(projectId, taskId, draft, 'rejected')
              if (result.task) {
                onKnowledgeActivity?.(result.task)
              }
              return result.note
            },
          }}
        />
      )}
    </div>
  )
}

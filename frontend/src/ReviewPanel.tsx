import { useEffect, useState } from 'react'
import { finalizeKnowledge, finalizeReview, getReviewDiff, listExecutions } from './api'
import { KnowledgeDraftForm } from './KnowledgeDraftForm'
import { ReviewDraftForm } from './ReviewDraftForm'
import { stageConversationOps } from './stageConversationOps'
import { StageConversationPanel } from './StageConversationPanel'
import type { Execution, KnowledgeConceptDraft, Review, ReviewDraft, Task } from './types'

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
}

// ReviewPanel is the Review-stage screen: a read-only summary of the execution
// under review (branch, commits, changed files) with the full patch available
// on demand, above a StageConversationPanel that runs the review conversation.
// autoStart is false — arriving here shows the diff and waits for an explicit
// "Start Review", since the agent's first turn runs the real test suite in a
// worktree, not just an opening question (docs/milestones/done/milestone6.md PR 3).
export function ReviewPanel({ projectId, taskId, onFinalized }: ReviewPanelProps) {
  const [latest, setLatest] = useState<Execution | null>(null)
  const [patch, setPatch] = useState<string | null>(null)

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
          {patch && (
            <details className="review-diff">
              <summary>View diff</summary>
              <pre>{patch}</pre>
            </details>
          )}
        </div>
      )}

      <StageConversationPanel<ReviewDraft, KnowledgeConceptDraft>
        conversationKey={`${projectId}:${taskId}:review`}
        ops={stageConversationOps(projectId, taskId, 'review')}
        title="Review"
        description="Start the review to have the agent run the tests, review the diff, and walk the verification steps — then finalize an approved, needs-changes, or rejected verdict. It may also propose a knowledge concept worth recording along the way; accept or reject that independently of the review verdict."
        emptyDraft={EMPTY_DRAFT}
        autoStart={false}
        startLabel="Start Review"
        renderDraft={(draft, onChange) => <ReviewDraftForm draft={draft} onChange={onChange} />}
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
            await finalizeKnowledge(projectId, taskId, draft, 'accepted')
          },
          onReject: async (draft) => {
            await finalizeKnowledge(projectId, taskId, draft, 'rejected')
          },
        }}
      />
    </div>
  )
}

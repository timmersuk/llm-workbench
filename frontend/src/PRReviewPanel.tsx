import { useEffect, useState } from 'react'
import { finalizeReview, getReviewDiff, listExecutions, markPRMerged, pushPR } from './api'
import { DiffView } from './DiffView'
import { PRRejectForm } from './PRRejectForm'
import type { Execution, ReviewDraft, Task } from './types'

const EMPTY_REJECT_DRAFT: ReviewDraft = { decision: 'needs_changes', notes: '' }

interface PRReviewPanelProps {
  projectId: string
  taskId: string
  task: Task
  onUpdated: (task: Task) => void
}

// PRReviewPanel is the pr_review-stage screen (docs/milestones/done/milestone7.md
// PR 3): unlike Review, it has no agent conversation — both of pr_review's
// resolutions ("mark as merged", reject) are a human reading external GitHub
// state and asserting a fact, not an agent judgment call. A single static
// layout: the execution summary/diff (mirroring ReviewPanel), "Push & Open
// PR", the PR link, "Mark as merged", and the reject control are all always
// rendered, each enabled/disabled by whether task.pull_request is set —
// every task normally passes through pr_review with no PR yet for however
// long it takes a human to click "Push & Open PR" (deliberately not
// automatic on arrival, the same autoStart: false reasoning as Review
// itself), so that's expected steady-state, not an edge case to hide.
export function PRReviewPanel({ projectId, taskId, task, onUpdated }: PRReviewPanelProps) {
  const [latest, setLatest] = useState<Execution | null>(null)
  const [patch, setPatch] = useState<string | null>(null)

  const [pushing, setPushing] = useState(false)
  const [pushError, setPushError] = useState<string | null>(null)

  const [merging, setMerging] = useState(false)
  const [mergeError, setMergeError] = useState<string | null>(null)

  const [rejectDraft, setRejectDraft] = useState<ReviewDraft>(EMPTY_REJECT_DRAFT)
  const [rejecting, setRejecting] = useState(false)
  const [rejectError, setRejectError] = useState<string | null>(null)

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
      .catch(() => undefined)
    getReviewDiff(projectId, taskId)
      .then((r) => {
        if (!cancelled) {
          setPatch(r.patch)
        }
      })
      .catch(() => undefined)
    return () => {
      cancelled = true
    }
  }, [projectId, taskId])

  async function handlePush() {
    if (pushing) {
      return
    }
    setPushing(true)
    setPushError(null)
    try {
      onUpdated(await pushPR(projectId, taskId))
    } catch (err) {
      setPushError(err instanceof Error ? err.message : String(err))
    } finally {
      setPushing(false)
    }
  }

  async function handleMarkMerged() {
    if (merging) {
      return
    }
    setMerging(true)
    setMergeError(null)
    try {
      onUpdated(await markPRMerged(projectId, taskId))
    } catch (err) {
      setMergeError(err instanceof Error ? err.message : String(err))
    } finally {
      setMerging(false)
    }
  }

  async function handleReject() {
    if (rejecting) {
      return
    }
    setRejecting(true)
    setRejectError(null)
    try {
      const result = await finalizeReview(projectId, taskId, rejectDraft)
      onUpdated(result.task)
    } catch (err) {
      setRejectError(err instanceof Error ? err.message : String(err))
    } finally {
      setRejecting(false)
    }
  }

  const hasPR = Boolean(task.pull_request)

  return (
    <div className="pr-review-panel">
      {latest && (
        <div className="review-summary">
          <h4>Execution to push</h4>
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
          <DiffView patch={patch} />
        </div>
      )}

      <div className="pr-review-actions">
        <button type="button" disabled={hasPR || pushing} onClick={handlePush}>
          {pushing ? 'Pushing…' : 'Push & Open PR'}
        </button>
        {pushError && <p className="error">{pushError}</p>}

        <div className="pr-status">
          {hasPR && task.pull_request ? (
            <p>
              Pull request:{' '}
              <a href={task.pull_request.url} target="_blank" rel="noopener noreferrer">
                #{task.pull_request.number}
              </a>
            </p>
          ) : (
            <p>No pull request opened yet.</p>
          )}
        </div>

        <button type="button" disabled={!hasPR || merging} onClick={handleMarkMerged}>
          {merging ? 'Marking…' : 'Mark as merged'}
        </button>
        {mergeError && <p className="error">{mergeError}</p>}
      </div>

      <div className="pr-reject">
        <h4>Reject</h4>
        <PRRejectForm draft={rejectDraft} onChange={setRejectDraft} onSubmit={handleReject} disabled={rejecting} />
        {rejectError && <p className="error">{rejectError}</p>}
      </div>
    </div>
  )
}

import { useEffect, useState } from 'react'
import { getProjectTask, getTaskContext, getTaskPlan, getWorkspaceStatus, listExecutions, listReviews, reviseRequirements, revisePlan } from './api'
import { ExecutePanel } from './ExecutePanel'
import { ExecutionHistoryList } from './ExecutionHistoryList'
import { GrillMePanel } from './GrillMePanel'
import { PlanningModePanel } from './PlanningModePanel'
import { PRReviewPanel } from './PRReviewPanel'
import { ReviewPanel } from './ReviewPanel'
import { TimelinePanel } from './TimelinePanel'
import type { Execution, Review, Task, TaskContext, TaskPlan, WorkspaceStatusResult } from './types'

interface TaskDetailPanelProps {
  projectId: string
  task: Task
  onBack: () => void
  // onViewDraft navigates to the read-only pre-creation "New Task"
  // conversation (TaskDraftView.tsx) — only ever offered (see the nav link
  // below) when task.draft_session_id is set.
  onViewDraft: () => void
}

// TaskDetailPanel is a task's full view: read-only requirements fields
// (GrillMe-owned once past bare creation), the finalized context/plan
// artifacts, and — depending on the task's current stage — GrillMe,
// Planning Mode, or a Revise action. There is no per-task edit form beyond
// this view; a task is only ever created via the chat-driven "New Task"
// flow (NewTaskPanel.tsx, CONTEXT.md's "Draft").
export function TaskDetailPanel({ projectId, task: initialTask, onBack, onViewDraft }: TaskDetailPanelProps) {
  const [task, setTask] = useState(initialTask)
  const [context, setContext] = useState<TaskContext | null>(null)
  const [plan, setPlan] = useState<TaskPlan | null>(null)
  const [reviseError, setReviseError] = useState<string | null>(null)
  const [revising, setRevising] = useState(false)
  // reviseReason is the optional, skippable explanation shown alongside
  // Revise Requirements/Plan — recorded on the resulting StageTransition and
  // surfaced later in TimelinePanel. Shared between both actions since only
  // one is ever rendered at a time (gated by task.stage below).
  const [reviseReason, setReviseReason] = useState('')
  // review backs the terminal "merged" screen: the latest verdict's notes
  // (the approving review from back at the review stage — merged never
  // itself writes a new one). Loaded only at stage merged (see effect
  // below), so a re-visited completed task re-reads it rather than relying
  // on in-session state. The PR link that screen also shows comes straight
  // off task.pull_request — already on hand, no extra fetch needed.
  const [review, setReview] = useState<Review | null>(null)
  // Project-scoped, not task/stage-scoped — the shared checkout is one per
  // project. Advisory only: a failed fetch just means no banner, never
  // blocks the rest of the view (docs/milestones/done/milestone8a.md).
  const [workspaceStatus, setWorkspaceStatus] = useState<WorkspaceStatusResult | null>(null)
  // latestExecution backs the failure banner below — surfaced at the top of
  // the task view (not just in ExecutePanel's own history list, which is
  // easy to miss since a failed run leaves stage unchanged and the panel
  // scrolled below other content).
  const [latestExecution, setLatestExecution] = useState<Execution | null>(null)
  // executions backs the always-visible execution history/log list below —
  // unlike ExecutePanel's own copy (which needs to append a just-finished
  // run live, mid-stream), this is a plain re-fetch on task change, since a
  // human reaching for this is reloading the task view, not mid-run.
  const [executions, setExecutions] = useState<Execution[]>([])

  useEffect(() => {
    setContext(null)
    getTaskContext(projectId, task.id)
      .then(setContext)
      .catch(() => undefined) // 404 just means requirements haven't been finalized yet
  }, [projectId, task.id, task.stage])

  useEffect(() => {
    setWorkspaceStatus(null)
    getWorkspaceStatus(projectId)
      .then(setWorkspaceStatus)
      .catch(() => undefined)
  }, [projectId])

  useEffect(() => {
    setLatestExecution(null)
    setExecutions([])
    listExecutions(projectId, task.id)
      .then((result) => {
        const list = result.executions ?? []
        setExecutions(list)
        setLatestExecution(list.length > 0 ? list[list.length - 1] : null)
      })
      .catch(() => undefined) // no prior attempts, or the list failed to load — no banner either way
  }, [projectId, task.id])

  useEffect(() => {
    setPlan(null)
    getTaskPlan(projectId, task.id)
      .then(setPlan)
      .catch(() => undefined) // 404 just means planning hasn't been finalized yet
  }, [projectId, task.id, task.stage])

  useEffect(() => {
    setReview(null)
    if (task.stage !== 'merged') {
      return
    }
    listReviews(projectId, task.id)
      .then((r) => {
        const list = r.reviews ?? []
        setReview(list.length > 0 ? list[list.length - 1] : null)
      })
      .catch(() => undefined) // no verdict readable — the confirmation still shows
  }, [projectId, task.id, task.stage])

  async function handleRevise(action: () => Promise<Task>) {
    if (revising) {
      return
    }
    setRevising(true)
    setReviseError(null)
    try {
      setTask(await action())
      setReviseReason('')
    } catch (err) {
      setReviseError(err instanceof Error ? err.message : String(err))
    } finally {
      setRevising(false)
    }
  }

  const hasSummary = Boolean(
    task.objective || task.constraints.length > 0 || task.assumptions.length > 0 || task.success_criteria.length > 0 || context || plan,
  )

  // executionFailureNotice surfaces the most recent run's outcome at the
  // top of the task view when it didn't succeed — ExecutePanel's own
  // execution-history list shows the same status, but it's scrolled below
  // requirements/context/plan and easy to miss, especially since a failed
  // run leaves task.status/stage unchanged (internal/task/execution.go's
  // RecordExecution only advances stage on success). Hitting the turn cap
  // gets its own wording rather than the raw SDK error string, since it's
  // common enough (internal/api/execution.go's executionMaxTurns) to
  // deserve a plain "what happened, what to do" message instead of
  // "Reached maximum number of turns (100)".
  const isMaxTurnsFailure = Boolean(
    latestExecution?.failure?.message && /maximum number of turns/i.test(latestExecution.failure.message),
  )
  const executionFailureNotice =
    latestExecution && latestExecution.status !== 'success'
      ? isMaxTurnsFailure
        ? `Last execution (${latestExecution.execution_id}) stopped after hitting its turn limit before finishing — try again, or split the plan into smaller steps.`
        : `Last execution (${latestExecution.execution_id}) ${latestExecution.status === 'partial' ? 'finished partially' : 'failed'}${latestExecution.failure ? `: ${latestExecution.failure.message}` : '.'}`
      : null

  // Never mentions a clean or unknown signal — same "only say something
  // when there's something to say" framing as the backend's own
  // appendWorkspaceAdvisories (stage_conversation.go), so the two surfaces
  // stay consistent.
  const workspaceNotices: string[] = []
  if (workspaceStatus?.repository_configured) {
    const { behind_origin, dirty } = workspaceStatus.status
    if (behind_origin.known && behind_origin.behind > 0) {
      workspaceNotices.push(
        `Shared checkout is ${behind_origin.behind} commit${behind_origin.behind === 1 ? '' : 's'} behind origin.`,
      )
    }
    if (dirty.known && dirty.dirty) {
      workspaceNotices.push('Shared checkout has uncommitted changes.')
    }
  }

  // A successful execution that still left its worktree dirty after a
  // dedicated cleanup turn (internal/api/execution.go's
  // buildWorkspaceCleanupPrompt) isn't auto-committed or auto-deleted —
  // dirty state after success might be scratch the agent deliberately left
  // out, unlike the failure path's presumed-interrupted work. Folded into
  // the same advisory banner as the workspace notices above, since it's the
  // same "only say something when there's something to say" posture.
  if (latestExecution?.status === 'success' && latestExecution.output.workspace_dirty) {
    workspaceNotices.push(
      `Last execution (${latestExecution.execution_id}) succeeded but left uncommitted changes in its workspace — worth a look before merging.`,
    )
  }

  return (
    <div className="task-detail">
      <button type="button" className="back-link" onClick={onBack}>
        &larr; Back to tasks
      </button>

      <div className="panel-actions">
        <h3>{task.title || task.id}</h3>
      </div>
      <p>
        {task.id} &middot; stage: {task.stage}
        {task.draft_session_id && (
          <>
            {' '}
            &middot;{' '}
            <button type="button" className="link-button" onClick={onViewDraft}>
              View pre-creation conversation
            </button>
          </>
        )}
      </p>

      {executionFailureNotice && <div className="execution-failure-banner">{executionFailureNotice}</div>}

      {workspaceNotices.length > 0 && (
        <div className="workspace-status-banner">
          {workspaceNotices.map((n) => (
            <p key={n}>{n}</p>
          ))}
        </div>
      )}

      {hasSummary && (
        <div className="task-summary">
          {(task.objective || task.constraints.length > 0 || task.assumptions.length > 0 || task.success_criteria.length > 0) && (
            <section>
              <h4>Requirements</h4>
              {task.objective && <p>{task.objective}</p>}
              {task.constraints.length > 0 && (
                <>
                  <strong>Constraints</strong>
                  <ul>
                    {task.constraints.map((c) => (
                      <li key={c}>{c}</li>
                    ))}
                  </ul>
                </>
              )}
              {task.assumptions.length > 0 && (
                <>
                  <strong>Assumptions</strong>
                  <ul>
                    {task.assumptions.map((a) => (
                      <li key={a}>{a}</li>
                    ))}
                  </ul>
                </>
              )}
              {task.success_criteria.length > 0 && (
                <>
                  <strong>Success criteria</strong>
                  <ul>
                    {task.success_criteria.map((s) => (
                      <li key={s}>{s}</li>
                    ))}
                  </ul>
                </>
              )}
            </section>
          )}

          {context && (
            <section>
              <h4>Context</h4>
              {context.summary && <p>{context.summary}</p>}
              {context.background && <p>{context.background}</p>}
            </section>
          )}

          {plan && (
            <section>
              <h4>Plan</h4>
              {plan.approach && <p>{plan.approach}</p>}
              {plan.steps.length > 0 && (
                <ol>
                  {plan.steps.map((s) => (
                    <li key={s}>{s}</li>
                  ))}
                </ol>
              )}
            </section>
          )}
        </div>
      )}

      <TimelinePanel projectId={projectId} taskId={task.id} />

      {reviseError && <p className="error">{reviseError}</p>}

      {task.stage === 'requirements' && (
        <div className="task-interview">
          <GrillMePanel
            projectId={projectId}
            taskId={task.id}
            onFinalized={(updatedTask, updatedContext) => {
              setTask(updatedTask)
              setContext(updatedContext)
            }}
          />
        </div>
      )}

      {task.stage === 'planning' && (
        <div className="task-interview">
          <PlanningModePanel
            projectId={projectId}
            taskId={task.id}
            onFinalized={(updatedTask, updatedPlan) => {
              setTask(updatedTask)
              setPlan(updatedPlan)
            }}
          />
          <div className="stage-actions">
            <textarea
              className="revise-reason-input"
              placeholder="Optional: why send this back? (shown in the timeline)"
              value={reviseReason}
              onChange={(e) => setReviseReason(e.target.value)}
              disabled={revising}
            />
            <button
              type="button"
              disabled={revising}
              onClick={() => handleRevise(() => reviseRequirements(projectId, task.id, reviseReason))}
            >
              Revise Requirements
            </button>
          </div>
        </div>
      )}

      {task.stage === 'implementation' && (
        <div className="task-interview">
          <ExecutePanel
            projectId={projectId}
            taskId={task.id}
            onExecuted={(execution) => {
              setLatestExecution(execution)
              // The "done" event only carries the Execution record, not the
              // task — a successful run advances stage server-side, so
              // reload the task to pick that up (and to no-op harmlessly on
              // a failed run, where stage is unchanged).
              getProjectTask(projectId, task.id).then(setTask).catch(() => undefined)
              // executions was last fetched when the task was opened, before
              // this run existed — re-fetch so the history list below
              // (revealed once stage leaves implementation) includes it too.
              listExecutions(projectId, task.id)
                .then((result) => setExecutions(result.executions ?? []))
                .catch(() => undefined)
            }}
          />
        </div>
      )}

      {task.stage === 'review' && (
        <div className="task-interview">
          <ReviewPanel
            projectId={projectId}
            taskId={task.id}
            // A verdict moves the task (approved→merged,
            // needs_changes→implementation, rejected→requirements);
            // re-rendering by the new stage is all the panel needs to do.
            onFinalized={(updatedTask) => setTask(updatedTask)}
          />
        </div>
      )}

      {task.stage === 'pr_review' && (
        <div className="task-interview">
          <PRReviewPanel projectId={projectId} taskId={task.id} task={task} onUpdated={(updatedTask) => setTask(updatedTask)} />
        </div>
      )}

      {(task.stage === 'implementation' || task.stage === 'review') && (
        <div className="stage-actions">
          <textarea
            className="revise-reason-input"
            placeholder="Optional: why send this back? (shown in the timeline)"
            value={reviseReason}
            onChange={(e) => setReviseReason(e.target.value)}
            disabled={revising}
          />
          <button type="button" disabled={revising} onClick={() => handleRevise(() => revisePlan(projectId, task.id, reviseReason))}>
            Revise Plan
          </button>
        </div>
      )}

      {task.stage === 'merged' && (
        <div className="task-complete">
          <h4>Review complete — {review?.decision ?? 'approved'}</h4>
          {review?.notes && <p>{review.notes}</p>}
          {task.pull_request && (
            <p>
              Merged via{' '}
              <a href={task.pull_request.url} target="_blank" rel="noopener noreferrer">
                pull request #{task.pull_request.number}
              </a>
            </p>
          )}
        </div>
      )}

      {/* ExecutePanel (stage 'implementation') already shows this same list
          itself, live-appending a just-finished run — showing it again here
          too would duplicate it. Every later stage (review, pr_review,
          merged) has no other execution-history view at all, and that's
          exactly when a human is most likely to want to check what a past
          attempt actually did. */}
      {task.stage !== 'implementation' && executions.length > 0 && (
        <div className="task-interview">
          <h4>Execution history</h4>
          <ExecutionHistoryList projectId={projectId} taskId={task.id} executions={executions} />
        </div>
      )}
    </div>
  )
}

import { useEffect, useState } from 'react'
import {
  getProjectTask,
  getTaskContext,
  getTaskPlan,
  listExecutions,
  listReviews,
  reviseRequirements,
  revisePlan,
  updateProjectTask,
} from './api'
import { ExecutePanel } from './ExecutePanel'
import { GrillMePanel } from './GrillMePanel'
import { PlanningModePanel } from './PlanningModePanel'
import { ReviewPanel } from './ReviewPanel'
import type { Review, Task, TaskContext, TaskPlan, TaskStatus } from './types'

const STATUSES: TaskStatus[] = ['draft', 'ready', 'in_progress', 'blocked', 'failed', 'complete']

interface TaskDetailPanelProps {
  projectId: string
  task: Task
  onBack: () => void
}

// TaskDetailPanel is a task's full view: read-only requirements fields
// (GrillMe-owned once past bare creation), the finalized context/plan
// artifacts, and — depending on the task's current stage — GrillMe,
// Planning Mode, or a Revise action. There is no per-task edit form beyond
// this view; TaskForm only ever creates a task (see CONTEXT.md).
export function TaskDetailPanel({ projectId, task: initialTask, onBack }: TaskDetailPanelProps) {
  const [task, setTask] = useState(initialTask)
  const [context, setContext] = useState<TaskContext | null>(null)
  const [plan, setPlan] = useState<TaskPlan | null>(null)
  const [reviseError, setReviseError] = useState<string | null>(null)
  const [revising, setRevising] = useState(false)
  // review/reviewBranch back the terminal "merged" screen: the latest
  // verdict's notes and the execution branch left for the human to merge by
  // hand. Loaded only at stage merged (see effect below), so a re-visited
  // completed task re-reads them rather than relying on in-session state.
  const [review, setReview] = useState<Review | null>(null)
  const [reviewBranch, setReviewBranch] = useState('')

  useEffect(() => {
    setContext(null)
    getTaskContext(projectId, task.id)
      .then(setContext)
      .catch(() => undefined) // 404 just means requirements haven't been finalized yet
  }, [projectId, task.id, task.stage])

  useEffect(() => {
    setPlan(null)
    getTaskPlan(projectId, task.id)
      .then(setPlan)
      .catch(() => undefined) // 404 just means planning hasn't been finalized yet
  }, [projectId, task.id, task.stage])

  useEffect(() => {
    setReview(null)
    setReviewBranch('')
    if (task.stage !== 'merged') {
      return
    }
    listReviews(projectId, task.id)
      .then((r) => {
        const list = r.reviews ?? []
        setReview(list.length > 0 ? list[list.length - 1] : null)
      })
      .catch(() => undefined) // no verdict readable — the confirmation still shows
    listExecutions(projectId, task.id)
      .then((r) => {
        const list = r.executions ?? []
        setReviewBranch(list.length > 0 ? list[list.length - 1].output.git_branch : '')
      })
      .catch(() => undefined) // no branch to surface — omit the merge hint
  }, [projectId, task.id, task.stage])

  async function handleStatusChange(status: TaskStatus) {
    const updated = await updateProjectTask(projectId, task.id, {
      title: task.title,
      status,
      stage: task.stage,
      objective: task.objective,
      constraints: task.constraints,
      assumptions: task.assumptions,
      success_criteria: task.success_criteria,
      references: task.references,
    })
    setTask(updated)
  }

  async function handleRevise(action: () => Promise<Task>) {
    if (revising) {
      return
    }
    setRevising(true)
    setReviseError(null)
    try {
      setTask(await action())
    } catch (err) {
      setReviseError(err instanceof Error ? err.message : String(err))
    } finally {
      setRevising(false)
    }
  }

  const hasSummary = Boolean(
    task.objective || task.constraints.length > 0 || task.assumptions.length > 0 || task.success_criteria.length > 0 || context || plan,
  )

  return (
    <div className="task-detail">
      <button type="button" className="back-link" onClick={onBack}>
        &larr; Back to tasks
      </button>

      <div className="panel-actions">
        <h3>{task.title || task.id}</h3>
        <select value={task.status} onChange={(e) => handleStatusChange(e.target.value as TaskStatus)}>
          {STATUSES.map((s) => (
            <option key={s} value={s}>
              {s}
            </option>
          ))}
        </select>
      </div>
      <p>
        {task.id} &middot; stage: {task.stage}
      </p>

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
            <button
              type="button"
              disabled={revising}
              onClick={() => handleRevise(() => reviseRequirements(projectId, task.id))}
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
            onExecuted={() => {
              // The "done" event only carries the Execution record, not the
              // task — a successful run advances stage server-side, so
              // reload the task to pick that up (and to no-op harmlessly on
              // a failed run, where stage is unchanged).
              getProjectTask(projectId, task.id).then(setTask).catch(() => undefined)
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

      {(task.stage === 'implementation' || task.stage === 'review') && (
        <div className="stage-actions">
          <button type="button" disabled={revising} onClick={() => handleRevise(() => revisePlan(projectId, task.id))}>
            Revise Plan
          </button>
        </div>
      )}

      {task.stage === 'merged' && (
        <div className="task-complete">
          <h4>Review complete — {review?.decision ?? 'approved'}</h4>
          {review?.notes && <p>{review.notes}</p>}
          {reviewBranch && (
            <p>
              Merge this branch by hand: <code>{reviewBranch}</code>
            </p>
          )}
        </div>
      )}
    </div>
  )
}

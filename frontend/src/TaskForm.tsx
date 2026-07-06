import { useState } from 'react'
import type { FormEvent } from 'react'
import type { CreateTaskRequest, Task, TaskStage, TaskStatus } from './types'

const STATUSES: TaskStatus[] = ['draft', 'ready', 'in_progress', 'blocked', 'failed', 'complete']
const STAGES: TaskStage[] = ['requirements', 'planning', 'implementation', 'review', 'complete']

function linesToList(value: string): string[] {
  return value
    .split('\n')
    .map((line) => line.trim())
    .filter(Boolean)
}

function listToLines(items: string[]): string {
  return items.join('\n')
}

interface TaskFormProps {
  mode: 'create' | 'edit'
  initial?: Task
  onSubmit: (req: CreateTaskRequest) => Promise<void>
  onCancel: () => void
}

export function TaskForm({ mode, initial, onSubmit, onCancel }: TaskFormProps) {
  const [id, setId] = useState(initial?.id ?? '')
  const [title, setTitle] = useState(initial?.title ?? '')
  const [status, setStatus] = useState<TaskStatus>(initial?.status ?? 'draft')
  const [stage, setStage] = useState<TaskStage>(initial?.stage ?? 'requirements')
  const [objective, setObjective] = useState(initial?.objective ?? '')
  const [constraints, setConstraints] = useState(listToLines(initial?.constraints ?? []))
  const [assumptions, setAssumptions] = useState(listToLines(initial?.assumptions ?? []))
  const [successCriteria, setSuccessCriteria] = useState(listToLines(initial?.success_criteria ?? []))
  const [refKnowledge, setRefKnowledge] = useState(listToLines(initial?.references.knowledge ?? []))
  const [refRepo, setRefRepo] = useState(listToLines(initial?.references.repo ?? []))
  const [submitting, setSubmitting] = useState(false)
  const [error, setError] = useState<string | null>(null)

  async function handleSubmit(e: FormEvent) {
    e.preventDefault()
    if (submitting) {
      return
    }

    setSubmitting(true)
    setError(null)
    try {
      await onSubmit({
        id,
        title,
        status,
        stage,
        objective,
        constraints: linesToList(constraints),
        assumptions: linesToList(assumptions),
        success_criteria: linesToList(successCriteria),
        references: {
          knowledge: linesToList(refKnowledge),
          repo: linesToList(refRepo),
        },
      })
    } catch (err) {
      setError(err instanceof Error ? err.message : String(err))
    } finally {
      setSubmitting(false)
    }
  }

  return (
    <form className="entity-form" onSubmit={handleSubmit}>
      <div className="form-row">
        <label htmlFor="task-id">ID</label>
        <input
          id="task-id"
          type="text"
          value={id}
          onChange={(e) => setId(e.target.value)}
          disabled={mode === 'edit'}
          required
        />
      </div>
      <div className="form-row">
        <label htmlFor="task-title">Title</label>
        <input id="task-title" type="text" value={title} onChange={(e) => setTitle(e.target.value)} required />
      </div>
      <div className="form-row">
        <label htmlFor="task-status">Status</label>
        <select id="task-status" value={status} onChange={(e) => setStatus(e.target.value as TaskStatus)}>
          {STATUSES.map((s) => (
            <option key={s} value={s}>
              {s}
            </option>
          ))}
        </select>
      </div>
      <div className="form-row">
        <label htmlFor="task-stage">Stage</label>
        <select id="task-stage" value={stage} onChange={(e) => setStage(e.target.value as TaskStage)}>
          {STAGES.map((s) => (
            <option key={s} value={s}>
              {s}
            </option>
          ))}
        </select>
      </div>
      <div className="form-row">
        <label htmlFor="task-objective">Objective</label>
        <textarea id="task-objective" value={objective} onChange={(e) => setObjective(e.target.value)} rows={3} />
      </div>
      <div className="form-row">
        <label htmlFor="task-constraints">Constraints (one per line)</label>
        <textarea id="task-constraints" value={constraints} onChange={(e) => setConstraints(e.target.value)} rows={3} />
      </div>
      <div className="form-row">
        <label htmlFor="task-assumptions">Assumptions (one per line)</label>
        <textarea id="task-assumptions" value={assumptions} onChange={(e) => setAssumptions(e.target.value)} rows={3} />
      </div>
      <div className="form-row">
        <label htmlFor="task-success-criteria">Success criteria (one per line)</label>
        <textarea
          id="task-success-criteria"
          value={successCriteria}
          onChange={(e) => setSuccessCriteria(e.target.value)}
          rows={3}
        />
      </div>
      <div className="form-row">
        <label htmlFor="task-ref-knowledge">Referenced knowledge (one per line)</label>
        <textarea id="task-ref-knowledge" value={refKnowledge} onChange={(e) => setRefKnowledge(e.target.value)} rows={2} />
      </div>
      <div className="form-row">
        <label htmlFor="task-ref-repo">Referenced repos (one per line)</label>
        <textarea id="task-ref-repo" value={refRepo} onChange={(e) => setRefRepo(e.target.value)} rows={2} />
      </div>
      {error && <p className="error">{error}</p>}
      <div className="form-actions">
        <button type="submit" disabled={submitting || !id.trim() || !title.trim()}>
          {submitting ? 'Saving...' : 'Save'}
        </button>
        <button type="button" onClick={onCancel} disabled={submitting}>
          Cancel
        </button>
      </div>
    </form>
  )
}

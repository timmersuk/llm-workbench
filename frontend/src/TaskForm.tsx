import { useState } from 'react'
import type { FormEvent } from 'react'
import type { CreateTaskRequest } from './types'
import { linesToList } from './listFields'

interface TaskFormProps {
  onSubmit: (req: CreateTaskRequest) => Promise<void>
  onCancel: () => void
}

// TaskForm only ever creates a task: id, title, and references. Every task
// starts at stage: requirements (server-defaulted) — its
// objective/constraints/assumptions/success_criteria are set afterward via
// GrillMe (TaskDetailPanel), not here. See CONTEXT.md's "Draft"/"GrillMe".
export function TaskForm({ onSubmit, onCancel }: TaskFormProps) {
  const [id, setId] = useState('')
  const [title, setTitle] = useState('')
  const [refKnowledge, setRefKnowledge] = useState('')
  const [refRepo, setRefRepo] = useState('')
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
        <input id="task-id" type="text" value={id} onChange={(e) => setId(e.target.value)} required />
      </div>
      <div className="form-row">
        <label htmlFor="task-title">Title</label>
        <input id="task-title" type="text" value={title} onChange={(e) => setTitle(e.target.value)} required />
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

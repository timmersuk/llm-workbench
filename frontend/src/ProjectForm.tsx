import { useState } from 'react'
import type { FormEvent } from 'react'
import type { CreateProjectRequest, Project } from './types'

function linesToList(value: string): string[] {
  return value
    .split('\n')
    .map((line) => line.trim())
    .filter(Boolean)
}

function listToLines(items: string[]): string {
  return items.join('\n')
}

interface ProjectFormProps {
  initial?: Project
  onSubmit: (req: CreateProjectRequest) => Promise<void>
  onCancel: () => void
}

export function ProjectForm({ initial, onSubmit, onCancel }: ProjectFormProps) {
  const [name, setName] = useState(initial?.name ?? '')
  const [description, setDescription] = useState(initial?.description ?? '')
  const [repositories, setRepositories] = useState(listToLines(initial?.repositories ?? []))
  const [knowledge, setKnowledge] = useState(listToLines(initial?.knowledge ?? []))
  const [constraints, setConstraints] = useState(listToLines(initial?.constraints ?? []))
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
        name,
        description,
        repositories: linesToList(repositories),
        knowledge: linesToList(knowledge),
        constraints: linesToList(constraints),
      })
    } catch (err) {
      setError(err instanceof Error ? err.message : String(err))
    } finally {
      setSubmitting(false)
    }
  }

  return (
    <form className="entity-form" onSubmit={handleSubmit}>
      {initial && (
        <div className="form-row">
          <label htmlFor="project-id">ID</label>
          <input id="project-id" type="text" value={initial.id} disabled />
        </div>
      )}
      <div className="form-row">
        <label htmlFor="project-name">Name</label>
        <input id="project-name" type="text" value={name} onChange={(e) => setName(e.target.value)} required />
      </div>
      <div className="form-row">
        <label htmlFor="project-description">Description</label>
        <textarea id="project-description" value={description} onChange={(e) => setDescription(e.target.value)} rows={3} />
      </div>
      <div className="form-row">
        <label htmlFor="project-repositories">Repositories (one per line)</label>
        <textarea id="project-repositories" value={repositories} onChange={(e) => setRepositories(e.target.value)} rows={3} />
      </div>
      <div className="form-row">
        <label htmlFor="project-knowledge">Knowledge (one per line)</label>
        <textarea id="project-knowledge" value={knowledge} onChange={(e) => setKnowledge(e.target.value)} rows={3} />
      </div>
      <div className="form-row">
        <label htmlFor="project-constraints">Constraints (one per line)</label>
        <textarea id="project-constraints" value={constraints} onChange={(e) => setConstraints(e.target.value)} rows={3} />
      </div>
      {error && <p className="error">{error}</p>}
      <div className="form-actions">
        <button type="submit" disabled={submitting || !name.trim()}>
          {submitting ? 'Saving...' : 'Save'}
        </button>
        <button type="button" onClick={onCancel} disabled={submitting}>
          Cancel
        </button>
      </div>
    </form>
  )
}

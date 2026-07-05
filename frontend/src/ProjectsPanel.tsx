import { useEffect, useState } from 'react'
import { listProjects } from './api'
import type { LoadError, Project } from './types'

export function ProjectsPanel() {
  const [projects, setProjects] = useState<Project[]>([])
  const [loadErrors, setLoadErrors] = useState<LoadError[]>([])
  const [error, setError] = useState<string | null>(null)

  useEffect(() => {
    listProjects()
      .then((result) => {
        setProjects(result.projects ?? [])
        setLoadErrors(result.errors ?? [])
      })
      .catch((err: Error) => setError(err.message))
  }, [])

  if (error) {
    return <p className="error">Failed to load projects: {error}</p>
  }

  const loadErrorNotice = loadErrors.length > 0 && (
    <p className="error">
      {loadErrors.length} project{loadErrors.length > 1 ? 's' : ''} failed to load:{' '}
      {loadErrors.map((e) => e.id).join(', ')}
    </p>
  )

  if (projects.length === 0) {
    return (
      <>
        {loadErrorNotice}
        <p>No projects yet.</p>
      </>
    )
  }

  return (
    <>
      {loadErrorNotice}
      <table>
        <thead>
          <tr>
            <th>ID</th>
            <th>Name</th>
            <th>Description</th>
            <th>Repositories</th>
          </tr>
        </thead>
        <tbody>
          {projects.map((project) => (
            <tr key={project.id}>
              <td>{project.id}</td>
              <td>{project.name}</td>
              <td>{project.description}</td>
              <td>{project.repositories.join(', ')}</td>
            </tr>
          ))}
        </tbody>
      </table>
    </>
  )
}

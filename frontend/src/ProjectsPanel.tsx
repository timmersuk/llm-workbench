import { useEffect, useState } from 'react'
import { listProjects } from './api'
import type { Project } from './types'

export function ProjectsPanel() {
  const [projects, setProjects] = useState<Project[]>([])
  const [error, setError] = useState<string | null>(null)

  useEffect(() => {
    listProjects()
      .then(setProjects)
      .catch((err: Error) => setError(err.message))
  }, [])

  if (error) {
    return <p className="error">Failed to load projects: {error}</p>
  }

  if (projects.length === 0) {
    return <p>No projects yet.</p>
  }

  return (
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
  )
}

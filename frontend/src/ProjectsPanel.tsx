import { useEffect, useState } from 'react'
import { createProject, listProjects } from './api'
import { ProjectDetailPanel } from './ProjectDetailPanel'
import { ProjectForm } from './ProjectForm'
import type { CreateProjectRequest, LoadError, Project } from './types'

export function ProjectsPanel() {
  const [projects, setProjects] = useState<Project[]>([])
  const [loadErrors, setLoadErrors] = useState<LoadError[]>([])
  const [error, setError] = useState<string | null>(null)
  const [creating, setCreating] = useState(false)
  const [selectedProject, setSelectedProject] = useState<Project | null>(null)

  function reload() {
    listProjects()
      .then((result) => {
        setProjects(result.projects ?? [])
        setLoadErrors(result.errors ?? [])
      })
      .catch((err: Error) => setError(err.message))
  }

  useEffect(reload, [])

  async function handleCreate(req: CreateProjectRequest) {
    await createProject(req)
    setCreating(false)
    reload()
  }

  if (selectedProject) {
    return <ProjectDetailPanel project={selectedProject} onBack={() => setSelectedProject(null)} />
  }

  if (error) {
    return <p className="error">Failed to load projects: {error}</p>
  }

  const loadErrorNotice = loadErrors.length > 0 && (
    <p className="error">
      {loadErrors.length} project{loadErrors.length > 1 ? 's' : ''} failed to load:{' '}
      {loadErrors.map((e) => e.id).join(', ')}
    </p>
  )

  return (
    <>
      <div className="panel-actions">
        <h2>Projects</h2>
        {!creating && (
          <button type="button" onClick={() => setCreating(true)}>
            New Project
          </button>
        )}
      </div>

      {creating && <ProjectForm onSubmit={handleCreate} onCancel={() => setCreating(false)} />}

      {loadErrorNotice}

      {projects.length === 0 ? (
        <p>No projects yet.</p>
      ) : (
        <table>
          <thead>
            <tr>
              <th>ID</th>
              <th>Name</th>
              <th>Description</th>
              <th>Repositories</th>
              <th></th>
            </tr>
          </thead>
          <tbody>
            {projects.map((project) => (
              <tr key={project.id}>
                <td>{project.id}</td>
                <td>{project.name}</td>
                <td>{project.description}</td>
                <td>{project.repositories.join(', ')}</td>
                <td>
                  <button type="button" onClick={() => setSelectedProject(project)}>
                    View tasks
                  </button>
                </td>
              </tr>
            ))}
          </tbody>
        </table>
      )}
    </>
  )
}

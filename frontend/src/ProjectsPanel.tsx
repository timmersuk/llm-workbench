import { useEffect, useState } from 'react'
import { createProject, getProject, listProjects } from './api'
import { ProjectDetailPanel } from './ProjectDetailPanel'
import { ProjectForm } from './ProjectForm'
import type { CreateProjectRequest, LoadError, Project } from './types'
import type { Route } from './url'

interface ProjectsPanelProps {
  // selectedProjectId is controlled by App, driven off the URL — see
  // frontend/src/url.ts. Undefined means "show the list".
  selectedProjectId?: string
  selectedTaskId?: string
  selectedTaskView?: Route['taskView']
  selectedNewTaskSessionId?: string
  onSelectProject: (id: string) => void
  onBackToProjects: () => void
  onInvalidProject: () => void
  onSelectTask: (id: string) => void
  onBackToProject: () => void
  onInvalidTask: () => void
  onNewTask: (sessionId: string) => void
  onViewTaskDraft: () => void
  onBackFromTaskDraft: () => void
}

export function ProjectsPanel({
  selectedProjectId,
  selectedTaskId,
  selectedTaskView,
  selectedNewTaskSessionId,
  onSelectProject,
  onBackToProjects,
  onInvalidProject,
  onSelectTask,
  onBackToProject,
  onInvalidTask,
  onNewTask,
  onViewTaskDraft,
  onBackFromTaskDraft,
}: ProjectsPanelProps) {
  const [projects, setProjects] = useState<Project[]>([])
  const [loadErrors, setLoadErrors] = useState<LoadError[]>([])
  const [error, setError] = useState<string | null>(null)
  const [creating, setCreating] = useState(false)
  // listLoaded tracks whether the initial listProjects call has settled
  // (either way) — distinct from the id-resolution below, which stays a
  // plain string|undefined with no tri-state of its own. Without this, a
  // selectedProjectId supplied before the list has loaded (deep link /
  // reload) would race a wasted getProject call against the list arriving
  // moments later with the same id already in it.
  const [listLoaded, setListLoaded] = useState(false)
  // resolvedProject backs the deep-link/reload case: selectedProjectId came
  // from the URL and isn't (yet) in the loaded projects list below, so it
  // was fetched directly via getProject.
  const [resolvedProject, setResolvedProject] = useState<Project | null>(null)

  function reload() {
    listProjects()
      .then((result) => {
        setProjects(result.projects ?? [])
        setLoadErrors(result.errors ?? [])
      })
      .catch((err: Error) => setError(err.message))
      .finally(() => setListLoaded(true))
  }

  useEffect(reload, [])

  // Resolve selectedProjectId against the loaded list first (the normal
  // click-through case — no network call); only falls back to getProject
  // for a project not present in the current list, once the list has
  // settled (deep link / reload). onInvalidProject fires once resolution
  // definitively fails, letting App collapse the URL down to /projects via
  // replaceState.
  useEffect(() => {
    if (!selectedProjectId) {
      setResolvedProject(null)
      return
    }
    const fromList = projects.find((p) => p.id === selectedProjectId)
    if (fromList) {
      setResolvedProject(fromList)
      return
    }
    if (!listLoaded) {
      return
    }
    let cancelled = false
    getProject(selectedProjectId)
      .then((project) => {
        if (!cancelled) {
          setResolvedProject(project)
        }
      })
      .catch(() => {
        if (!cancelled) {
          onInvalidProject()
        }
      })
    return () => {
      cancelled = true
    }
    // eslint-disable-next-line react-hooks/exhaustive-deps
  }, [selectedProjectId, projects, listLoaded])

  async function handleCreate(req: CreateProjectRequest) {
    await createProject(req)
    setCreating(false)
    reload()
  }

  if (selectedProjectId) {
    if (!resolvedProject) {
      return null
    }
    return (
      <ProjectDetailPanel
        project={resolvedProject}
        onBack={onBackToProjects}
        selectedTaskId={selectedTaskId}
        selectedTaskView={selectedTaskView}
        selectedNewTaskSessionId={selectedNewTaskSessionId}
        onSelectTask={onSelectTask}
        onBackToProject={onBackToProject}
        onInvalidTask={onInvalidTask}
        onNewTask={onNewTask}
        onViewTaskDraft={onViewTaskDraft}
        onBackFromTaskDraft={onBackFromTaskDraft}
      />
    )
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
                  <button type="button" onClick={() => onSelectProject(project.id)}>
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

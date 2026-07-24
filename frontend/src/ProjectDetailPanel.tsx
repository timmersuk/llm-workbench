import { useEffect, useState } from 'react'
import { createProjectTask, getProjectTask, listProjectTasks, updateProject } from './api'
import { ProjectForm } from './ProjectForm'
import { TaskForm } from './TaskForm'
import { TaskDetailPanel } from './TaskDetailPanel'
import { TaskKanbanBoard } from './TaskKanbanBoard'
import type { CreateTaskRequest, LoadError, Project, Task, UpdateProjectRequest } from './types'

interface ProjectDetailPanelProps {
  project: Project
  onBack: () => void
  // selectedTaskId is controlled by App, driven off the URL — see
  // frontend/src/url.ts. Undefined means "show the task list".
  selectedTaskId?: string
  onSelectTask: (id: string) => void
  onBackToProject: () => void
  onInvalidTask: () => void
}

export function ProjectDetailPanel({
  project,
  onBack,
  selectedTaskId,
  onSelectTask,
  onBackToProject,
  onInvalidTask,
}: ProjectDetailPanelProps) {
  const [current, setCurrent] = useState(project)
  const [editingProject, setEditingProject] = useState(false)

  const [tasks, setTasks] = useState<Task[]>([])
  const [loadErrors, setLoadErrors] = useState<LoadError[]>([])
  const [tasksError, setTasksError] = useState<string | null>(null)
  const [creatingTask, setCreatingTask] = useState(false)
  // tasksLoaded tracks whether the initial listProjectTasks call has
  // settled (either way) — distinct from the id-resolution below, which
  // stays a plain string|undefined with no tri-state of its own. Without
  // this, a selectedTaskId supplied before the list has loaded (deep link /
  // reload) would race a wasted getProjectTask call against the list
  // arriving moments later with the same id already in it.
  const [tasksLoaded, setTasksLoaded] = useState(false)
  // resolvedTask backs the deep-link/reload case: selectedTaskId came from
  // the URL and isn't (yet) in the loaded tasks list below, so it was
  // fetched directly via getProjectTask.
  const [resolvedTask, setResolvedTask] = useState<Task | null>(null)

  function reloadTasks() {
    listProjectTasks(current.id)
      .then((result) => {
        setTasks(result.tasks ?? [])
        setLoadErrors(result.errors ?? [])
      })
      .catch((err: Error) => setTasksError(err.message))
      .finally(() => setTasksLoaded(true))
  }

  useEffect(() => {
    reloadTasks()
    // eslint-disable-next-line react-hooks/exhaustive-deps
  }, [current.id])

  // Resolve selectedTaskId against the loaded list first (no network call
  // on a normal click-through); falls back to getProjectTask for a task not
  // present in the current list, once the list has settled (deep link /
  // reload). onInvalidTask fires once resolution definitively fails,
  // letting App collapse the URL down to /projects/:projectId via
  // replaceState.
  useEffect(() => {
    if (!selectedTaskId) {
      setResolvedTask(null)
      return
    }
    const fromList = tasks.find((t) => t.id === selectedTaskId)
    if (fromList) {
      setResolvedTask(fromList)
      return
    }
    if (!tasksLoaded) {
      return
    }
    let cancelled = false
    getProjectTask(current.id, selectedTaskId)
      .then((task) => {
        if (!cancelled) {
          setResolvedTask(task)
        }
      })
      .catch(() => {
        if (!cancelled) {
          onInvalidTask()
        }
      })
    return () => {
      cancelled = true
    }
    // eslint-disable-next-line react-hooks/exhaustive-deps
  }, [selectedTaskId, tasks, tasksLoaded, current.id])

  async function handleProjectSave(req: UpdateProjectRequest) {
    const updated = await updateProject(current.id, req)
    setCurrent(updated)
    setEditingProject(false)
  }

  async function handleTaskCreate(req: CreateTaskRequest) {
    await createProjectTask(current.id, req)
    setCreatingTask(false)
    reloadTasks()
  }

  const loadErrorNotice = loadErrors.length > 0 && (
    <p className="error">
      {loadErrors.length} task{loadErrors.length > 1 ? 's' : ''} failed to load:{' '}
      {loadErrors.map((e) => e.id).join(', ')}
    </p>
  )

  if (selectedTaskId) {
    if (!resolvedTask) {
      return null
    }
    return (
      <TaskDetailPanel
        projectId={current.id}
        task={resolvedTask}
        onBack={() => {
          onBackToProject()
          reloadTasks()
        }}
      />
    )
  }

  return (
    <div className="project-detail">
      <button type="button" className="back-link" onClick={onBack}>
        &larr; Back to projects
      </button>

      <section>
        <div className="panel-actions">
          <h2>{current.name}</h2>
          {!editingProject && (
            <button type="button" onClick={() => setEditingProject(true)}>
              Edit project
            </button>
          )}
        </div>
        {editingProject ? (
          <ProjectForm initial={current} onSubmit={handleProjectSave} onCancel={() => setEditingProject(false)} />
        ) : (
          <p>{current.description || 'No description.'}</p>
        )}
      </section>

      <section>
        <div className="panel-actions">
          <h3>Tasks</h3>
          {!creatingTask && (
            <button type="button" onClick={() => setCreatingTask(true)}>
              New Task
            </button>
          )}
        </div>

        {creatingTask && <TaskForm onSubmit={handleTaskCreate} onCancel={() => setCreatingTask(false)} />}

        {tasksError && <p className="error">Failed to load tasks: {tasksError}</p>}
        {loadErrorNotice}

        {!tasksError && tasks.length === 0 && <p>No tasks yet.</p>}

        {!tasksError && tasks.length > 0 && (
          <TaskKanbanBoard tasks={tasks} onSelect={(task) => onSelectTask(task.id)} />
        )}
      </section>
    </div>
  )
}

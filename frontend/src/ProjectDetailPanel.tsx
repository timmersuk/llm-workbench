import { useEffect, useState } from 'react'
import { createProjectTask, listProjectTasks, updateProject, updateProjectTask } from './api'
import { ProjectForm } from './ProjectForm'
import { TaskForm } from './TaskForm'
import type { CreateTaskRequest, LoadError, Project, Task, UpdateProjectRequest } from './types'

interface ProjectDetailPanelProps {
  project: Project
  onBack: () => void
}

type TaskFormMode = { kind: 'create' } | { kind: 'edit'; task: Task }

export function ProjectDetailPanel({ project, onBack }: ProjectDetailPanelProps) {
  const [current, setCurrent] = useState(project)
  const [editingProject, setEditingProject] = useState(false)

  const [tasks, setTasks] = useState<Task[]>([])
  const [loadErrors, setLoadErrors] = useState<LoadError[]>([])
  const [tasksError, setTasksError] = useState<string | null>(null)
  const [taskForm, setTaskForm] = useState<TaskFormMode | null>(null)

  function reloadTasks() {
    listProjectTasks(current.id)
      .then((result) => {
        setTasks(result.tasks ?? [])
        setLoadErrors(result.errors ?? [])
      })
      .catch((err: Error) => setTasksError(err.message))
  }

  useEffect(() => {
    reloadTasks()
    // eslint-disable-next-line react-hooks/exhaustive-deps
  }, [current.id])

  async function handleProjectSave(req: UpdateProjectRequest) {
    const updated = await updateProject(current.id, req)
    setCurrent(updated)
    setEditingProject(false)
  }

  async function handleTaskSave(req: CreateTaskRequest) {
    if (taskForm?.kind === 'edit') {
      const { id: _id, ...updateReq } = req
      await updateProjectTask(current.id, taskForm.task.id, updateReq)
    } else {
      await createProjectTask(current.id, req)
    }
    setTaskForm(null)
    reloadTasks()
  }

  const loadErrorNotice = loadErrors.length > 0 && (
    <p className="error">
      {loadErrors.length} task{loadErrors.length > 1 ? 's' : ''} failed to load:{' '}
      {loadErrors.map((e) => e.id).join(', ')}
    </p>
  )

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
          {!taskForm && (
            <button type="button" onClick={() => setTaskForm({ kind: 'create' })}>
              New Task
            </button>
          )}
        </div>

        {taskForm && (
          <TaskForm
            mode={taskForm.kind}
            initial={taskForm.kind === 'edit' ? taskForm.task : undefined}
            onSubmit={handleTaskSave}
            onCancel={() => setTaskForm(null)}
          />
        )}

        {tasksError && <p className="error">Failed to load tasks: {tasksError}</p>}
        {loadErrorNotice}

        {!tasksError && tasks.length === 0 && <p>No tasks yet.</p>}

        {!tasksError && tasks.length > 0 && (
          <table>
            <thead>
              <tr>
                <th>ID</th>
                <th>Title</th>
                <th>Status</th>
                <th>Stage</th>
                <th></th>
              </tr>
            </thead>
            <tbody>
              {tasks.map((task) => (
                <tr key={task.id}>
                  <td>{task.id}</td>
                  <td>{task.title}</td>
                  <td>{task.status}</td>
                  <td>{task.stage}</td>
                  <td>
                    <button type="button" onClick={() => setTaskForm({ kind: 'edit', task })}>
                      Edit
                    </button>
                  </td>
                </tr>
              ))}
            </tbody>
          </table>
        )}
      </section>
    </div>
  )
}

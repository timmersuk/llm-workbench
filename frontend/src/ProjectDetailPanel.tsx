import { useEffect, useState } from 'react'
import { createProjectTask, listProjectTasks, updateProject } from './api'
import { ProjectForm } from './ProjectForm'
import { TaskForm } from './TaskForm'
import { TaskDetailPanel } from './TaskDetailPanel'
import { TaskKanbanBoard } from './TaskKanbanBoard'
import type { CreateTaskRequest, LoadError, Project, Task, UpdateProjectRequest } from './types'

interface ProjectDetailPanelProps {
  project: Project
  onBack: () => void
}

type TaskView = 'list' | 'kanban'

export function ProjectDetailPanel({ project, onBack }: ProjectDetailPanelProps) {
  const [current, setCurrent] = useState(project)
  const [editingProject, setEditingProject] = useState(false)

  const [tasks, setTasks] = useState<Task[]>([])
  const [loadErrors, setLoadErrors] = useState<LoadError[]>([])
  const [tasksError, setTasksError] = useState<string | null>(null)
  const [creatingTask, setCreatingTask] = useState(false)
  const [taskView, setTaskView] = useState<TaskView>('list')
  const [selectedTask, setSelectedTask] = useState<Task | null>(null)

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

  if (selectedTask) {
    return (
      <TaskDetailPanel
        projectId={current.id}
        task={selectedTask}
        onBack={() => {
          setSelectedTask(null)
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
          <div className="task-view-toggle">
            <button type="button" className={taskView === 'list' ? 'active' : ''} onClick={() => setTaskView('list')}>
              List
            </button>
            <button type="button" className={taskView === 'kanban' ? 'active' : ''} onClick={() => setTaskView('kanban')}>
              Kanban
            </button>
          </div>
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

        {!tasksError && tasks.length > 0 && taskView === 'kanban' && (
          <TaskKanbanBoard tasks={tasks} onSelect={setSelectedTask} />
        )}

        {!tasksError && tasks.length > 0 && taskView === 'list' && (
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
                    <button type="button" onClick={() => setSelectedTask(task)}>
                      Open
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

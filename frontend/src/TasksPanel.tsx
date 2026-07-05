import { useEffect, useState } from 'react'
import { listTasks } from './api'
import type { LoadError, Task } from './types'

export function TasksPanel() {
  const [tasks, setTasks] = useState<Task[]>([])
  const [loadErrors, setLoadErrors] = useState<LoadError[]>([])
  const [error, setError] = useState<string | null>(null)

  useEffect(() => {
    listTasks()
      .then((result) => {
        setTasks(result.tasks ?? [])
        setLoadErrors(result.errors ?? [])
      })
      .catch((err: Error) => setError(err.message))
  }, [])

  if (error) {
    return <p className="error">Failed to load tasks: {error}</p>
  }

  const loadErrorNotice = loadErrors.length > 0 && (
    <p className="error">
      {loadErrors.length} task{loadErrors.length > 1 ? 's' : ''} failed to load:{' '}
      {loadErrors.map((e) => e.id).join(', ')}
    </p>
  )

  if (tasks.length === 0) {
    return (
      <>
        {loadErrorNotice}
        <p>No tasks yet.</p>
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
            <th>Title</th>
            <th>Project</th>
            <th>Status</th>
            <th>Stage</th>
          </tr>
        </thead>
        <tbody>
          {tasks.map((task) => (
            <tr key={task.id}>
              <td>{task.id}</td>
              <td>{task.title}</td>
              <td>{task.project}</td>
              <td>{task.status}</td>
              <td>{task.stage}</td>
            </tr>
          ))}
        </tbody>
      </table>
    </>
  )
}

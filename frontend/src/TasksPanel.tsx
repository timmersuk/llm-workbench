import { useEffect, useState } from 'react'
import { listTasks } from './api'
import type { Task } from './types'

export function TasksPanel() {
  const [tasks, setTasks] = useState<Task[]>([])
  const [error, setError] = useState<string | null>(null)

  useEffect(() => {
    listTasks()
      .then(setTasks)
      .catch((err: Error) => setError(err.message))
  }, [])

  if (error) {
    return <p className="error">Failed to load tasks: {error}</p>
  }

  if (tasks.length === 0) {
    return <p>No tasks yet.</p>
  }

  return (
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
  )
}

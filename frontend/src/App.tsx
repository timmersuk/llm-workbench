import { useEffect, useState } from 'react'
import { getHealthStatus } from './api'
import { TasksPanel } from './TasksPanel'
import { ProjectsPanel } from './ProjectsPanel'
import { ChatPanel } from './ChatPanel'

type Tab = 'tasks' | 'projects' | 'chat'

const TABS: { id: Tab; label: string }[] = [
  { id: 'tasks', label: 'Tasks' },
  { id: 'projects', label: 'Projects' },
  { id: 'chat', label: 'Chat' },
]

function App() {
  const [tab, setTab] = useState<Tab>('tasks')
  const [buildId, setBuildId] = useState('dev')

  useEffect(() => {
    getHealthStatus()
      .then((status) => setBuildId(status.build_id || 'dev'))
      .catch(() => setBuildId('dev'))
  }, [])

  return (
    <div className="app">
      <header>
        <div className="header-row">
          <h1>LLM Workbench</h1>
          <span className="build-badge">build {buildId}</span>
        </div>
        <nav>
          {TABS.map(({ id, label }) => (
            <button
              key={id}
              className={tab === id ? 'active' : ''}
              onClick={() => setTab(id)}
            >
              {label}
            </button>
          ))}
        </nav>
      </header>
      <main>
        {tab === 'tasks' && <TasksPanel />}
        {tab === 'projects' && <ProjectsPanel />}
        {tab === 'chat' && <ChatPanel />}
      </main>
    </div>
  )
}

export default App

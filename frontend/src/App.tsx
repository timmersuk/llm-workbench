import { useState } from 'react'
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

  return (
    <div className="app">
      <header>
        <h1>LLM Workbench</h1>
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

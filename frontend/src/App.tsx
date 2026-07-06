import { useEffect, useState } from 'react'
import { getHealthStatus } from './api'
import { ProjectsPanel } from './ProjectsPanel'
import { ChatPanel } from './ChatPanel'

type Tab = 'projects' | 'chat'

const TABS: { id: Tab; label: string }[] = [
  { id: 'projects', label: 'Projects' },
  { id: 'chat', label: 'Chat' },
]

function App() {
  const [tab, setTab] = useState<Tab>('projects')
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
        {tab === 'projects' && <ProjectsPanel />}
        {tab === 'chat' && <ChatPanel />}
      </main>
    </div>
  )
}

export default App

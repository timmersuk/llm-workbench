import { useEffect, useState } from 'react'
import { getHealthStatus } from './api'
import { ProjectsPanel } from './ProjectsPanel'
import { ChatPanel } from './ChatPanel'
import { ThemeToggle } from './ThemeToggle'
import { parsePath, routeToPath } from './url'
import type { Route, Tab } from './url'

const TABS: { id: Tab; label: string }[] = [
  { id: 'projects', label: 'Projects' },
  { id: 'chat', label: 'Chat' },
]

// initialRoute/initialCorrection are computed once, outside the component,
// so the very first render already reflects the URL App was loaded with —
// no flash of the default /projects view before an effect runs.
function computeInitialRoute(pathname: string): { route: Route; needsCorrection: boolean } {
  const route = parsePath(pathname)
  return { route, needsCorrection: routeToPath(route) !== pathname }
}

function App() {
  const [{ route, needsCorrection }, setRouteState] = useState(() =>
    computeInitialRoute(window.location.pathname),
  )
  const [buildId, setBuildId] = useState('dev')

  useEffect(() => {
    getHealthStatus()
      .then((status) => setBuildId(status.build_id || 'dev'))
      .catch(() => setBuildId('dev'))
  }, [])

  // The bare "/" path and any unrecognized path collapse to the default
  // /projects route (parsePath); if that's what happened, silently correct
  // the address bar via replaceState so it doesn't create a bogus back-
  // button entry. This only needs to run once, right after mount.
  useEffect(() => {
    if (needsCorrection) {
      window.history.replaceState(null, '', routeToPath(route))
    }
    // eslint-disable-next-line react-hooks/exhaustive-deps
  }, [])

  // Browser back/forward: re-parse whatever path the user navigated to and
  // adopt it as the new route, with no push/replace of our own — the
  // history entry already exists.
  useEffect(() => {
    function handlePopState() {
      setRouteState({ route: parsePath(window.location.pathname), needsCorrection: false })
    }
    window.addEventListener('popstate', handlePopState)
    return () => window.removeEventListener('popstate', handlePopState)
  }, [])

  function navigate(next: Route) {
    window.history.pushState(null, '', routeToPath(next))
    setRouteState({ route: next, needsCorrection: false })
  }

  function correctTo(next: Route) {
    window.history.replaceState(null, '', routeToPath(next))
    setRouteState({ route: next, needsCorrection: false })
  }

  function handleTabChange(tab: Tab) {
    navigate(tab === 'chat' ? { tab: 'chat' } : { tab: 'projects' })
  }

  return (
    <div className="app">
      <header>
        <div className="header-row">
          <h1>LLM Workbench</h1>
          <div className="header-controls">
            <span className="build-badge">build {buildId}</span>
            <ThemeToggle />
          </div>
        </div>
        <nav>
          {TABS.map(({ id, label }) => (
            <button
              key={id}
              className={route.tab === id ? 'active' : ''}
              onClick={() => handleTabChange(id)}
            >
              {label}
            </button>
          ))}
        </nav>
      </header>
      <main>
        {route.tab === 'projects' && (
          <ProjectsPanel
            selectedProjectId={route.projectId}
            selectedTaskId={route.taskId}
            selectedTaskView={route.taskView}
            selectedNewTaskSessionId={route.newTaskSessionId}
            onSelectProject={(id) => navigate({ tab: 'projects', projectId: id })}
            onBackToProjects={() => navigate({ tab: 'projects' })}
            onInvalidProject={() => correctTo({ tab: 'projects' })}
            onSelectTask={(id) => navigate({ tab: 'projects', projectId: route.projectId, taskId: id })}
            onBackToProject={() => navigate({ tab: 'projects', projectId: route.projectId })}
            onInvalidTask={() => correctTo({ tab: 'projects', projectId: route.projectId })}
            onNewTask={(sessionId) => navigate({ tab: 'projects', projectId: route.projectId, newTaskSessionId: sessionId })}
            onViewTaskDraft={() => navigate({ tab: 'projects', projectId: route.projectId, taskId: route.taskId, taskView: 'draft' })}
            onBackFromTaskDraft={() => navigate({ tab: 'projects', projectId: route.projectId, taskId: route.taskId })}
          />
        )}
        {route.tab === 'chat' && <ChatPanel />}
      </main>
    </div>
  )
}

export default App

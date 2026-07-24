// url.ts is the whole of this app's URL<->state sync layer: a hand-rolled
// parse/serialize pair, deliberately not a routing library (see docs/
// engineering conventions.md — the tab/page set here is small enough not to
// justify one). App.tsx owns all navigation history (pushState/replaceState/
// popstate); this module is pure and has no browser dependency, so it's
// trivially unit-testable without jsdom location/history plumbing.

export type Tab = 'projects' | 'chat'

export interface Route {
  tab: Tab
  projectId?: string
  taskId?: string
}

// parsePath maps a location.pathname to a Route. Only four shapes are
// recognized: /chat, /projects, /projects/:projectId, and
// /projects/:projectId/tasks/:taskId. Anything else — including the bare
// "/" root and any malformed/unrecognized path — falls back to the default
// { tab: 'projects' } route with nothing selected; the caller (App) is
// responsible for correcting the address bar via replaceState when the
// input didn't already match one of the canonical shapes.
export function parsePath(pathname: string): Route {
  const segments = pathname.split('/').filter((segment) => segment.length > 0)

  if (segments.length === 1 && segments[0] === 'chat') {
    return { tab: 'chat' }
  }

  if (segments.length === 1 && segments[0] === 'projects') {
    return { tab: 'projects' }
  }

  if (segments.length === 2 && segments[0] === 'projects') {
    return { tab: 'projects', projectId: decodeURIComponent(segments[1]) }
  }

  if (segments.length === 4 && segments[0] === 'projects' && segments[2] === 'tasks') {
    return {
      tab: 'projects',
      projectId: decodeURIComponent(segments[1]),
      taskId: decodeURIComponent(segments[3]),
    }
  }

  return { tab: 'projects' }
}

// routeToPath is parsePath's inverse: it serializes a Route back to the
// canonical path App pushes/replaces into browser history.
export function routeToPath(route: Route): string {
  if (route.tab === 'chat') {
    return '/chat'
  }

  if (route.projectId && route.taskId) {
    return `/projects/${encodeURIComponent(route.projectId)}/tasks/${encodeURIComponent(route.taskId)}`
  }

  if (route.projectId) {
    return `/projects/${encodeURIComponent(route.projectId)}`
  }

  return '/projects'
}

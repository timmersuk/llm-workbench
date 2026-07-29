import { describe, expect, it } from 'vitest'
import { parsePath, routeToPath } from './url'

describe('parsePath', () => {
  it('parses /chat', () => {
    expect(parsePath('/chat')).toEqual({ tab: 'chat' })
  })

  it('parses /projects', () => {
    expect(parsePath('/projects')).toEqual({ tab: 'projects' })
  })

  it('parses /projects/:projectId', () => {
    expect(parsePath('/projects/demo')).toEqual({ tab: 'projects', projectId: 'demo' })
  })

  it('parses /projects/:projectId/tasks/:taskId', () => {
    expect(parsePath('/projects/demo/tasks/task-a')).toEqual({
      tab: 'projects',
      projectId: 'demo',
      taskId: 'task-a',
    })
  })

  it('decodes URI-encoded ids', () => {
    expect(parsePath('/projects/demo%20project/tasks/task%20one')).toEqual({
      tab: 'projects',
      projectId: 'demo project',
      taskId: 'task one',
    })
  })

  it('defaults the bare root path to projects with nothing selected', () => {
    expect(parsePath('/')).toEqual({ tab: 'projects' })
  })

  it('defaults an unrecognized path to projects with nothing selected', () => {
    expect(parsePath('/something/else/entirely')).toEqual({ tab: 'projects' })
  })

  it('defaults a malformed projects path (trailing tasks segment with no id) to projects', () => {
    expect(parsePath('/projects/demo/tasks')).toEqual({ tab: 'projects' })
  })

  it('defaults an empty path to projects', () => {
    expect(parsePath('')).toEqual({ tab: 'projects' })
  })

  it('parses /projects/:projectId/new-task/:sessionId', () => {
    expect(parsePath('/projects/demo/new-task/session-123')).toEqual({
      tab: 'projects',
      projectId: 'demo',
      newTaskSessionId: 'session-123',
    })
  })

  it('decodes URI-encoded ids in the new-task path', () => {
    expect(parsePath('/projects/demo%20project/new-task/session%20one')).toEqual({
      tab: 'projects',
      projectId: 'demo project',
      newTaskSessionId: 'session one',
    })
  })

  it('parses /projects/:projectId/tasks/:taskId/draft', () => {
    expect(parsePath('/projects/demo/tasks/task-a/draft')).toEqual({
      tab: 'projects',
      projectId: 'demo',
      taskId: 'task-a',
      taskView: 'draft',
    })
  })

  it('does not confuse a new-task path with a tasks path (same segment count, differing at index 2)', () => {
    const newTask = parsePath('/projects/demo/new-task/session-123')
    expect(newTask.taskId).toBeUndefined()
    const task = parsePath('/projects/demo/tasks/task-a')
    expect(task.newTaskSessionId).toBeUndefined()
  })
})

describe('routeToPath', () => {
  it('serializes the chat tab', () => {
    expect(routeToPath({ tab: 'chat' })).toBe('/chat')
  })

  it('serializes projects with nothing selected', () => {
    expect(routeToPath({ tab: 'projects' })).toBe('/projects')
  })

  it('serializes a selected project', () => {
    expect(routeToPath({ tab: 'projects', projectId: 'demo' })).toBe('/projects/demo')
  })

  it('serializes a selected project and task', () => {
    expect(routeToPath({ tab: 'projects', projectId: 'demo', taskId: 'task-a' })).toBe(
      '/projects/demo/tasks/task-a',
    )
  })

  it('URI-encodes ids', () => {
    expect(routeToPath({ tab: 'projects', projectId: 'demo project', taskId: 'task one' })).toBe(
      '/projects/demo%20project/tasks/task%20one',
    )
  })

  it('ignores a taskId with no projectId', () => {
    expect(routeToPath({ tab: 'projects', taskId: 'task-a' })).toBe('/projects')
  })

  it('serializes a new-task session', () => {
    expect(routeToPath({ tab: 'projects', projectId: 'demo', newTaskSessionId: 'session-123' })).toBe(
      '/projects/demo/new-task/session-123',
    )
  })

  it('serializes the draft view for a task', () => {
    expect(routeToPath({ tab: 'projects', projectId: 'demo', taskId: 'task-a', taskView: 'draft' })).toBe(
      '/projects/demo/tasks/task-a/draft',
    )
  })

  it('prefers newTaskSessionId over taskId if both are somehow set', () => {
    expect(
      routeToPath({ tab: 'projects', projectId: 'demo', taskId: 'task-a', newTaskSessionId: 'session-123' }),
    ).toBe('/projects/demo/new-task/session-123')
  })

  it('round-trips through parsePath for all six canonical shapes', () => {
    const routes = [
      { tab: 'chat' as const },
      { tab: 'projects' as const },
      { tab: 'projects' as const, projectId: 'demo' },
      { tab: 'projects' as const, projectId: 'demo', taskId: 'task-a' },
      { tab: 'projects' as const, projectId: 'demo', taskId: 'task-a', taskView: 'draft' as const },
      { tab: 'projects' as const, projectId: 'demo', newTaskSessionId: 'session-123' },
    ]
    for (const route of routes) {
      expect(parsePath(routeToPath(route))).toEqual(route)
    }
  })
})

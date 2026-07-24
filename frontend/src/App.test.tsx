import { beforeEach, describe, expect, it, vi } from 'vitest'
import { render, screen, waitFor } from '@testing-library/react'
import userEvent from '@testing-library/user-event'
import App from './App'
import * as api from './api'

vi.mock('./api')

interface ProjectsPanelStubProps {
  selectedProjectId?: string
  selectedTaskId?: string
  onSelectProject: (id: string) => void
  onBackToProjects: () => void
  onInvalidProject: () => void
  onSelectTask: (id: string) => void
  onBackToProject: () => void
  onInvalidTask: () => void
}

vi.mock('./ProjectsPanel', () => ({
  ProjectsPanel: (props: ProjectsPanelStubProps) => (
    <div data-testid="projects-panel">
      projects panel
      <span data-testid="selected-project-id">{props.selectedProjectId ?? ''}</span>
      <span data-testid="selected-task-id">{props.selectedTaskId ?? ''}</span>
      <button type="button" onClick={() => props.onSelectProject('demo')}>
        Select demo
      </button>
      <button type="button" onClick={props.onBackToProjects}>
        Back to projects
      </button>
      <button type="button" onClick={props.onInvalidProject}>
        Invalid project
      </button>
      <button type="button" onClick={() => props.onSelectTask('task-a')}>
        Select task-a
      </button>
      <button type="button" onClick={props.onBackToProject}>
        Back to project
      </button>
      <button type="button" onClick={props.onInvalidTask}>
        Invalid task
      </button>
    </div>
  ),
}))
vi.mock('./ChatPanel', () => ({
  ChatPanel: () => <div data-testid="chat-panel">chat panel</div>,
}))

// setLocation moves the (jsdom) browser to path without a real navigation —
// the same replaceState trick App itself uses — so each test can control
// what pathname App sees on mount.
function setLocation(path: string) {
  window.history.replaceState(null, '', path)
}

beforeEach(() => {
  vi.mocked(api.getHealthStatus).mockResolvedValue({ status: 'ok', build_id: 'abc123' })
  setLocation('/projects')
})

describe('App — restoring state from the URL on load', () => {
  it('defaults to the Projects tab at /projects', async () => {
    setLocation('/projects')
    render(<App />)

    expect(await screen.findByTestId('projects-panel')).toBeInTheDocument()
    expect(screen.queryByTestId('chat-panel')).not.toBeInTheDocument()
    expect(screen.getByTestId('selected-project-id')).toHaveTextContent('')
  })

  it('restores the Chat tab from /chat', async () => {
    setLocation('/chat')
    render(<App />)

    expect(await screen.findByTestId('chat-panel')).toBeInTheDocument()
    expect(screen.queryByTestId('projects-panel')).not.toBeInTheDocument()
  })

  it('restores a selected project from /projects/:projectId', async () => {
    setLocation('/projects/demo')
    render(<App />)

    expect(await screen.findByTestId('selected-project-id')).toHaveTextContent('demo')
    expect(screen.getByTestId('selected-task-id')).toHaveTextContent('')
  })

  it('restores a selected project and task from /projects/:projectId/tasks/:taskId', async () => {
    setLocation('/projects/demo/tasks/task-a')
    render(<App />)

    expect(await screen.findByTestId('selected-project-id')).toHaveTextContent('demo')
    expect(screen.getByTestId('selected-task-id')).toHaveTextContent('task-a')
  })
})

describe('App — silent fallback correction for non-canonical paths', () => {
  it('collapses the bare "/" path to /projects via replaceState', async () => {
    setLocation('/')
    const replaceSpy = vi.spyOn(window.history, 'replaceState')
    const pushSpy = vi.spyOn(window.history, 'pushState')
    render(<App />)

    await screen.findByTestId('projects-panel')
    expect(replaceSpy).toHaveBeenCalledWith(null, '', '/projects')
    expect(pushSpy).not.toHaveBeenCalled()
  })

  it('collapses an unrecognized path to /projects via replaceState', async () => {
    setLocation('/something/else/entirely')
    const replaceSpy = vi.spyOn(window.history, 'replaceState')
    render(<App />)

    await screen.findByTestId('projects-panel')
    expect(replaceSpy).toHaveBeenCalledWith(null, '', '/projects')
  })

  it('does not touch history for an already-canonical path', async () => {
    setLocation('/projects/demo/tasks/task-a')
    const replaceSpy = vi.spyOn(window.history, 'replaceState')
    const pushSpy = vi.spyOn(window.history, 'pushState')
    render(<App />)

    await screen.findByTestId('selected-task-id')
    expect(replaceSpy).not.toHaveBeenCalled()
    expect(pushSpy).not.toHaveBeenCalled()
  })
})

describe('App — user-driven navigation pushes history', () => {
  it('switching tabs pushes /chat and /projects', async () => {
    const user = userEvent.setup()
    const pushSpy = vi.spyOn(window.history, 'pushState')
    render(<App />)
    await screen.findByTestId('projects-panel')

    await user.click(screen.getByRole('button', { name: 'Chat' }))
    expect(pushSpy).toHaveBeenCalledWith(null, '', '/chat')
    expect(await screen.findByTestId('chat-panel')).toBeInTheDocument()

    await user.click(screen.getByRole('button', { name: 'Projects' }))
    expect(pushSpy).toHaveBeenCalledWith(null, '', '/projects')
    expect(await screen.findByTestId('projects-panel')).toBeInTheDocument()
  })

  it('selecting a project pushes /projects/:projectId', async () => {
    const user = userEvent.setup()
    const pushSpy = vi.spyOn(window.history, 'pushState')
    render(<App />)
    await screen.findByTestId('projects-panel')

    await user.click(screen.getByRole('button', { name: 'Select demo' }))

    expect(pushSpy).toHaveBeenCalledWith(null, '', '/projects/demo')
    expect(await screen.findByTestId('selected-project-id')).toHaveTextContent('demo')
  })

  it('selecting a task pushes /projects/:projectId/tasks/:taskId', async () => {
    const user = userEvent.setup()
    setLocation('/projects/demo')
    const pushSpy = vi.spyOn(window.history, 'pushState')
    render(<App />)
    await screen.findByTestId('selected-project-id')

    await user.click(screen.getByRole('button', { name: 'Select task-a' }))

    expect(pushSpy).toHaveBeenCalledWith(null, '', '/projects/demo/tasks/task-a')
    expect(await screen.findByTestId('selected-task-id')).toHaveTextContent('task-a')
  })

  it('the back-to-projects affordance pushes /projects', async () => {
    const user = userEvent.setup()
    setLocation('/projects/demo')
    const pushSpy = vi.spyOn(window.history, 'pushState')
    render(<App />)
    await screen.findByTestId('selected-project-id')

    await user.click(screen.getByRole('button', { name: 'Back to projects' }))

    expect(pushSpy).toHaveBeenCalledWith(null, '', '/projects')
    expect(await screen.findByTestId('selected-project-id')).toHaveTextContent('')
  })

  it('the back-to-project affordance pushes /projects/:projectId', async () => {
    const user = userEvent.setup()
    setLocation('/projects/demo/tasks/task-a')
    const pushSpy = vi.spyOn(window.history, 'pushState')
    render(<App />)
    await screen.findByTestId('selected-task-id')

    await user.click(screen.getByRole('button', { name: 'Back to project' }))

    expect(pushSpy).toHaveBeenCalledWith(null, '', '/projects/demo')
    expect(await screen.findByTestId('selected-task-id')).toHaveTextContent('')
    expect(screen.getByTestId('selected-project-id')).toHaveTextContent('demo')
  })
})

describe('App — invalid ids correct the URL via replaceState, not pushState', () => {
  it('an invalid project id collapses to /projects via replaceState', async () => {
    const user = userEvent.setup()
    setLocation('/projects/bogus')
    const replaceSpy = vi.spyOn(window.history, 'replaceState')
    const pushSpy = vi.spyOn(window.history, 'pushState')
    render(<App />)
    await screen.findByTestId('selected-project-id')

    await user.click(screen.getByRole('button', { name: 'Invalid project' }))

    expect(replaceSpy).toHaveBeenCalledWith(null, '', '/projects')
    expect(pushSpy).not.toHaveBeenCalled()
    expect(await screen.findByTestId('selected-project-id')).toHaveTextContent('')
  })

  it('an invalid task id collapses to /projects/:projectId via replaceState', async () => {
    const user = userEvent.setup()
    setLocation('/projects/demo/tasks/bogus')
    const replaceSpy = vi.spyOn(window.history, 'replaceState')
    const pushSpy = vi.spyOn(window.history, 'pushState')
    render(<App />)
    await screen.findByTestId('selected-task-id')

    await user.click(screen.getByRole('button', { name: 'Invalid task' }))

    expect(replaceSpy).toHaveBeenCalledWith(null, '', '/projects/demo')
    expect(pushSpy).not.toHaveBeenCalled()
    expect(await screen.findByTestId('selected-task-id')).toHaveTextContent('')
    expect(screen.getByTestId('selected-project-id')).toHaveTextContent('demo')
  })
})

describe('App — browser back/forward (popstate)', () => {
  it('re-parses the path on popstate without pushing or replacing history itself', async () => {
    setLocation('/projects/demo')
    render(<App />)
    await screen.findByTestId('selected-project-id')
    expect(screen.getByTestId('selected-project-id')).toHaveTextContent('demo')

    const replaceSpy = vi.spyOn(window.history, 'replaceState')
    const pushSpy = vi.spyOn(window.history, 'pushState')

    // Simulate the browser having navigated back to /projects — the
    // pathname changes (as the browser itself would do) and a popstate
    // event fires; App should only re-render from it, never call
    // pushState/replaceState in response.
    window.history.replaceState(null, '', '/projects')
    replaceSpy.mockClear()
    window.dispatchEvent(new PopStateEvent('popstate'))

    await waitFor(() => expect(screen.getByTestId('selected-project-id')).toHaveTextContent(''))
    expect(replaceSpy).not.toHaveBeenCalled()
    expect(pushSpy).not.toHaveBeenCalled()
  })

  it('moves forward again through a later popstate', async () => {
    setLocation('/projects')
    render(<App />)
    await screen.findByTestId('projects-panel')

    window.history.replaceState(null, '', '/projects/demo/tasks/task-a')
    window.dispatchEvent(new PopStateEvent('popstate'))

    await waitFor(() => expect(screen.getByTestId('selected-task-id')).toHaveTextContent('task-a'))
    expect(screen.getByTestId('selected-project-id')).toHaveTextContent('demo')
  })
})

describe('App — build badge', () => {
  it('shows "build dev" before the healthcheck resolves, then the real build_id', async () => {
    render(<App />)

    expect(screen.getByText('build dev')).toBeInTheDocument()
    await waitFor(() => expect(screen.getByText('build abc123')).toBeInTheDocument())
  })

  it('falls back to "build dev" when the healthcheck rejects', async () => {
    vi.mocked(api.getHealthStatus).mockRejectedValue(new Error('down'))
    render(<App />)

    await waitFor(() => expect(api.getHealthStatus).toHaveBeenCalled())
    expect(screen.getByText('build dev')).toBeInTheDocument()
  })
})

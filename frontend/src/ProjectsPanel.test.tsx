import { describe, expect, it, vi } from 'vitest'
import { render, screen, waitFor } from '@testing-library/react'
import userEvent from '@testing-library/user-event'
import { ProjectsPanel } from './ProjectsPanel'
import * as api from './api'
import type { Project } from './types'

vi.mock('./api')
vi.mock('./ProjectDetailPanel', () => ({
  ProjectDetailPanel: (props: {
    project: Project
    onBack: () => void
    selectedTaskId?: string
    onSelectTask: (id: string) => void
    onBackToProject: () => void
    onInvalidTask: () => void
  }) => (
    <div data-testid="project-detail-panel">
      project-detail:{props.project.id}
      {props.selectedTaskId && <span data-testid="selected-task-id">{props.selectedTaskId}</span>}
      <button type="button" onClick={props.onBack}>
        Back to projects
      </button>
      <button type="button" onClick={() => props.onSelectTask('task-a')}>
        Open task-a
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

function makeProject(overrides: Partial<Project>): Project {
  return {
    id: 'demo',
    name: 'Demo',
    description: '',
    repositories: [],
    knowledge: [],
    constraints: [],
    created_at: '2026-01-01T00:00:00Z',
    updated_at: '2026-01-01T00:00:00Z',
    ...overrides,
  }
}

function noop() {
  // intentionally empty — unused callback in tests that don't exercise it
}

interface Overrides {
  selectedProjectId?: string
  selectedTaskId?: string
  onSelectProject?: (id: string) => void
  onBackToProjects?: () => void
  onInvalidProject?: () => void
  onSelectTask?: (id: string) => void
  onBackToProject?: () => void
  onInvalidTask?: () => void
}

function renderPanel(overrides: Overrides = {}) {
  return render(
    <ProjectsPanel
      selectedProjectId={overrides.selectedProjectId}
      selectedTaskId={overrides.selectedTaskId}
      onSelectProject={overrides.onSelectProject ?? noop}
      onBackToProjects={overrides.onBackToProjects ?? noop}
      onInvalidProject={overrides.onInvalidProject ?? noop}
      onSelectTask={overrides.onSelectTask ?? noop}
      onBackToProject={overrides.onBackToProject ?? noop}
      onInvalidTask={overrides.onInvalidTask ?? noop}
    />,
  )
}

describe('ProjectsPanel — list states', () => {
  it('shows an error when listProjects rejects', async () => {
    vi.mocked(api.listProjects).mockRejectedValue(new Error('network down'))
    renderPanel()
    expect(await screen.findByText('Failed to load projects: network down')).toBeInTheDocument()
  })

  it('shows an empty state with no projects', async () => {
    vi.mocked(api.listProjects).mockResolvedValue({ projects: [], errors: [] })
    renderPanel()
    expect(await screen.findByText('No projects yet.')).toBeInTheDocument()
  })

  it('renders a populated table with a partial-load-errors notice', async () => {
    vi.mocked(api.listProjects).mockResolvedValue({
      projects: [makeProject({ id: 'demo', name: 'Demo Project' })],
      errors: [{ id: 'broken-project', error: 'parsing failed' }],
    })
    renderPanel()

    expect(await screen.findByText('Demo Project')).toBeInTheDocument()
    expect(screen.getByText(/1 project failed to load: broken-project/)).toBeInTheDocument()
  })
})

describe('ProjectsPanel — creation and selection', () => {
  it('New Project creates a project via createProject and reloads the list', async () => {
    const user = userEvent.setup()
    vi.mocked(api.listProjects).mockResolvedValue({ projects: [], errors: [] })
    vi.mocked(api.createProject).mockResolvedValue(makeProject({ id: 'new-project', name: 'New Project' }))

    renderPanel()
    await screen.findByText('No projects yet.')

    await user.click(screen.getByRole('button', { name: 'New Project' }))
    await user.type(screen.getByLabelText('Name'), 'New Project')
    await user.click(screen.getByRole('button', { name: 'Save' }))

    await waitFor(() => expect(api.createProject).toHaveBeenCalled())
    await waitFor(() => expect(api.listProjects).toHaveBeenCalledTimes(2))
  })

  it('View tasks calls onSelectProject with the clicked project id (no fetch)', async () => {
    const user = userEvent.setup()
    const onSelectProject = vi.fn()
    vi.mocked(api.listProjects).mockResolvedValue({
      projects: [makeProject({ id: 'demo', name: 'Demo Project' })],
      errors: [],
    })
    renderPanel({ onSelectProject })
    await screen.findByText('Demo Project')

    await user.click(screen.getByRole('button', { name: 'View tasks' }))

    expect(onSelectProject).toHaveBeenCalledWith('demo')
    expect(api.getProject).not.toHaveBeenCalled()
  })
})

describe('ProjectsPanel — controlled selectedProjectId', () => {
  it('renders ProjectDetailPanel for a project already in the loaded list, with no getProject call', async () => {
    vi.mocked(api.listProjects).mockResolvedValue({
      projects: [makeProject({ id: 'demo', name: 'Demo Project' })],
      errors: [],
    })
    renderPanel({ selectedProjectId: 'demo' })

    expect(await screen.findByTestId('project-detail-panel')).toHaveTextContent('project-detail:demo')
    expect(api.getProject).not.toHaveBeenCalled()
  })

  it('falls back to getProject for a deep-linked project id not in the loaded list', async () => {
    vi.mocked(api.listProjects).mockResolvedValue({ projects: [], errors: [] })
    vi.mocked(api.getProject).mockResolvedValue(makeProject({ id: 'deep-link', name: 'Deep Link Project' }))

    renderPanel({ selectedProjectId: 'deep-link' })

    expect(await screen.findByTestId('project-detail-panel')).toHaveTextContent('project-detail:deep-link')
    expect(api.getProject).toHaveBeenCalledWith('deep-link')
  })

  it('calls onInvalidProject when getProject rejects for a deep-linked project id', async () => {
    const onInvalidProject = vi.fn()
    vi.mocked(api.listProjects).mockResolvedValue({ projects: [], errors: [] })
    vi.mocked(api.getProject).mockRejectedValue(new Error('not found'))

    renderPanel({ selectedProjectId: 'missing', onInvalidProject })

    await waitFor(() => expect(onInvalidProject).toHaveBeenCalled())
    expect(screen.queryByTestId('project-detail-panel')).not.toBeInTheDocument()
  })

  it('passes selectedTaskId and the task callbacks through to ProjectDetailPanel', async () => {
    const user = userEvent.setup()
    const onSelectTask = vi.fn()
    const onBackToProject = vi.fn()
    const onInvalidTask = vi.fn()
    vi.mocked(api.listProjects).mockResolvedValue({
      projects: [makeProject({ id: 'demo', name: 'Demo Project' })],
      errors: [],
    })
    renderPanel({ selectedProjectId: 'demo', selectedTaskId: 'task-a', onSelectTask, onBackToProject, onInvalidTask })

    expect(await screen.findByTestId('selected-task-id')).toHaveTextContent('task-a')

    await user.click(screen.getByRole('button', { name: 'Open task-a' }))
    expect(onSelectTask).toHaveBeenCalledWith('task-a')

    await user.click(screen.getByRole('button', { name: 'Back to project' }))
    expect(onBackToProject).toHaveBeenCalled()

    await user.click(screen.getByRole('button', { name: 'Invalid task' }))
    expect(onInvalidTask).toHaveBeenCalled()
  })

  it('Back to projects calls onBackToProjects', async () => {
    const user = userEvent.setup()
    const onBackToProjects = vi.fn()
    vi.mocked(api.listProjects).mockResolvedValue({
      projects: [makeProject({ id: 'demo', name: 'Demo Project' })],
      errors: [],
    })
    renderPanel({ selectedProjectId: 'demo', onBackToProjects })
    await screen.findByTestId('project-detail-panel')

    await user.click(screen.getByRole('button', { name: 'Back to projects' }))
    expect(onBackToProjects).toHaveBeenCalled()
  })
})

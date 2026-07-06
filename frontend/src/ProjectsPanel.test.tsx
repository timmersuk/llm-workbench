import { describe, expect, it, vi } from 'vitest'
import { render, screen, waitFor } from '@testing-library/react'
import userEvent from '@testing-library/user-event'
import { ProjectsPanel } from './ProjectsPanel'
import * as api from './api'
import type { Project } from './types'

vi.mock('./api')
vi.mock('./ProjectDetailPanel', () => ({
  ProjectDetailPanel: (props: { project: Project; onBack: () => void }) => (
    <div data-testid="project-detail-panel">
      project-detail:{props.project.id}
      <button type="button" onClick={props.onBack}>
        Back to projects
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

describe('ProjectsPanel — list states', () => {
  it('shows an error when listProjects rejects', async () => {
    vi.mocked(api.listProjects).mockRejectedValue(new Error('network down'))
    render(<ProjectsPanel />)
    expect(await screen.findByText('Failed to load projects: network down')).toBeInTheDocument()
  })

  it('shows an empty state with no projects', async () => {
    vi.mocked(api.listProjects).mockResolvedValue({ projects: [], errors: [] })
    render(<ProjectsPanel />)
    expect(await screen.findByText('No projects yet.')).toBeInTheDocument()
  })

  it('renders a populated table with a partial-load-errors notice', async () => {
    vi.mocked(api.listProjects).mockResolvedValue({
      projects: [makeProject({ id: 'demo', name: 'Demo Project' })],
      errors: [{ id: 'broken-project', error: 'parsing failed' }],
    })
    render(<ProjectsPanel />)

    expect(await screen.findByText('Demo Project')).toBeInTheDocument()
    expect(screen.getByText(/1 project failed to load: broken-project/)).toBeInTheDocument()
  })
})

describe('ProjectsPanel — creation and selection', () => {
  it('New Project creates a project via createProject and reloads the list', async () => {
    const user = userEvent.setup()
    vi.mocked(api.listProjects).mockResolvedValue({ projects: [], errors: [] })
    vi.mocked(api.createProject).mockResolvedValue(makeProject({ id: 'new-project', name: 'New Project' }))

    render(<ProjectsPanel />)
    await screen.findByText('No projects yet.')

    await user.click(screen.getByRole('button', { name: 'New Project' }))
    await user.type(screen.getByLabelText('Name'), 'New Project')
    await user.click(screen.getByRole('button', { name: 'Save' }))

    await waitFor(() => expect(api.createProject).toHaveBeenCalled())
    await waitFor(() => expect(api.listProjects).toHaveBeenCalledTimes(2))
  })

  it('View tasks selects a project and renders the stubbed ProjectDetailPanel', async () => {
    const user = userEvent.setup()
    vi.mocked(api.listProjects).mockResolvedValue({
      projects: [makeProject({ id: 'demo', name: 'Demo Project' })],
      errors: [],
    })
    render(<ProjectsPanel />)
    await screen.findByText('Demo Project')

    await user.click(screen.getByRole('button', { name: 'View tasks' }))

    expect(await screen.findByTestId('project-detail-panel')).toHaveTextContent('project-detail:demo')
  })
})

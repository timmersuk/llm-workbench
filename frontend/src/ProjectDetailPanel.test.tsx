import { describe, expect, it, vi } from 'vitest'
import { render, screen, waitFor } from '@testing-library/react'
import userEvent from '@testing-library/user-event'
import { ProjectDetailPanel } from './ProjectDetailPanel'
import * as api from './api'
import type { Project, Task } from './types'

vi.mock('./api')
vi.mock('./TaskDetailPanel', () => ({
  TaskDetailPanel: (props: { projectId: string; task: Task; onBack: () => void }) => (
    <div data-testid="task-detail-panel">
      task-detail:{props.projectId}:{props.task.id}
      <button type="button" onClick={props.onBack}>
        Back to tasks
      </button>
    </div>
  ),
}))

const project: Project = {
  id: 'demo',
  name: 'Demo Project',
  description: 'A demo project',
  repositories: [],
  knowledge: [],
  constraints: [],
  created_at: '2026-01-01T00:00:00Z',
  updated_at: '2026-01-01T00:00:00Z',
}

function makeTask(overrides: Partial<Task>): Task {
  return {
    id: 'task-a',
    title: 'Task A',
    project: 'demo',
    status: 'draft',
    stage: 'requirements',
    created_at: '2026-01-01T00:00:00Z',
    updated_at: '2026-01-01T00:00:00Z',
    objective: '',
    constraints: [],
    assumptions: [],
    success_criteria: [],
    references: { knowledge: [], repo: [] },
    ...overrides,
  }
}

describe('ProjectDetailPanel — task list states', () => {
  it('shows an error when listProjectTasks rejects', async () => {
    vi.mocked(api.listProjectTasks).mockRejectedValue(new Error('network down'))
    render(<ProjectDetailPanel project={project} onBack={vi.fn()} />)
    expect(await screen.findByText('Failed to load tasks: network down')).toBeInTheDocument()
  })

  it('shows an empty state with no tasks', async () => {
    vi.mocked(api.listProjectTasks).mockResolvedValue({ tasks: [], errors: [] })
    render(<ProjectDetailPanel project={project} onBack={vi.fn()} />)
    expect(await screen.findByText('No tasks yet.')).toBeInTheDocument()
  })

  it('renders a populated list-view table with a partial-load-errors notice', async () => {
    vi.mocked(api.listProjectTasks).mockResolvedValue({
      tasks: [makeTask({ id: 'task-a', title: 'Task A' })],
      errors: [{ id: 'task-b', error: 'parsing failed' }],
    })
    render(<ProjectDetailPanel project={project} onBack={vi.fn()} />)

    expect(await screen.findByText('Task A')).toBeInTheDocument()
    expect(screen.getByText(/1 task failed to load: task-b/)).toBeInTheDocument()
  })
})

describe('ProjectDetailPanel — list/kanban toggle', () => {
  it('switches between list and kanban views', async () => {
    const user = userEvent.setup()
    vi.mocked(api.listProjectTasks).mockResolvedValue({
      tasks: [makeTask({ id: 'task-a', title: 'Task A', stage: 'requirements' })],
      errors: [],
    })
    render(<ProjectDetailPanel project={project} onBack={vi.fn()} />)
    await screen.findByText('Task A')

    expect(screen.getByRole('table')).toBeInTheDocument()

    await user.click(screen.getByRole('button', { name: 'Kanban' }))
    expect(screen.queryByRole('table')).not.toBeInTheDocument()
    expect(screen.getByText('Requirements')).toBeInTheDocument() // kanban column header

    await user.click(screen.getByRole('button', { name: 'List' }))
    expect(screen.getByRole('table')).toBeInTheDocument()
  })
})

describe('ProjectDetailPanel — task creation and selection', () => {
  it('New Task creates a task via createProjectTask and reloads the list', async () => {
    const user = userEvent.setup()
    vi.mocked(api.listProjectTasks).mockResolvedValue({ tasks: [], errors: [] })
    vi.mocked(api.createProjectTask).mockResolvedValue(makeTask({ id: 'new-task', title: 'New Task' }))

    render(<ProjectDetailPanel project={project} onBack={vi.fn()} />)
    await screen.findByText('No tasks yet.')

    await user.click(screen.getByRole('button', { name: 'New Task' }))
    await user.type(screen.getByLabelText('ID'), 'new-task')
    await user.type(screen.getByLabelText('Title'), 'New Task')
    await user.click(screen.getByRole('button', { name: 'Save' }))

    await waitFor(() =>
      expect(api.createProjectTask).toHaveBeenCalledWith('demo', {
        id: 'new-task',
        title: 'New Task',
        references: { knowledge: [], repo: [] },
      }),
    )
    await waitFor(() => expect(api.listProjectTasks).toHaveBeenCalledTimes(2))
  })

  it('Open selects a task and renders the stubbed TaskDetailPanel with the right props', async () => {
    const user = userEvent.setup()
    vi.mocked(api.listProjectTasks).mockResolvedValue({
      tasks: [makeTask({ id: 'task-a', title: 'Task A' })],
      errors: [],
    })
    render(<ProjectDetailPanel project={project} onBack={vi.fn()} />)
    await screen.findByText('Task A')

    await user.click(screen.getByRole('button', { name: 'Open' }))

    expect(await screen.findByTestId('task-detail-panel')).toHaveTextContent('task-detail:demo:task-a')
  })
})

describe('ProjectDetailPanel — edit project', () => {
  it('Edit project opens ProjectForm and updateProject is called on save', async () => {
    const user = userEvent.setup()
    vi.mocked(api.listProjectTasks).mockResolvedValue({ tasks: [], errors: [] })
    vi.mocked(api.updateProject).mockResolvedValue({ ...project, description: 'Updated description' })

    render(<ProjectDetailPanel project={project} onBack={vi.fn()} />)
    await screen.findByText('No tasks yet.')

    await user.click(screen.getByRole('button', { name: 'Edit project' }))
    await user.clear(screen.getByLabelText('Description'))
    await user.type(screen.getByLabelText('Description'), 'Updated description')
    await user.click(screen.getByRole('button', { name: 'Save' }))

    await waitFor(() => expect(api.updateProject).toHaveBeenCalled())
    expect(await screen.findByText('Updated description')).toBeInTheDocument()
  })
})

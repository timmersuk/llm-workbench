import { describe, expect, it, vi } from 'vitest'
import { render, screen, waitFor } from '@testing-library/react'
import userEvent from '@testing-library/user-event'
import { ProjectDetailPanel } from './ProjectDetailPanel'
import * as api from './api'
import type { Project, Task } from './types'

vi.mock('./api')
vi.mock('./TaskDetailPanel', () => ({
  TaskDetailPanel: (props: { projectId: string; task: Task; onBack: () => void; onViewDraft: () => void }) => (
    <div data-testid="task-detail-panel">
      task-detail:{props.projectId}:{props.task.id}
      <button type="button" onClick={props.onBack}>
        Back to tasks
      </button>
      <button type="button" onClick={props.onViewDraft}>
        View pre-creation conversation
      </button>
    </div>
  ),
}))
vi.mock('./NewTaskPanel', () => ({
  NewTaskPanel: (props: { projectId: string; sessionId: string; onCreated: (task: Task) => void }) => (
    <div data-testid="new-task-panel">
      new-task:{props.projectId}:{props.sessionId}
      <button
        type="button"
        onClick={() =>
          props.onCreated({
            id: 'created-task',
            title: 'Created Task',
            project: props.projectId,
            stage: 'requirements',
            created_at: '2026-01-01T00:00:00Z',
            updated_at: '2026-01-01T00:00:00Z',
            objective: '',
            constraints: [],
            assumptions: [],
            success_criteria: [],
            references: { knowledge: [], repo: [] },
          })
        }
      >
        Confirm draft
      </button>
    </div>
  ),
}))
vi.mock('./TaskDraftView', () => ({
  TaskDraftView: (props: { projectId: string; task: Task; onBack: () => void }) => (
    <div data-testid="task-draft-view">
      task-draft:{props.projectId}:{props.task.id}
      <button type="button" onClick={props.onBack}>
        Back to task
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

function noop() {
  // intentionally empty — unused callback in tests that don't exercise it
}

interface Overrides {
  onBack?: () => void
  selectedTaskId?: string
  selectedTaskView?: 'draft'
  selectedNewTaskSessionId?: string
  onSelectTask?: (id: string) => void
  onBackToProject?: () => void
  onInvalidTask?: () => void
  onNewTask?: (sessionId: string) => void
  onViewTaskDraft?: () => void
  onBackFromTaskDraft?: () => void
}

function renderPanel(overrides: Overrides = {}) {
  return render(
    <ProjectDetailPanel
      project={project}
      onBack={overrides.onBack ?? noop}
      selectedTaskId={overrides.selectedTaskId}
      selectedTaskView={overrides.selectedTaskView}
      selectedNewTaskSessionId={overrides.selectedNewTaskSessionId}
      onSelectTask={overrides.onSelectTask ?? noop}
      onBackToProject={overrides.onBackToProject ?? noop}
      onInvalidTask={overrides.onInvalidTask ?? noop}
      onNewTask={overrides.onNewTask ?? noop}
      onViewTaskDraft={overrides.onViewTaskDraft ?? noop}
      onBackFromTaskDraft={overrides.onBackFromTaskDraft ?? noop}
    />,
  )
}

describe('ProjectDetailPanel — task list states', () => {
  it('shows an error when listProjectTasks rejects', async () => {
    vi.mocked(api.listProjectTasks).mockRejectedValue(new Error('network down'))
    renderPanel()
    expect(await screen.findByText('Failed to load tasks: network down')).toBeInTheDocument()
  })

  it('shows an empty state with no tasks', async () => {
    vi.mocked(api.listProjectTasks).mockResolvedValue({ tasks: [], errors: [] })
    renderPanel()
    expect(await screen.findByText('No tasks yet.')).toBeInTheDocument()
  })

  it('renders populated tasks on the kanban board with a partial-load-errors notice', async () => {
    vi.mocked(api.listProjectTasks).mockResolvedValue({
      tasks: [makeTask({ id: 'task-a', title: 'Task A', stage: 'requirements' })],
      errors: [{ id: 'task-b', error: 'parsing failed' }],
    })
    renderPanel()

    expect(await screen.findByText('Task A')).toBeInTheDocument()
    expect(screen.getByText('Requirements')).toBeInTheDocument() // kanban column header
    expect(screen.getByText(/1 task failed to load: task-b/)).toBeInTheDocument()
  })
})

describe('ProjectDetailPanel — New Task (chat-driven)', () => {
  it('New Task mints a session id client-side and calls onNewTask immediately, with no API call', async () => {
    const user = userEvent.setup()
    const onNewTask = vi.fn()
    vi.mocked(api.listProjectTasks).mockResolvedValue({ tasks: [], errors: [] })

    renderPanel({ onNewTask })
    await screen.findByText('No tasks yet.')

    await user.click(screen.getByRole('button', { name: 'New Task' }))

    expect(onNewTask).toHaveBeenCalledTimes(1)
    expect(onNewTask.mock.calls[0][0]).toEqual(expect.any(String))
    expect(onNewTask.mock.calls[0][0].length).toBeGreaterThan(0)
    expect(api.createProjectTask).not.toHaveBeenCalled()
  })

  it('renders NewTaskPanel when selectedNewTaskSessionId is set', async () => {
    vi.mocked(api.listProjectTasks).mockResolvedValue({ tasks: [], errors: [] })
    renderPanel({ selectedNewTaskSessionId: 'session-123' })

    expect(await screen.findByTestId('new-task-panel')).toHaveTextContent('new-task:demo:session-123')
  })

  it('confirming the draft in NewTaskPanel reloads the task list and selects the created task', async () => {
    const user = userEvent.setup()
    const onSelectTask = vi.fn()
    vi.mocked(api.listProjectTasks).mockResolvedValue({ tasks: [], errors: [] })

    renderPanel({ selectedNewTaskSessionId: 'session-123', onSelectTask })
    await screen.findByTestId('new-task-panel')

    await user.click(screen.getByRole('button', { name: 'Confirm draft' }))

    expect(onSelectTask).toHaveBeenCalledWith('created-task')
    await waitFor(() => expect(api.listProjectTasks).toHaveBeenCalledTimes(2))
  })
})

describe('ProjectDetailPanel — task selection', () => {
  it('clicking a kanban card calls onSelectTask with the clicked task id (no fetch)', async () => {
    const user = userEvent.setup()
    const onSelectTask = vi.fn()
    vi.mocked(api.listProjectTasks).mockResolvedValue({
      tasks: [makeTask({ id: 'task-a', title: 'Task A' })],
      errors: [],
    })
    renderPanel({ onSelectTask })
    await screen.findByText('Task A')

    await user.click(screen.getByRole('button', { name: /Task A/ }))

    expect(onSelectTask).toHaveBeenCalledWith('task-a')
    expect(api.getProjectTask).not.toHaveBeenCalled()
  })
})

describe('ProjectDetailPanel — controlled selectedTaskId', () => {
  it('renders TaskDetailPanel for a task already in the loaded list, with no getProjectTask call', async () => {
    vi.mocked(api.listProjectTasks).mockResolvedValue({
      tasks: [makeTask({ id: 'task-a', title: 'Task A' })],
      errors: [],
    })
    renderPanel({ selectedTaskId: 'task-a' })

    expect(await screen.findByTestId('task-detail-panel')).toHaveTextContent('task-detail:demo:task-a')
    expect(api.getProjectTask).not.toHaveBeenCalled()
  })

  it('falls back to getProjectTask for a deep-linked task id not in the loaded list', async () => {
    vi.mocked(api.listProjectTasks).mockResolvedValue({ tasks: [], errors: [] })
    vi.mocked(api.getProjectTask).mockResolvedValue(makeTask({ id: 'deep-link', title: 'Deep Link Task' }))

    renderPanel({ selectedTaskId: 'deep-link' })

    expect(await screen.findByTestId('task-detail-panel')).toHaveTextContent('task-detail:demo:deep-link')
    expect(api.getProjectTask).toHaveBeenCalledWith('demo', 'deep-link')
  })

  it('calls onInvalidTask when getProjectTask rejects for a deep-linked task id', async () => {
    const onInvalidTask = vi.fn()
    vi.mocked(api.listProjectTasks).mockResolvedValue({ tasks: [], errors: [] })
    vi.mocked(api.getProjectTask).mockRejectedValue(new Error('not found'))

    renderPanel({ selectedTaskId: 'missing', onInvalidTask })

    await waitFor(() => expect(onInvalidTask).toHaveBeenCalled())
    expect(screen.queryByTestId('task-detail-panel')).not.toBeInTheDocument()
  })

  it('Back from TaskDetailPanel calls onBackToProject and reloads tasks', async () => {
    const user = userEvent.setup()
    const onBackToProject = vi.fn()
    vi.mocked(api.listProjectTasks).mockResolvedValue({
      tasks: [makeTask({ id: 'task-a', title: 'Task A' })],
      errors: [],
    })
    renderPanel({ selectedTaskId: 'task-a', onBackToProject })
    await screen.findByTestId('task-detail-panel')

    await user.click(screen.getByRole('button', { name: 'Back to tasks' }))

    expect(onBackToProject).toHaveBeenCalled()
    await waitFor(() => expect(api.listProjectTasks).toHaveBeenCalledTimes(2))
  })

  it('View pre-creation conversation calls onViewTaskDraft', async () => {
    const user = userEvent.setup()
    const onViewTaskDraft = vi.fn()
    vi.mocked(api.listProjectTasks).mockResolvedValue({
      tasks: [makeTask({ id: 'task-a', title: 'Task A' })],
      errors: [],
    })
    renderPanel({ selectedTaskId: 'task-a', onViewTaskDraft })
    await screen.findByTestId('task-detail-panel')

    await user.click(screen.getByRole('button', { name: 'View pre-creation conversation' }))

    expect(onViewTaskDraft).toHaveBeenCalled()
  })
})

describe('ProjectDetailPanel — task draft view', () => {
  it('renders TaskDraftView (not TaskDetailPanel) when selectedTaskView is draft', async () => {
    vi.mocked(api.listProjectTasks).mockResolvedValue({
      tasks: [makeTask({ id: 'task-a', title: 'Task A', draft_session_id: 'session-1' })],
      errors: [],
    })
    renderPanel({ selectedTaskId: 'task-a', selectedTaskView: 'draft' })

    expect(await screen.findByTestId('task-draft-view')).toHaveTextContent('task-draft:demo:task-a')
    expect(screen.queryByTestId('task-detail-panel')).not.toBeInTheDocument()
  })

  it('Back from TaskDraftView calls onBackFromTaskDraft', async () => {
    const user = userEvent.setup()
    const onBackFromTaskDraft = vi.fn()
    vi.mocked(api.listProjectTasks).mockResolvedValue({
      tasks: [makeTask({ id: 'task-a', title: 'Task A', draft_session_id: 'session-1' })],
      errors: [],
    })
    renderPanel({ selectedTaskId: 'task-a', selectedTaskView: 'draft', onBackFromTaskDraft })
    await screen.findByTestId('task-draft-view')

    await user.click(screen.getByRole('button', { name: 'Back to task' }))

    expect(onBackFromTaskDraft).toHaveBeenCalled()
  })
})

describe('ProjectDetailPanel — edit project', () => {
  it('Edit project opens ProjectForm and updateProject is called on save', async () => {
    const user = userEvent.setup()
    vi.mocked(api.listProjectTasks).mockResolvedValue({ tasks: [], errors: [] })
    vi.mocked(api.updateProject).mockResolvedValue({ ...project, description: 'Updated description' })

    renderPanel()
    await screen.findByText('No tasks yet.')

    await user.click(screen.getByRole('button', { name: 'Edit project' }))
    await user.clear(screen.getByLabelText('Description'))
    await user.type(screen.getByLabelText('Description'), 'Updated description')
    await user.click(screen.getByRole('button', { name: 'Save' }))

    await waitFor(() => expect(api.updateProject).toHaveBeenCalled())
    expect(await screen.findByText('Updated description')).toBeInTheDocument()
  })
})

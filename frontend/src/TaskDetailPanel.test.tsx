import { describe, expect, it, vi } from 'vitest'
import { render, screen, waitFor } from '@testing-library/react'
import userEvent from '@testing-library/user-event'
import { TaskDetailPanel } from './TaskDetailPanel'
import * as api from './api'
import type { Task, TaskStage } from './types'

vi.mock('./api')
vi.mock('./GrillMePanel', () => ({
  GrillMePanel: (props: { projectId: string; taskId: string }) => (
    <div data-testid="grillme-panel">
      grillme:{props.projectId}:{props.taskId}
    </div>
  ),
}))
vi.mock('./PlanningModePanel', () => ({
  PlanningModePanel: (props: { projectId: string; taskId: string }) => (
    <div data-testid="planningmode-panel">
      planningmode:{props.projectId}:{props.taskId}
    </div>
  ),
}))
vi.mock('./ExecutePanel', () => ({
  ExecutePanel: (props: { projectId: string; taskId: string }) => (
    <div data-testid="execute-panel">
      execute:{props.projectId}:{props.taskId}
    </div>
  ),
}))
vi.mock('./ReviewPanel', () => ({
  ReviewPanel: (props: { projectId: string; taskId: string }) => (
    <div data-testid="review-panel">
      review:{props.projectId}:{props.taskId}
    </div>
  ),
}))
vi.mock('./PRReviewPanel', () => ({
  PRReviewPanel: (props: { projectId: string; taskId: string }) => (
    <div data-testid="pr-review-panel">
      pr-review:{props.projectId}:{props.taskId}
    </div>
  ),
}))

const projectId = 'demo'

function makeTask(stage: TaskStage, overrides: Partial<Task> = {}): Task {
  return {
    id: 'task-a',
    title: 'Task A',
    project: projectId,
    status: 'draft',
    stage,
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

function stubNoContextOrPlan() {
  vi.mocked(api.getTaskContext).mockRejectedValue(new Error('not found'))
  vi.mocked(api.getTaskPlan).mockRejectedValue(new Error('not found'))
  // The merged-stage effect reads the latest verdict; default to empty so
  // tests that don't care about the completion detail don't crash on an
  // unmocked call.
  vi.mocked(api.listReviews).mockResolvedValue({ reviews: [] })
  vi.mocked(api.listExecutions).mockResolvedValue({ executions: [] })
  // Default to "nothing to report" so tests that don't care about the
  // workspace-status banner never see it appear unexpectedly.
  vi.mocked(api.getWorkspaceStatus).mockResolvedValue({
    repository_configured: false,
    status: { behind_origin: { known: false, behind: 0 }, dirty: { known: false, dirty: false } },
  })
}

describe('TaskDetailPanel — stage-conditional rendering', () => {
  it('requirements stage renders GrillMePanel with the right props', async () => {
    stubNoContextOrPlan()
    render(<TaskDetailPanel projectId={projectId} task={makeTask('requirements')} onBack={vi.fn()} />)

    expect(await screen.findByTestId('grillme-panel')).toHaveTextContent('grillme:demo:task-a')
    expect(screen.queryByTestId('planningmode-panel')).not.toBeInTheDocument()
    expect(screen.queryByRole('button', { name: /Revise/ })).not.toBeInTheDocument()
  })

  it('planning stage renders PlanningModePanel and a Revise Requirements button', async () => {
    stubNoContextOrPlan()
    render(<TaskDetailPanel projectId={projectId} task={makeTask('planning')} onBack={vi.fn()} />)

    expect(await screen.findByTestId('planningmode-panel')).toHaveTextContent('planningmode:demo:task-a')
    expect(screen.getByRole('button', { name: 'Revise Requirements' })).toBeInTheDocument()
  })

  it('Revise Requirements calls reviseRequirements and updates the displayed task', async () => {
    const user = userEvent.setup()
    stubNoContextOrPlan()
    const revised = makeTask('requirements')
    vi.mocked(api.reviseRequirements).mockResolvedValue(revised)

    render(<TaskDetailPanel projectId={projectId} task={makeTask('planning')} onBack={vi.fn()} />)
    await user.click(await screen.findByRole('button', { name: 'Revise Requirements' }))

    expect(await screen.findByTestId('grillme-panel')).toBeInTheDocument()
    expect(api.reviseRequirements).toHaveBeenCalledWith(projectId, 'task-a')
  })

  it('shows an inline error when Revise Requirements rejects', async () => {
    const user = userEvent.setup()
    stubNoContextOrPlan()
    vi.mocked(api.reviseRequirements).mockRejectedValue(new Error('task is not in the expected stage'))

    render(<TaskDetailPanel projectId={projectId} task={makeTask('planning')} onBack={vi.fn()} />)
    await user.click(await screen.findByRole('button', { name: 'Revise Requirements' }))

    expect(await screen.findByText('task is not in the expected stage')).toBeInTheDocument()
  })

  it.each(['implementation', 'review'] as const)('%s stage renders a Revise Plan button', async (stage) => {
    stubNoContextOrPlan()
    render(<TaskDetailPanel projectId={projectId} task={makeTask(stage)} onBack={vi.fn()} />)

    await waitFor(() => expect(api.getTaskContext).toHaveBeenCalled())
    expect(screen.queryByTestId('grillme-panel')).not.toBeInTheDocument()
    expect(screen.queryByTestId('planningmode-panel')).not.toBeInTheDocument()
    expect(screen.getByRole('button', { name: 'Revise Plan' })).toBeInTheDocument()
  })

  it('implementation stage renders ExecutePanel; review stage does not', async () => {
    stubNoContextOrPlan()
    const { unmount } = render(<TaskDetailPanel projectId={projectId} task={makeTask('implementation')} onBack={vi.fn()} />)
    expect(await screen.findByTestId('execute-panel')).toHaveTextContent('execute:demo:task-a')
    unmount()

    render(<TaskDetailPanel projectId={projectId} task={makeTask('review')} onBack={vi.fn()} />)
    await waitFor(() => expect(api.getTaskContext).toHaveBeenCalled())
    expect(screen.queryByTestId('execute-panel')).not.toBeInTheDocument()
  })

  it('review stage renders ReviewPanel with the right props', async () => {
    stubNoContextOrPlan()
    render(<TaskDetailPanel projectId={projectId} task={makeTask('review')} onBack={vi.fn()} />)

    expect(await screen.findByTestId('review-panel')).toHaveTextContent('review:demo:task-a')
  })

  it('pr_review stage renders PRReviewPanel with the right props and no Revise Plan button', async () => {
    stubNoContextOrPlan()
    render(<TaskDetailPanel projectId={projectId} task={makeTask('pr_review')} onBack={vi.fn()} />)

    expect(await screen.findByTestId('pr-review-panel')).toHaveTextContent('pr-review:demo:task-a')
    expect(screen.queryByRole('button', { name: 'Revise Plan' })).not.toBeInTheDocument()
  })

  it('merged stage renders neither stage panel nor any Revise button', async () => {
    stubNoContextOrPlan()
    render(<TaskDetailPanel projectId={projectId} task={makeTask('merged')} onBack={vi.fn()} />)

    await waitFor(() => expect(api.getTaskContext).toHaveBeenCalled())
    expect(screen.queryByTestId('grillme-panel')).not.toBeInTheDocument()
    expect(screen.queryByTestId('planningmode-panel')).not.toBeInTheDocument()
    expect(screen.queryByRole('button', { name: /Revise/ })).not.toBeInTheDocument()
  })

  it('merged stage shows the verdict notes and a link to the merged pull request', async () => {
    vi.mocked(api.getTaskContext).mockRejectedValue(new Error('not found'))
    vi.mocked(api.getTaskPlan).mockRejectedValue(new Error('not found'))
    vi.mocked(api.getWorkspaceStatus).mockResolvedValue({
      repository_configured: false,
      status: { behind_origin: { known: false, behind: 0 }, dirty: { known: false, dirty: false } },
    })
    vi.mocked(api.listReviews).mockResolvedValue({
      reviews: [
        { review_id: 'review-001', task_id: 'task-a', execution_id: 'exec-001', decision: 'needs_changes', notes: 'first pass', created_at: '2026-01-01T00:00:00Z' },
        { review_id: 'review-002', task_id: 'task-a', execution_id: 'exec-002', decision: 'approved', notes: 'looks great', created_at: '2026-01-02T00:00:00Z' },
      ],
    })

    const task = makeTask('merged', {
      pull_request: { url: 'https://github.com/org/repo/pull/7', number: 7, branch: 'task-exec/task-a/exec-002' },
    })
    render(<TaskDetailPanel projectId={projectId} task={task} onBack={vi.fn()} />)

    // Shows the latest verdict, not the earlier needs_changes one.
    expect(await screen.findByText(/Review complete — approved/)).toBeInTheDocument()
    expect(screen.getByText('looks great')).toBeInTheDocument()
    const link = screen.getByRole('link', { name: /pull request #7/ })
    expect(link).toHaveAttribute('href', 'https://github.com/org/repo/pull/7')
  })
})

describe('TaskDetailPanel — Context/Plan sections', () => {
  it('renders the Context section once getTaskContext resolves', async () => {
    vi.mocked(api.getTaskContext).mockResolvedValue({
      summary: 'A finalized summary',
      background: '',
      files: [],
      detail: '',
      verification: [],
      open_questions: [],
    })
    vi.mocked(api.getTaskPlan).mockRejectedValue(new Error('not found'))
    vi.mocked(api.getWorkspaceStatus).mockResolvedValue({
      repository_configured: false,
      status: { behind_origin: { known: false, behind: 0 }, dirty: { known: false, dirty: false } },
    })

    render(<TaskDetailPanel projectId={projectId} task={makeTask('planning')} onBack={vi.fn()} />)

    expect(await screen.findByText('A finalized summary')).toBeInTheDocument()
  })

  it('omits the Context/Plan sections when both requests reject (not yet finalized)', async () => {
    stubNoContextOrPlan()
    render(<TaskDetailPanel projectId={projectId} task={makeTask('requirements')} onBack={vi.fn()} />)

    await screen.findByTestId('grillme-panel')
    expect(screen.queryByText('Context')).not.toBeInTheDocument()
    expect(screen.queryByText('Plan')).not.toBeInTheDocument()
  })

  it('renders the Plan section once getTaskPlan resolves', async () => {
    vi.mocked(api.getTaskContext).mockRejectedValue(new Error('not found'))
    vi.mocked(api.getTaskPlan).mockResolvedValue({
      approach: 'A finalized approach',
      steps: ['Step one'],
      risks: [],
      estimated_complexity: 'low',
      recommended_executor: '',
    })
    vi.mocked(api.getWorkspaceStatus).mockResolvedValue({
      repository_configured: false,
      status: { behind_origin: { known: false, behind: 0 }, dirty: { known: false, dirty: false } },
    })

    render(<TaskDetailPanel projectId={projectId} task={makeTask('implementation')} onBack={vi.fn()} />)

    expect(await screen.findByText('A finalized approach')).toBeInTheDocument()
    expect(screen.getByText('Step one')).toBeInTheDocument()
  })
})

describe('TaskDetailPanel — summary/interview zone separation', () => {
  it('wraps the read-only summary and the GrillMe interview in separate containers', async () => {
    stubNoContextOrPlan()
    const task = makeTask('requirements', { objective: 'ship it' })
    const { container } = render(<TaskDetailPanel projectId={projectId} task={task} onBack={vi.fn()} />)

    const grillme = await screen.findByTestId('grillme-panel')
    const summary = container.querySelector('.task-summary')
    const interview = container.querySelector('.task-interview')

    expect(summary).toBeInTheDocument()
    expect(summary).toHaveTextContent('ship it')
    expect(interview).toBeInTheDocument()
    expect(interview).toContainElement(grillme)
    expect(summary).not.toContainElement(grillme)
  })

  it('omits the summary container entirely when there is nothing to summarize', async () => {
    stubNoContextOrPlan()
    const { container } = render(<TaskDetailPanel projectId={projectId} task={makeTask('requirements')} onBack={vi.fn()} />)

    await screen.findByTestId('grillme-panel')
    expect(container.querySelector('.task-summary')).not.toBeInTheDocument()
  })
})

describe('TaskDetailPanel — status select', () => {
  it('changing the status calls updateProjectTask with the full current task fields', async () => {
    const user = userEvent.setup()
    stubNoContextOrPlan()
    const task = makeTask('requirements', { title: 'Task A', objective: 'ship it', constraints: ['no new deps'] })
    const updated = { ...task, status: 'in_progress' as const }
    vi.mocked(api.updateProjectTask).mockResolvedValue(updated)

    render(<TaskDetailPanel projectId={projectId} task={task} onBack={vi.fn()} />)
    await screen.findByTestId('grillme-panel')

    await user.selectOptions(screen.getByDisplayValue('draft'), 'in_progress')

    expect(api.updateProjectTask).toHaveBeenCalledWith(projectId, 'task-a', {
      title: 'Task A',
      status: 'in_progress',
      stage: 'requirements',
      objective: 'ship it',
      constraints: ['no new deps'],
      assumptions: [],
      success_criteria: [],
      references: { knowledge: [], repo: [] },
    })
    await waitFor(() => expect(screen.getByDisplayValue('in_progress')).toBeInTheDocument())
  })
})

describe('TaskDetailPanel — workspace status banner', () => {
  it('shows a note when the shared checkout is behind origin', async () => {
    stubNoContextOrPlan()
    vi.mocked(api.getWorkspaceStatus).mockResolvedValue({
      repository_configured: true,
      status: { behind_origin: { known: true, behind: 3 }, dirty: { known: true, dirty: false } },
    })

    render(<TaskDetailPanel projectId={projectId} task={makeTask('requirements')} onBack={vi.fn()} />)

    expect(await screen.findByText(/3 commits behind origin/)).toBeInTheDocument()
  })

  it('shows a note when the shared checkout is dirty', async () => {
    stubNoContextOrPlan()
    vi.mocked(api.getWorkspaceStatus).mockResolvedValue({
      repository_configured: true,
      status: { behind_origin: { known: true, behind: 0 }, dirty: { known: true, dirty: true } },
    })

    render(<TaskDetailPanel projectId={projectId} task={makeTask('requirements')} onBack={vi.fn()} />)

    expect(await screen.findByText(/uncommitted changes/)).toBeInTheDocument()
  })

  it('shows nothing when the checkout is clean', async () => {
    stubNoContextOrPlan()
    vi.mocked(api.getWorkspaceStatus).mockResolvedValue({
      repository_configured: true,
      status: { behind_origin: { known: true, behind: 0 }, dirty: { known: true, dirty: false } },
    })
    const { container } = render(<TaskDetailPanel projectId={projectId} task={makeTask('requirements')} onBack={vi.fn()} />)

    await screen.findByTestId('grillme-panel')
    expect(screen.queryByText(/behind origin|uncommitted changes/)).not.toBeInTheDocument()
    expect(container.querySelector('.workspace-status-banner')).not.toBeInTheDocument()
  })

  it('shows nothing when there is no repository configured', async () => {
    stubNoContextOrPlan() // default stub already returns repository_configured: false
    const { container } = render(<TaskDetailPanel projectId={projectId} task={makeTask('requirements')} onBack={vi.fn()} />)

    await screen.findByTestId('grillme-panel')
    expect(container.querySelector('.workspace-status-banner')).not.toBeInTheDocument()
  })

  it('shows nothing when the fetch rejects', async () => {
    stubNoContextOrPlan()
    vi.mocked(api.getWorkspaceStatus).mockRejectedValue(new Error('boom'))
    const { container } = render(<TaskDetailPanel projectId={projectId} task={makeTask('requirements')} onBack={vi.fn()} />)

    await screen.findByTestId('grillme-panel')
    expect(container.querySelector('.workspace-status-banner')).not.toBeInTheDocument()
  })
})

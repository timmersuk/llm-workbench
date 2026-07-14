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
  // The complete-stage effect reads the latest verdict + branch; default to
  // empty so tests that don't care about the completion detail don't crash on
  // an unmocked call.
  vi.mocked(api.listReviews).mockResolvedValue({ reviews: [] })
  vi.mocked(api.listExecutions).mockResolvedValue({ executions: [] })
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

  it('complete stage renders neither stage panel nor any Revise button', async () => {
    stubNoContextOrPlan()
    render(<TaskDetailPanel projectId={projectId} task={makeTask('complete')} onBack={vi.fn()} />)

    await waitFor(() => expect(api.getTaskContext).toHaveBeenCalled())
    expect(screen.queryByTestId('grillme-panel')).not.toBeInTheDocument()
    expect(screen.queryByTestId('planningmode-panel')).not.toBeInTheDocument()
    expect(screen.queryByRole('button', { name: /Revise/ })).not.toBeInTheDocument()
  })

  it('complete stage shows the verdict notes and the branch to merge by hand', async () => {
    vi.mocked(api.getTaskContext).mockRejectedValue(new Error('not found'))
    vi.mocked(api.getTaskPlan).mockRejectedValue(new Error('not found'))
    vi.mocked(api.listReviews).mockResolvedValue({
      reviews: [
        { review_id: 'review-001', task_id: 'task-a', decision: 'needs_changes', notes: 'first pass', created_at: '2026-01-01T00:00:00Z' },
        { review_id: 'review-002', task_id: 'task-a', decision: 'approved', notes: 'looks great', created_at: '2026-01-02T00:00:00Z' },
      ],
    })
    vi.mocked(api.listExecutions).mockResolvedValue({
      executions: [
        {
          execution_id: 'exec-002',
          task_id: 'task-a',
          executor: { type: 'claude-code', version: '' },
          input: { plan_ref: 'plan.yaml', context_refs: [] },
          output: { artifacts: [], git_branch: 'wb/task-a/exec-002', commits: [] },
          metrics: { duration_seconds: 1, tokens_used: 0, cost_estimate: 0 },
          status: 'success',
          created_at: '2026-01-02T00:00:00Z',
        },
      ],
    })

    render(<TaskDetailPanel projectId={projectId} task={makeTask('complete')} onBack={vi.fn()} />)

    // Shows the latest verdict, not the earlier needs_changes one.
    expect(await screen.findByText(/Review complete — approved/)).toBeInTheDocument()
    expect(screen.getByText('looks great')).toBeInTheDocument()
    expect(screen.getByText('wb/task-a/exec-002')).toBeInTheDocument()
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

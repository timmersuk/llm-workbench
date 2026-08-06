import { describe, expect, it, vi } from 'vitest'
import { render, screen, waitFor } from '@testing-library/react'
import userEvent from '@testing-library/user-event'
import { TaskDetailPanel } from './TaskDetailPanel'
import * as api from './api'
import type { Execution, Task, TaskStage } from './types'

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
vi.mock('./TimelinePanel', () => ({
  TimelinePanel: (props: { projectId: string; taskId: string }) => (
    <div data-testid="timeline-panel">
      timeline:{props.projectId}:{props.taskId}
    </div>
  ),
}))

const projectId = 'demo'

function makeTask(stage: TaskStage, overrides: Partial<Task> = {}): Task {
  return {
    id: 'task-a',
    title: 'Task A',
    project: projectId,
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

function makeExecution(overrides: Partial<Execution> = {}): Execution {
  return {
    execution_id: 'exec-002',
    task_id: 'task-a',
    executor: { type: 'claude-code', version: '' },
    input: { plan_ref: 'plan.yaml', context_refs: [], review_feedback: '', continued_from_execution_id: '' },
    output: {
      artifacts: [],
      git_branch: 'task-exec/task-a/exec-002',
      commits: ['abc123'],
      forked_from_branch: '',
      workspace_dirty: false,
    },
    metrics: { duration_seconds: 1, tokens_used: 0, cost_estimate: 0 },
    status: 'success',
    created_at: '2026-01-01T00:00:00Z',
    ...overrides,
  }
}

function stubNoContextOrPlan() {
  vi.mocked(api.getTaskContext).mockRejectedValue(new Error('not found'))
  vi.mocked(api.getTaskPlan).mockRejectedValue(new Error('not found'))
  // The completed-stage effect reads the latest verdict; default to empty so
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
  // TaskDetailPanel's Defaults section (AgentDefaultsEditor, collapsed by
  // default but still mounted) fetches executor capabilities on mount
  // regardless of stage; default to one capability per seed executor so
  // every existing test's stubbed task (whose agent_defaults reference
  // "local"/"claude-code") renders a valid, non-stale Defaults section
  // unless a test overrides this itself.
  vi.mocked(api.listExecutorCapabilities).mockResolvedValue({
    executors: [
      { name: 'local', models: ['test-model'], efforts: ['low', 'medium', 'high'], default_model: 'test-model', default_effort: 'medium' },
      { name: 'claude-code', models: ['sonnet', 'opus', 'haiku'], efforts: ['low', 'medium', 'high'], default_model: 'sonnet', default_effort: 'high' },
    ],
  })
}

describe('TaskDetailPanel — stage-conditional rendering', () => {
  it('requirements stage renders GrillMePanel with the right props', async () => {
    stubNoContextOrPlan()
    render(<TaskDetailPanel projectId={projectId} task={makeTask('requirements')} onBack={vi.fn()} onViewDraft={vi.fn()} />)

    expect(await screen.findByTestId('grillme-panel')).toHaveTextContent('grillme:demo:task-a')
    expect(screen.queryByTestId('planningmode-panel')).not.toBeInTheDocument()
    expect(screen.queryByRole('button', { name: /Revise/ })).not.toBeInTheDocument()
  })

  it('planning stage renders PlanningModePanel and a Revise Requirements button', async () => {
    stubNoContextOrPlan()
    render(<TaskDetailPanel projectId={projectId} task={makeTask('planning')} onBack={vi.fn()} onViewDraft={vi.fn()} />)

    expect(await screen.findByTestId('planningmode-panel')).toHaveTextContent('planningmode:demo:task-a')
    expect(screen.getByRole('button', { name: 'Revise Requirements' })).toBeInTheDocument()
  })

  it('Revise Requirements calls reviseRequirements and updates the displayed task', async () => {
    const user = userEvent.setup()
    stubNoContextOrPlan()
    const revised = makeTask('requirements')
    vi.mocked(api.reviseRequirements).mockResolvedValue(revised)

    render(<TaskDetailPanel projectId={projectId} task={makeTask('planning')} onBack={vi.fn()} onViewDraft={vi.fn()} />)
    await user.click(await screen.findByRole('button', { name: 'Revise Requirements' }))

    expect(await screen.findByTestId('grillme-panel')).toBeInTheDocument()
    expect(api.reviseRequirements).toHaveBeenCalledWith(projectId, 'task-a', '')
  })

  it('Revise Requirements passes along a typed reason', async () => {
    const user = userEvent.setup()
    stubNoContextOrPlan()
    vi.mocked(api.reviseRequirements).mockResolvedValue(makeTask('requirements'))

    render(<TaskDetailPanel projectId={projectId} task={makeTask('planning')} onBack={vi.fn()} onViewDraft={vi.fn()} />)
    const textarea = await screen.findByPlaceholderText(/Optional: why send this back/)
    await user.type(textarea, 'the plan missed X')
    await user.click(screen.getByRole('button', { name: 'Revise Requirements' }))

    expect(api.reviseRequirements).toHaveBeenCalledWith(projectId, 'task-a', 'the plan missed X')
  })

  it('shows an inline error when Revise Requirements rejects', async () => {
    const user = userEvent.setup()
    stubNoContextOrPlan()
    vi.mocked(api.reviseRequirements).mockRejectedValue(new Error('task is not in the expected stage'))

    render(<TaskDetailPanel projectId={projectId} task={makeTask('planning')} onBack={vi.fn()} onViewDraft={vi.fn()} />)
    await user.click(await screen.findByRole('button', { name: 'Revise Requirements' }))

    expect(await screen.findByText('task is not in the expected stage')).toBeInTheDocument()
  })

  it.each(['implementation', 'review'] as const)('%s stage renders a Revise Plan button', async (stage) => {
    stubNoContextOrPlan()
    render(<TaskDetailPanel projectId={projectId} task={makeTask(stage)} onBack={vi.fn()} onViewDraft={vi.fn()} />)

    await waitFor(() => expect(api.getTaskContext).toHaveBeenCalled())
    expect(screen.queryByTestId('grillme-panel')).not.toBeInTheDocument()
    expect(screen.queryByTestId('planningmode-panel')).not.toBeInTheDocument()
    expect(screen.getByRole('button', { name: 'Revise Plan' })).toBeInTheDocument()
  })

  it('implementation stage renders ExecutePanel; review stage does not', async () => {
    stubNoContextOrPlan()
    const { unmount } = render(<TaskDetailPanel projectId={projectId} task={makeTask('implementation')} onBack={vi.fn()} onViewDraft={vi.fn()} />)
    expect(await screen.findByTestId('execute-panel')).toHaveTextContent('execute:demo:task-a')
    unmount()

    render(<TaskDetailPanel projectId={projectId} task={makeTask('review')} onBack={vi.fn()} onViewDraft={vi.fn()} />)
    await waitFor(() => expect(api.getTaskContext).toHaveBeenCalled())
    expect(screen.queryByTestId('execute-panel')).not.toBeInTheDocument()
  })

  it('review stage renders ReviewPanel with the right props', async () => {
    stubNoContextOrPlan()
    render(<TaskDetailPanel projectId={projectId} task={makeTask('review')} onBack={vi.fn()} onViewDraft={vi.fn()} />)

    expect(await screen.findByTestId('review-panel')).toHaveTextContent('review:demo:task-a')
  })

  it('pr_review stage renders PRReviewPanel with the right props and no Revise Plan button', async () => {
    stubNoContextOrPlan()
    render(<TaskDetailPanel projectId={projectId} task={makeTask('pr_review')} onBack={vi.fn()} onViewDraft={vi.fn()} />)

    expect(await screen.findByTestId('pr-review-panel')).toHaveTextContent('pr-review:demo:task-a')
    expect(screen.queryByRole('button', { name: 'Revise Plan' })).not.toBeInTheDocument()
  })

  it('completed stage renders neither stage panel nor any Revise button', async () => {
    stubNoContextOrPlan()
    render(<TaskDetailPanel projectId={projectId} task={makeTask('completed')} onBack={vi.fn()} onViewDraft={vi.fn()} />)

    await waitFor(() => expect(api.getTaskContext).toHaveBeenCalled())
    expect(screen.queryByTestId('grillme-panel')).not.toBeInTheDocument()
    expect(screen.queryByTestId('planningmode-panel')).not.toBeInTheDocument()
    expect(screen.queryByRole('button', { name: /Revise/ })).not.toBeInTheDocument()
  })

  it('completed stage shows the verdict notes and a link to the merged pull request', async () => {
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

    const task = makeTask('completed', {
      pull_request: { url: 'https://github.com/org/repo/pull/7', number: 7, branch: 'task-exec/task-a/exec-002' },
    })
    render(<TaskDetailPanel projectId={projectId} task={task} onBack={vi.fn()} onViewDraft={vi.fn()} />)

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

    render(<TaskDetailPanel projectId={projectId} task={makeTask('planning')} onBack={vi.fn()} onViewDraft={vi.fn()} />)

    expect(await screen.findByText('A finalized summary')).toBeInTheDocument()
  })

  it('omits the Context/Plan sections when both requests reject (not yet finalized)', async () => {
    stubNoContextOrPlan()
    render(<TaskDetailPanel projectId={projectId} task={makeTask('requirements')} onBack={vi.fn()} onViewDraft={vi.fn()} />)

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

    render(<TaskDetailPanel projectId={projectId} task={makeTask('implementation')} onBack={vi.fn()} onViewDraft={vi.fn()} />)

    expect(await screen.findByText('A finalized approach')).toBeInTheDocument()
    expect(screen.getByText('Step one')).toBeInTheDocument()
  })
})

describe('TaskDetailPanel — reference sections open/highlight the one relevant to the current stage', () => {
  function stubContextAndPlan() {
    vi.mocked(api.getTaskContext).mockResolvedValue({
      summary: 'A finalized summary',
      background: '',
      files: [],
      detail: '',
      verification: [],
      open_questions: [],
    })
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
  }

  function sectionFor(text: string): HTMLDetailsElement {
    return screen.getByText(text).closest('details') as HTMLDetailsElement
  }

  it('opens and badges Requirements/Context, collapses Plan, while in requirements', async () => {
    stubContextAndPlan()
    const task = makeTask('requirements', { objective: 'ship it' })
    render(<TaskDetailPanel projectId={projectId} task={task} onBack={vi.fn()} onViewDraft={vi.fn()} />)

    await screen.findByText('A finalized summary')

    const requirements = sectionFor('Requirements')
    const context = sectionFor('Context')
    const plan = sectionFor('Plan')

    expect(requirements.open).toBe(true)
    expect(context.open).toBe(true)
    expect(plan.open).toBe(false)
    expect(requirements.querySelector('.task-section-current-badge')).toBeInTheDocument()
    expect(context.querySelector('.task-section-current-badge')).toBeInTheDocument()
    expect(plan.querySelector('.task-section-current-badge')).not.toBeInTheDocument()
  })

  it('opens and badges Plan, collapses Requirements/Context, once in implementation', async () => {
    stubContextAndPlan()
    const task = makeTask('implementation', { objective: 'ship it' })
    render(<TaskDetailPanel projectId={projectId} task={task} onBack={vi.fn()} onViewDraft={vi.fn()} />)

    await screen.findByText('A finalized summary')

    const requirements = sectionFor('Requirements')
    const context = sectionFor('Context')
    const plan = sectionFor('Plan')

    expect(requirements.open).toBe(false)
    expect(context.open).toBe(false)
    expect(plan.open).toBe(true)
    expect(requirements.querySelector('.task-section-current-badge')).not.toBeInTheDocument()
    expect(plan.querySelector('.task-section-current-badge')).toBeInTheDocument()
  })

  it('collapses every reference section once completed — nothing is "current" anymore', async () => {
    stubContextAndPlan()
    vi.mocked(api.listReviews).mockResolvedValue({ reviews: [] })
    const task = makeTask('completed', { objective: 'ship it' })
    render(<TaskDetailPanel projectId={projectId} task={task} onBack={vi.fn()} onViewDraft={vi.fn()} />)

    await screen.findByText('A finalized summary')

    expect(sectionFor('Requirements').open).toBe(false)
    expect(sectionFor('Context').open).toBe(false)
    expect(sectionFor('Plan').open).toBe(false)
    expect(document.querySelector('.task-section-current-badge')).not.toBeInTheDocument()
  })
})

describe('TaskDetailPanel — summary/interview zone separation', () => {
  it('wraps the read-only Requirements section and the GrillMe interview in separate containers', async () => {
    stubNoContextOrPlan()
    const task = makeTask('requirements', { objective: 'ship it' })
    const { container } = render(<TaskDetailPanel projectId={projectId} task={task} onBack={vi.fn()} onViewDraft={vi.fn()} />)

    const grillme = await screen.findByTestId('grillme-panel')
    const summary = container.querySelector('.task-section')
    const interview = container.querySelector('.task-interview')

    expect(summary).toBeInTheDocument()
    expect(summary).toHaveTextContent('ship it')
    expect(interview).toBeInTheDocument()
    expect(interview).toContainElement(grillme)
    expect(summary).not.toContainElement(grillme)
  })

  it('omits the Requirements section entirely when there is nothing to summarize', async () => {
    stubNoContextOrPlan()
    render(<TaskDetailPanel projectId={projectId} task={makeTask('requirements')} onBack={vi.fn()} onViewDraft={vi.fn()} />)

    await screen.findByTestId('grillme-panel')
    // Requirements/Context/Plan are conditionally rendered only when there's
    // content to summarize (unlike Defaults, which is always present) — so
    // this checks specifically for the Requirements section's own summary
    // text rather than any .task-section, which Defaults would also match.
    expect(screen.queryByText('Requirements')).not.toBeInTheDocument()
  })
})

describe('TaskDetailPanel — workspace status banner', () => {
  it('shows a note when the shared checkout is behind origin', async () => {
    stubNoContextOrPlan()
    vi.mocked(api.getWorkspaceStatus).mockResolvedValue({
      repository_configured: true,
      status: { behind_origin: { known: true, behind: 3 }, dirty: { known: true, dirty: false } },
    })

    render(<TaskDetailPanel projectId={projectId} task={makeTask('requirements')} onBack={vi.fn()} onViewDraft={vi.fn()} />)

    expect(await screen.findByText(/3 commits behind origin/)).toBeInTheDocument()
  })

  it('shows a note when the shared checkout is dirty', async () => {
    stubNoContextOrPlan()
    vi.mocked(api.getWorkspaceStatus).mockResolvedValue({
      repository_configured: true,
      status: { behind_origin: { known: true, behind: 0 }, dirty: { known: true, dirty: true } },
    })

    render(<TaskDetailPanel projectId={projectId} task={makeTask('requirements')} onBack={vi.fn()} onViewDraft={vi.fn()} />)

    expect(await screen.findByText(/uncommitted changes/)).toBeInTheDocument()
  })

  it('shows nothing when the checkout is clean', async () => {
    stubNoContextOrPlan()
    vi.mocked(api.getWorkspaceStatus).mockResolvedValue({
      repository_configured: true,
      status: { behind_origin: { known: true, behind: 0 }, dirty: { known: true, dirty: false } },
    })
    const { container } = render(<TaskDetailPanel projectId={projectId} task={makeTask('requirements')} onBack={vi.fn()} onViewDraft={vi.fn()} />)

    await screen.findByTestId('grillme-panel')
    expect(screen.queryByText(/behind origin|uncommitted changes/)).not.toBeInTheDocument()
    expect(container.querySelector('.workspace-status-banner')).not.toBeInTheDocument()
  })

  it('shows nothing when there is no repository configured', async () => {
    stubNoContextOrPlan() // default stub already returns repository_configured: false
    const { container } = render(<TaskDetailPanel projectId={projectId} task={makeTask('requirements')} onBack={vi.fn()} onViewDraft={vi.fn()} />)

    await screen.findByTestId('grillme-panel')
    expect(container.querySelector('.workspace-status-banner')).not.toBeInTheDocument()
  })

  it('shows nothing when the fetch rejects', async () => {
    stubNoContextOrPlan()
    vi.mocked(api.getWorkspaceStatus).mockRejectedValue(new Error('boom'))
    const { container } = render(<TaskDetailPanel projectId={projectId} task={makeTask('requirements')} onBack={vi.fn()} onViewDraft={vi.fn()} />)

    await screen.findByTestId('grillme-panel')
    expect(container.querySelector('.workspace-status-banner')).not.toBeInTheDocument()
  })
})

describe('TaskDetailPanel — execution outcome notices', () => {
  it('shows a plain-language notice when the last execution hit its turn limit', async () => {
    stubNoContextOrPlan()
    vi.mocked(api.listExecutions).mockResolvedValue({
      executions: [makeExecution({ status: 'failure', failure: { type: 'execution', message: 'Reached maximum number of turns (100)' } })],
    })

    render(<TaskDetailPanel projectId={projectId} task={makeTask('implementation')} onBack={vi.fn()} onViewDraft={vi.fn()} />)

    expect(await screen.findByText(/stopped after hitting its turn limit/)).toBeInTheDocument()
  })

  it('shows the raw failure message for a non-turn-limit failure', async () => {
    stubNoContextOrPlan()
    vi.mocked(api.listExecutions).mockResolvedValue({
      executions: [makeExecution({ status: 'failure', failure: { type: 'resource', message: 'context deadline exceeded' } })],
    })

    render(<TaskDetailPanel projectId={projectId} task={makeTask('implementation')} onBack={vi.fn()} onViewDraft={vi.fn()} />)

    expect(await screen.findByText(/context deadline exceeded/)).toBeInTheDocument()
  })

  it('folds a successful-but-dirty execution into the workspace advisory banner', async () => {
    stubNoContextOrPlan()
    vi.mocked(api.listExecutions).mockResolvedValue({
      executions: [makeExecution({ status: 'success', output: { ...makeExecution().output, workspace_dirty: true } })],
    })

    const { container } = render(<TaskDetailPanel projectId={projectId} task={makeTask('implementation')} onBack={vi.fn()} onViewDraft={vi.fn()} />)

    expect(await screen.findByText(/succeeded but left uncommitted changes/)).toBeInTheDocument()
    expect(container.querySelector('.workspace-status-banner')).toBeInTheDocument()
    expect(container.querySelector('.execution-failure-banner')).not.toBeInTheDocument()
  })

  it('shows no notice at all for a clean successful execution', async () => {
    stubNoContextOrPlan()
    vi.mocked(api.listExecutions).mockResolvedValue({ executions: [makeExecution({ status: 'success' })] })

    const { container } = render(<TaskDetailPanel projectId={projectId} task={makeTask('implementation')} onBack={vi.fn()} onViewDraft={vi.fn()} />)

    await screen.findByTestId('execute-panel')
    expect(container.querySelector('.execution-failure-banner')).not.toBeInTheDocument()
    expect(container.querySelector('.workspace-status-banner')).not.toBeInTheDocument()
  })
})

describe('TaskDetailPanel — Knowledge section', () => {
  it('shows a dedicated Knowledge section when the task has knowledge_activity entries', async () => {
    stubNoContextOrPlan()
    const task = makeTask('review', {
      knowledge_activity: [{ concept_id: 'coding-standards/logging', type: 'Coding Standard', action: 'created', created_at: '2026-01-01T00:00:00Z' }],
    })

    render(<TaskDetailPanel projectId={projectId} task={task} onBack={vi.fn()} onViewDraft={vi.fn()} />)

    expect(await screen.findByText('Knowledge')).toBeInTheDocument()
    expect(screen.getByText(/knowledge concept created: coding-standards\/logging/)).toBeInTheDocument()
  })

  it('omits the Knowledge section when the task has no knowledge_activity', async () => {
    stubNoContextOrPlan()
    render(<TaskDetailPanel projectId={projectId} task={makeTask('review')} onBack={vi.fn()} onViewDraft={vi.fn()} />)

    await screen.findByTestId('review-panel')
    expect(screen.queryByText('Knowledge')).not.toBeInTheDocument()
  })
})

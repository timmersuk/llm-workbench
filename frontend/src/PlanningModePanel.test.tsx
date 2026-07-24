import { beforeEach, describe, expect, it, vi } from 'vitest'
import { act, render, screen, waitFor } from '@testing-library/react'
import userEvent from '@testing-library/user-event'
import { PlanningModePanel } from './PlanningModePanel'
import * as api from './api'
import type { ChatStreamEvent, Task, TaskPlan } from './types'

vi.mock('./api')

const projectId = 'demo'
const taskId = 'task-a'

function makeTask(overrides: Partial<Task> = {}): Task {
  return {
    id: taskId,
    title: 'Task A',
    project: projectId,
    stage: 'implementation',
    created_at: '2026-01-01T00:00:00Z',
    updated_at: '2026-01-01T00:00:00Z',
    objective: 'ship it',
    constraints: [],
    assumptions: [],
    success_criteria: [],
    references: { knowledge: [], repo: [] },
    ...overrides,
  }
}

describe('PlanningModePanel', () => {
  it('mounts and renders the model select and empty transcript, waiting for an explicit Start Planning', async () => {
    vi.mocked(api.listModels).mockResolvedValue({ models: ['model-a'] })
    vi.mocked(api.listAgentExecutors).mockResolvedValue({ executors: ['local'] })
    vi.mocked(api.getStageConversation).mockResolvedValue({ stage: 'planning', messages: [] })

    render(<PlanningModePanel projectId={projectId} taskId={taskId} onFinalized={vi.fn()} />)

    await waitFor(() => expect(screen.getByLabelText('Model')).toHaveValue('model-a'))
    // Mounting must not have kicked off the turn on its own — autoStart is
    // false, so the Reply textarea only appears once Start Planning is
    // clicked.
    expect(screen.queryByPlaceholderText('Reply...')).not.toBeInTheDocument()
    expect(api.startStageConversation).not.toHaveBeenCalled()

    await userEvent.click(await screen.findByRole('button', { name: 'Start Planning' }))
    expect(screen.getByPlaceholderText('Reply...')).toBeInTheDocument()
  })

  it('a Finalize round-trip calls finalizePlan and reports the result via onFinalized', async () => {
    const user = userEvent.setup()
    vi.mocked(api.listModels).mockResolvedValue({ models: ['model-a'] })
    vi.mocked(api.listAgentExecutors).mockResolvedValue({ executors: [] })
    vi.mocked(api.getStageConversation).mockResolvedValue({ stage: 'planning', messages: [] })

    const resultTask = makeTask()
    const resultPlan: TaskPlan = { approach: 'x', steps: [], risks: [], estimated_complexity: 'low', recommended_executor: '' }
    vi.mocked(api.finalizePlan).mockResolvedValue({ task: resultTask, plan: resultPlan })

    let deliver!: (event: ChatStreamEvent) => void
    vi.mocked(api.postStageMessage).mockImplementation((_p, _t, _s, _c, _m, _e, onEvent) => {
      deliver = onEvent
      return Promise.resolve()
    })

    const onFinalized = vi.fn()
    render(<PlanningModePanel projectId={projectId} taskId={taskId} onFinalized={onFinalized} />)
    await user.click(await screen.findByRole('button', { name: 'Start Planning' }))

    await user.type(screen.getByPlaceholderText('Reply...'), 'Please propose a plan')
    await user.click(screen.getByRole('button', { name: 'Send' }))
    act(() => deliver({ tool_call: { name: 'propose_plan', arguments: JSON.stringify({ approach: 'x' }) } }))

    await user.click(await screen.findByRole('button', { name: 'Finalize' }))

    await waitFor(() => expect(onFinalized).toHaveBeenCalledWith(resultTask, resultPlan))
  })
})

describe('PlanningModePanel — ask_question', () => {
  beforeEach(() => {
    vi.mocked(api.listModels).mockResolvedValue({ models: ['model-a'] })
    vi.mocked(api.listAgentExecutors).mockResolvedValue({ executors: [] })
  })

  it('renders option buttons with the recommendation highlighted, and clicking one sends its label like a typed reply', async () => {
    const user = userEvent.setup()
    vi.mocked(api.getStageConversation).mockResolvedValue({ stage: 'planning', messages: [] })

    let deliver!: (event: ChatStreamEvent) => void
    vi.mocked(api.postStageMessage).mockImplementation((_p, _t, _s, _c, _m, _e, onEvent) => {
      deliver = onEvent
      return Promise.resolve()
    })

    render(<PlanningModePanel projectId={projectId} taskId={taskId} onFinalized={vi.fn()} />)
    await user.click(await screen.findByRole('button', { name: 'Start Planning' }))

    await user.type(screen.getByPlaceholderText('Reply...'), 'go ahead')
    await user.click(screen.getByRole('button', { name: 'Send' }))
    act(() =>
      deliver({
        tool_call: {
          name: 'ask_question',
          arguments: JSON.stringify({ options: ['Small PRs', 'One big PR'], recommended_option: 'Small PRs', recommendation_reason: 'Easier to review' }),
        },
      }),
    )

    const recommended = screen.getByRole('button', { name: /Small PRs/ })
    expect(recommended).toHaveClass('ask-question-option-recommended')
    expect(screen.getByRole('button', { name: 'One big PR' })).not.toHaveClass('ask-question-option-recommended')

    vi.mocked(api.postStageMessage).mockResolvedValue()
    await user.click(recommended)

    await waitFor(() =>
      expect(api.postStageMessage).toHaveBeenLastCalledWith(
        projectId,
        taskId,
        'planning',
        'Small PRs',
        expect.anything(),
        expect.anything(),
        expect.anything(),
        expect.anything(),
      ),
    )
    expect(screen.queryByRole('button', { name: 'One big PR' })).not.toBeInTheDocument()
  })

  it('only rehydrates pending options when ask_question is the last loaded message, and free text still works alongside them', async () => {
    vi.mocked(api.getStageConversation).mockResolvedValue({
      stage: 'planning',
      messages: [
        {
          role: 'assistant',
          content: 'How should we structure the PRs?',
          tool_call: {
            id: 'call-1',
            name: 'ask_question',
            arguments: JSON.stringify({ options: ['Small PRs', 'One big PR'], recommended_option: 'Small PRs', recommendation_reason: 'Easier to review' }),
          },
          created_at: '2026-01-01T00:00:00Z',
        },
      ],
    })

    const user = userEvent.setup()
    render(<PlanningModePanel projectId={projectId} taskId={taskId} onFinalized={vi.fn()} />)

    expect(await screen.findByRole('button', { name: /Small PRs/ })).toBeInTheDocument()

    vi.mocked(api.postStageMessage).mockResolvedValue()
    const textarea = screen.getByPlaceholderText('Reply...')
    expect(textarea).not.toBeDisabled()
    await user.type(textarea, 'Neither, split by risk{Enter}')

    await waitFor(() => expect(api.postStageMessage).toHaveBeenCalled())
    expect(screen.queryByRole('button', { name: /Small PRs/ })).not.toBeInTheDocument()
  })
})

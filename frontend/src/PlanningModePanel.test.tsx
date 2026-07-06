import { describe, expect, it, vi } from 'vitest'
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
    status: 'draft',
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
  it('mounts and renders the model select and empty transcript', async () => {
    vi.mocked(api.listModels).mockResolvedValue({ models: ['model-a'] })
    vi.mocked(api.getStageConversation).mockResolvedValue({ stage: 'planning', messages: [] })

    render(<PlanningModePanel projectId={projectId} taskId={taskId} onFinalized={vi.fn()} />)

    await waitFor(() => expect(screen.getByLabelText('Model')).toHaveValue('model-a'))
    expect(screen.getByPlaceholderText('Reply...')).toBeInTheDocument()
  })

  it('a Finalize round-trip calls finalizePlan and reports the result via onFinalized', async () => {
    const user = userEvent.setup()
    vi.mocked(api.listModels).mockResolvedValue({ models: ['model-a'] })
    vi.mocked(api.getStageConversation).mockResolvedValue({ stage: 'planning', messages: [] })

    const resultTask = makeTask()
    const resultPlan: TaskPlan = { approach: 'x', steps: [], risks: [], estimated_complexity: 'low', recommended_executor: '' }
    vi.mocked(api.finalizePlan).mockResolvedValue({ task: resultTask, plan: resultPlan })

    let deliver!: (event: ChatStreamEvent) => void
    vi.mocked(api.postStageMessage).mockImplementation((_p, _t, _s, _c, _m, onEvent) => {
      deliver = onEvent
      return Promise.resolve()
    })

    const onFinalized = vi.fn()
    render(<PlanningModePanel projectId={projectId} taskId={taskId} onFinalized={onFinalized} />)
    await waitFor(() => expect(api.getStageConversation).toHaveBeenCalled())

    await user.type(screen.getByPlaceholderText('Reply...'), 'Please propose a plan')
    await user.click(screen.getByRole('button', { name: 'Send' }))
    act(() => deliver({ tool_call: { name: 'propose_plan', arguments: JSON.stringify({ approach: 'x' }) } }))

    await user.click(await screen.findByRole('button', { name: 'Finalize' }))

    await waitFor(() => expect(onFinalized).toHaveBeenCalledWith(resultTask, resultPlan))
  })
})

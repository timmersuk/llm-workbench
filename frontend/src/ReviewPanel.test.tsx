import { describe, expect, it, vi } from 'vitest'
import { act, render, screen, waitFor } from '@testing-library/react'
import userEvent from '@testing-library/user-event'
import { ReviewPanel } from './ReviewPanel'
import * as api from './api'
import type { ChatStreamEvent, Execution, Review, Task } from './types'

vi.mock('./api')

const projectId = 'demo'
const taskId = 'task-a'

function makeExecution(overrides: Partial<Execution['output']> = {}): Execution {
  return {
    execution_id: 'exec-001',
    task_id: taskId,
    executor: { type: 'claude-code', version: '' },
    input: { plan_ref: 'plan.yaml', context_refs: [] },
    output: { artifacts: ['feature.go'], git_branch: 'wb/task-a/exec-001', commits: ['add feature'], ...overrides },
    metrics: { duration_seconds: 1, tokens_used: 0, cost_estimate: 0 },
    status: 'success',
    created_at: '2026-01-01T00:00:00Z',
  }
}

function stubConversation() {
  vi.mocked(api.getStageConversation).mockResolvedValue({ stage: 'review', messages: [] })
  vi.mocked(api.listModels).mockResolvedValue({ models: ['model-a'] })
  vi.mocked(api.listAgentExecutors).mockResolvedValue({ executors: [] })
}

describe('ReviewPanel', () => {
  it('shows the execution summary and the diff on demand', async () => {
    stubConversation()
    vi.mocked(api.listExecutions).mockResolvedValue({ executions: [makeExecution()] })
    vi.mocked(api.getReviewDiff).mockResolvedValue({ patch: 'diff --git a/feature.go b/feature.go\n+new line' })

    render(<ReviewPanel projectId={projectId} taskId={taskId} onFinalized={vi.fn()} />)

    expect(await screen.findByText(/wb\/task-a\/exec-001/)).toBeInTheDocument()
    expect(screen.getByText('add feature')).toBeInTheDocument()
    expect(screen.getByText('feature.go')).toBeInTheDocument()
    // The raw patch is present but tucked inside a collapsed "View diff".
    expect(screen.getByText('View diff')).toBeInTheDocument()
    expect(screen.getByText(/\+new line/)).toBeInTheDocument()
  })

  it('does not auto-start the conversation — it waits for an explicit Start Review', async () => {
    stubConversation()
    vi.mocked(api.listExecutions).mockResolvedValue({ executions: [makeExecution()] })
    vi.mocked(api.getReviewDiff).mockResolvedValue({ patch: '' })
    vi.mocked(api.startStageConversation).mockResolvedValue()

    render(<ReviewPanel projectId={projectId} taskId={taskId} onFinalized={vi.fn()} />)

    const start = await screen.findByRole('button', { name: 'Start Review' })
    // Crucially, mounting must not have kicked off the (test-suite-running)
    // review turn on its own.
    expect(api.startStageConversation).not.toHaveBeenCalled()
    expect(screen.queryByPlaceholderText('Reply...')).not.toBeInTheDocument()

    await userEvent.click(start)
    expect(api.startStageConversation).toHaveBeenCalled()
  })

  it('a Finalize round-trip calls finalizeReview and reports the result via onFinalized', async () => {
    const user = userEvent.setup()
    stubConversation()
    vi.mocked(api.listExecutions).mockResolvedValue({ executions: [makeExecution()] })
    vi.mocked(api.getReviewDiff).mockResolvedValue({ patch: '' })

    const resultTask = { id: taskId, stage: 'complete' } as Task
    const resultReview: Review = {
      review_id: 'review-001',
      task_id: taskId,
      decision: 'approved',
      notes: 'lgtm',
      created_at: '2026-01-01T00:00:00Z',
    }
    vi.mocked(api.finalizeReview).mockResolvedValue({ task: resultTask, review: resultReview })

    let deliver!: (event: ChatStreamEvent) => void
    vi.mocked(api.startStageConversation).mockImplementation((_p, _t, _s, _m, _e, onEvent) => {
      deliver = onEvent
      return Promise.resolve()
    })

    const onFinalized = vi.fn()
    render(<ReviewPanel projectId={projectId} taskId={taskId} onFinalized={onFinalized} />)

    await user.click(await screen.findByRole('button', { name: 'Start Review' }))
    act(() => deliver({ tool_call: { name: 'propose_review', arguments: JSON.stringify({ decision: 'approved', notes: 'lgtm' }) } }))

    await user.click(await screen.findByRole('button', { name: 'Finalize' }))

    await waitFor(() => expect(api.finalizeReview).toHaveBeenCalledWith(projectId, taskId, { decision: 'approved', notes: 'lgtm' }))
    expect(onFinalized).toHaveBeenCalledWith(resultTask, resultReview)
  })
})

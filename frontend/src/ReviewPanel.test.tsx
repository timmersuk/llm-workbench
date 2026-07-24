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
    input: { plan_ref: 'plan.yaml', context_refs: [], review_feedback: '', continued_from_execution_id: '' },
    output: {
      artifacts: ['feature.go'],
      git_branch: 'wb/task-a/exec-001',
      commits: ['add feature'],
      forked_from_branch: '',
      workspace_dirty: false,
      ...overrides,
    },
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

    const resultTask = { id: taskId, stage: 'merged' } as Task
    const resultReview: Review = {
      review_id: 'review-001',
      task_id: taskId,
      execution_id: 'exec-001',
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

  it('a propose_knowledge call surfaces independently of the review verdict, and Accept calls finalizeKnowledge', async () => {
    const user = userEvent.setup()
    stubConversation()
    vi.mocked(api.listExecutions).mockResolvedValue({ executions: [makeExecution()] })
    vi.mocked(api.getReviewDiff).mockResolvedValue({ patch: '' })
    vi.mocked(api.finalizeKnowledge).mockResolvedValue({ concept_id: 'coding-standards/logging', decision: 'accepted' })

    let deliver!: (event: ChatStreamEvent) => void
    vi.mocked(api.startStageConversation).mockImplementation((_p, _t, _s, _m, _e, onEvent) => {
      deliver = onEvent
      return Promise.resolve()
    })

    render(<ReviewPanel projectId={projectId} taskId={taskId} onFinalized={vi.fn()} />)

    await user.click(await screen.findByRole('button', { name: 'Start Review' }))
    act(() =>
      deliver({
        tool_call: {
          name: 'propose_knowledge',
          arguments: JSON.stringify({ concept_id: 'coding-standards/logging', type: 'Coding Standard', body: 'Use structured logging.' }),
        },
      }),
    )

    expect(await screen.findByText('Proposed knowledge concept')).toBeInTheDocument()
    expect(screen.getByLabelText('Concept ID')).toHaveValue('coding-standards/logging')
    // Finalize (the review verdict's own action) must not be present/confused
    // with the knowledge draft's Accept/Reject — no propose_review call has
    // happened yet, so there's nothing to finalize.
    expect(screen.queryByRole('button', { name: 'Finalize' })).not.toBeInTheDocument()

    await user.click(screen.getByRole('button', { name: 'Accept' }))

    await waitFor(() =>
      expect(api.finalizeKnowledge).toHaveBeenCalledWith(
        projectId,
        taskId,
        { concept_id: 'coding-standards/logging', type: 'Coding Standard', frontmatter: {}, body: 'Use structured logging.' },
        'accepted',
      ),
    )
    // Accepted — the form clears back out.
    await waitFor(() => expect(screen.queryByText('Proposed knowledge concept')).not.toBeInTheDocument())
  })

  it('a review verdict and a knowledge proposal can be pending at the same time, decided independently', async () => {
    const user = userEvent.setup()
    stubConversation()
    vi.mocked(api.listExecutions).mockResolvedValue({ executions: [makeExecution()] })
    vi.mocked(api.getReviewDiff).mockResolvedValue({ patch: '' })
    vi.mocked(api.finalizeKnowledge).mockResolvedValue({ concept_id: 'x', decision: 'rejected' })

    let deliver!: (event: ChatStreamEvent) => void
    vi.mocked(api.startStageConversation).mockImplementation((_p, _t, _s, _m, _e, onEvent) => {
      deliver = onEvent
      return Promise.resolve()
    })

    render(<ReviewPanel projectId={projectId} taskId={taskId} onFinalized={vi.fn()} />)

    await user.click(await screen.findByRole('button', { name: 'Start Review' }))
    act(() => deliver({ tool_call: { name: 'propose_review', arguments: JSON.stringify({ decision: 'needs_changes', notes: 'fix x' }) } }))
    act(() =>
      deliver({ tool_call: { name: 'propose_knowledge', arguments: JSON.stringify({ concept_id: 'x', type: 'Reference', body: 'y' }) } }),
    )

    // Both proposals are visible at once.
    expect(await screen.findByRole('button', { name: 'Finalize' })).toBeInTheDocument()
    expect(screen.getByText('Proposed knowledge concept')).toBeInTheDocument()

    await user.click(screen.getByRole('button', { name: 'Reject' }))
    await waitFor(() =>
      expect(api.finalizeKnowledge).toHaveBeenCalledWith(projectId, taskId, { concept_id: 'x', type: 'Reference', frontmatter: {}, body: 'y' }, 'rejected'),
    )

    // Rejecting the knowledge draft must not touch the still-pending review verdict.
    expect(api.finalizeReview).not.toHaveBeenCalled()
    expect(screen.getByRole('button', { name: 'Finalize' })).toBeInTheDocument()
  })
})

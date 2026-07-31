import { describe, expect, it, vi } from 'vitest'
import { act, render, screen, waitFor, within } from '@testing-library/react'
import userEvent from '@testing-library/user-event'
import { ReviewPanel } from './ReviewPanel'
import * as api from './api'
import type { ChatStreamEvent, Execution, Review, Task } from './types'

vi.mock('./api')

const projectId = 'demo'
const taskId = 'task-a'

const SAMPLE_PATCH = `diff --git a/feature.go b/feature.go
index 1234567..89abcde 100644
--- a/feature.go
+++ b/feature.go
@@ -1,3 +1,4 @@
 package main

+// new line
 func main() {}
`

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
  vi.mocked(api.listStageTransitions).mockResolvedValue({ stage_transitions: [] })
}

describe('ReviewPanel', () => {
  it('shows the execution summary and the diff, broken out per file', async () => {
    stubConversation()
    vi.mocked(api.listExecutions).mockResolvedValue({ executions: [makeExecution()] })
    vi.mocked(api.getReviewDiff).mockResolvedValue({ patch: SAMPLE_PATCH })

    render(<ReviewPanel projectId={projectId} taskId={taskId} onFinalized={vi.fn()} />)

    expect(await screen.findByText(/wb\/task-a\/exec-001/)).toBeInTheDocument()
    expect(screen.getByText('add feature')).toBeInTheDocument()
    // Once from the "Changed files" summary list, once as the diff section's
    // per-file heading.
    expect(screen.getAllByText('feature.go')).toHaveLength(2)
    // Scoped to the diff itself — StageConversationPanel's unrelated
    // "Enter to send · Alt+Enter for a new line" hint also matches /new line/.
    const diffView = document.querySelector<HTMLElement>('.diff-view')!
    expect(within(diffView).getByText(/new line/)).toBeInTheDocument()
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

  it('hides the reply box while a verdict is pending, renders decision/notes read-only, and Request changes/Dismiss both just reopen the conversation with no API call', async () => {
    const user = userEvent.setup()
    stubConversation()
    vi.mocked(api.listExecutions).mockResolvedValue({ executions: [makeExecution()] })
    vi.mocked(api.getReviewDiff).mockResolvedValue({ patch: '' })

    let deliver!: (event: ChatStreamEvent) => void
    vi.mocked(api.startStageConversation).mockImplementation((_p, _t, _s, _m, _e, onEvent) => {
      deliver = onEvent
      return Promise.resolve()
    })

    render(<ReviewPanel projectId={projectId} taskId={taskId} onFinalized={vi.fn()} />)

    await user.click(await screen.findByRole('button', { name: 'Start Review' }))
    expect(screen.getByPlaceholderText('Reply...')).toBeInTheDocument()

    act(() => deliver({ tool_call: { name: 'propose_review', arguments: JSON.stringify({ decision: 'needs_changes', notes: 'fix the thing' }) } }))

    // Once a verdict is proposed, the reply box makes way for the decision —
    // no competing "type something" affordance while a Finalize/Request
    // changes/Dismiss choice is on the table.
    expect(await screen.findByText('Needs changes')).toBeInTheDocument()
    expect(screen.getByText('fix the thing')).toBeInTheDocument()
    expect(screen.queryByPlaceholderText('Reply...')).not.toBeInTheDocument()
    // Nothing editable — no select or textarea for the human to hand-tweak
    // the decision/notes into something the model never actually said.
    const draftReview = document.querySelector<HTMLElement>('.draft-review')!
    expect(within(draftReview).queryByRole('combobox')).not.toBeInTheDocument()
    expect(within(draftReview).queryByRole('textbox')).not.toBeInTheDocument()
    // No separate "what should change" comment field either.
    expect(screen.queryByPlaceholderText(/What should change/)).not.toBeInTheDocument()

    await user.click(screen.getByRole('button', { name: 'Request changes' }))

    // Request changes on a non-editable draft has nothing to forward — it
    // just closes the draft and hands back to the ordinary conversation.
    expect(api.postStageMessage).not.toHaveBeenCalled()
    expect(screen.queryByText('Proposed draft')).not.toBeInTheDocument()
    expect(screen.getByPlaceholderText('Reply...')).toBeInTheDocument()

    // Dismiss behaves identically for a second proposal — still no API call.
    act(() => deliver({ tool_call: { name: 'propose_review', arguments: JSON.stringify({ decision: 'approved', notes: 'lgtm' }) } }))
    await user.click(await screen.findByRole('button', { name: 'Dismiss draft' }))

    expect(api.finalizeReview).not.toHaveBeenCalled()
    expect(screen.queryByText('Proposed draft')).not.toBeInTheDocument()
    expect(screen.getByPlaceholderText('Reply...')).toBeInTheDocument()
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

  it('does not rehydrate a stale propose_review draft left over from an earlier review round, and explains that this round has not started instead of resubmitting it', async () => {
    const user = userEvent.setup()
    // Simulates a task on its second (or later) trip through Review: the
    // stage's Conversation already has a propose_review tool call from the
    // round that just got sent back as needs_changes, dated before the
    // transition that re-entered review for this new round.
    vi.mocked(api.getStageConversation).mockResolvedValue({
      stage: 'review',
      messages: [
        {
          role: 'assistant',
          content: '',
          tool_call: { id: 'call-1', name: 'propose_review', arguments: JSON.stringify({ decision: 'needs_changes', notes: 'stale notes from a prior round' }) },
          created_at: '2026-01-01T00:00:00Z',
        },
      ],
    })
    vi.mocked(api.listModels).mockResolvedValue({ models: ['model-a'] })
    vi.mocked(api.listAgentExecutors).mockResolvedValue({ executors: [] })
    vi.mocked(api.listStageTransitions).mockResolvedValue({
      stage_transitions: [
        { task_id: taskId, from_stage: 'implementation', to_stage: 'review', trigger: 'execution_success', created_at: '2026-01-02T00:00:00Z' },
      ],
    })
    vi.mocked(api.listExecutions).mockResolvedValue({ executions: [makeExecution()] })
    vi.mocked(api.getReviewDiff).mockResolvedValue({ patch: '' })
    vi.mocked(api.postStageMessage).mockResolvedValue()

    render(<ReviewPanel projectId={projectId} taskId={taskId} onFinalized={vi.fn()} />)

    // The stale draft must not be offered as a live pending verdict...
    expect(await screen.findByText(/Nothing's been said about this attempt yet/)).toBeInTheDocument()
    expect(screen.queryByRole('button', { name: 'Finalize' })).not.toBeInTheDocument()
    // ...and there's no separate "start"-style button — the ordinary reply
    // box (already rendered, since the conversation isn't empty) is the only
    // way to continue, since postMessage re-resolves the current execution's
    // diff fresh on any turn, not just a dedicated kickoff call.
    expect(screen.queryByRole('button', { name: 'Start Review' })).not.toBeInTheDocument()

    const replyBox = screen.getByPlaceholderText('Reply...')
    await user.type(replyBox, 'please take a look')
    await user.click(screen.getByRole('button', { name: 'Send' }))
    expect(api.postStageMessage).toHaveBeenCalled()
    expect(api.startStageConversation).not.toHaveBeenCalled()
  })
})

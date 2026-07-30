import { describe, expect, it, vi } from 'vitest'
import { render, screen } from '@testing-library/react'
import userEvent from '@testing-library/user-event'
import { TimelinePanel } from './TimelinePanel'
import * as api from './api'
import type { Conversation, Review, StageTransition } from './types'

vi.mock('./api')

const projectId = 'demo'
const taskId = 'task-a'

describe('TimelinePanel', () => {
  it('renders nothing when there are no transitions or reviews', async () => {
    vi.mocked(api.listStageTransitions).mockResolvedValue({ stage_transitions: [] })
    vi.mocked(api.listReviews).mockResolvedValue({ reviews: [] })

    const { container } = render(<TimelinePanel projectId={projectId} taskId={taskId} />)
    await vi.waitFor(() => expect(api.listStageTransitions).toHaveBeenCalled())
    expect(container.textContent).toBe('')
  })

  it('merges transitions and reviews into one chronologically sorted list', async () => {
    const transitions: StageTransition[] = [
      { task_id: taskId, from_stage: 'requirements', to_stage: 'planning', trigger: 'finalize_requirements', created_at: '2026-01-01T00:00:00Z' },
      {
        task_id: taskId,
        from_stage: 'review',
        to_stage: 'planning',
        trigger: 'revise_planning',
        reason: 'I wanted icons, not words',
        created_at: '2026-01-03T00:00:00Z',
      },
    ]
    const reviews: Review[] = [
      { review_id: 'review-001', task_id: taskId, execution_id: 'exec-001', decision: 'needs_changes', notes: 'border contrast gap', created_at: '2026-01-02T00:00:00Z' },
    ]
    vi.mocked(api.listStageTransitions).mockResolvedValue({ stage_transitions: transitions })
    vi.mocked(api.listReviews).mockResolvedValue({ reviews })

    render(<TimelinePanel projectId={projectId} taskId={taskId} />)

    await screen.findByText(/I wanted icons, not words/)
    const items = document.querySelectorAll('.execution-history > li')
    expect(items).toHaveLength(3)
    expect(items[0].textContent).toContain('finalized requirements')
    expect(items[1].textContent).toContain('border contrast gap')
    expect(items[2].textContent).toContain('I wanted icons, not words')
  })

  it('expanding a transition loads its destination stage conversation, filtered to messages at or after the transition', async () => {
    const user = userEvent.setup()
    const transitions: StageTransition[] = [
      {
        task_id: taskId,
        from_stage: 'review',
        to_stage: 'planning',
        trigger: 'revise_planning',
        reason: 'I wanted icons, not words',
        created_at: '2026-01-03T00:00:00Z',
      },
    ]
    vi.mocked(api.listStageTransitions).mockResolvedValue({ stage_transitions: transitions })
    vi.mocked(api.listReviews).mockResolvedValue({ reviews: [] })
    const conv: Conversation = {
      stage: 'planning',
      messages: [
        { role: 'user', content: 'first planning pass, before the revision', created_at: '2026-01-01T12:00:00Z' },
        { role: 'user', content: 'second planning pass, after the revision', created_at: '2026-01-03T12:00:00Z' },
      ],
    }
    vi.mocked(api.getStageConversation).mockResolvedValue(conv)

    render(<TimelinePanel projectId={projectId} taskId={taskId} />)
    await user.click((await screen.findByText(/revised back to planning/)).closest('summary')!)

    await screen.findByText('second planning pass, after the revision')
    expect(screen.queryByText('first planning pass, before the revision')).not.toBeInTheDocument()
    expect(api.getStageConversation).toHaveBeenCalledWith(projectId, taskId, 'planning')
  })

  it('does not offer a conversation view for a transition whose destination stage has no Conversation', async () => {
    const transitions: StageTransition[] = [
      { task_id: taskId, from_stage: 'implementation', to_stage: 'review', trigger: 'execution_success', created_at: '2026-01-01T00:00:00Z' },
      { task_id: taskId, from_stage: 'pr_review', to_stage: 'merged', trigger: 'mark_pr_merged', created_at: '2026-01-02T00:00:00Z' },
    ]
    vi.mocked(api.listStageTransitions).mockResolvedValue({ stage_transitions: transitions })
    vi.mocked(api.listReviews).mockResolvedValue({ reviews: [] })

    render(<TimelinePanel projectId={projectId} taskId={taskId} />)
    await screen.findByText(/marked PR merged/)

    const items = document.querySelectorAll('.execution-history > li')
    expect(items).toHaveLength(2)
    // "review" (execution_success's to_stage) has a Conversation, so its
    // <details> body renders an (empty, unexpanded) log container...
    expect(items[0].querySelector('.execution-log')).not.toBeNull()
    // ...but "merged" (mark_pr_merged's to_stage) does not.
    expect(items[1].querySelector('.execution-log')).toBeNull()
  })
})

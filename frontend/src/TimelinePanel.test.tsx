import { describe, expect, it, vi } from 'vitest'
import { render, screen } from '@testing-library/react'
import userEvent from '@testing-library/user-event'
import { TimelinePanel } from './TimelinePanel'
import * as api from './api'
import type { Conversation, KnowledgeActivityEntry, Review, StageTransition } from './types'

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

  it('labels a finalize_review transition by its linked review decision, not the generic trigger name', async () => {
    const transitions: StageTransition[] = [
      { task_id: taskId, from_stage: 'review', to_stage: 'implementation', trigger: 'finalize_review', review_id: 'review-needs', created_at: '2026-01-01T00:00:00Z' },
      { task_id: taskId, from_stage: 'review', to_stage: 'pr_review', trigger: 'finalize_review', review_id: 'review-approved', created_at: '2026-01-02T00:00:00Z' },
      { task_id: taskId, from_stage: 'review', to_stage: 'requirements', trigger: 'finalize_review', review_id: 'review-rejected', created_at: '2026-01-03T00:00:00Z' },
    ]
    const reviews: Review[] = [
      { review_id: 'review-needs', task_id: taskId, execution_id: 'exec-001', decision: 'needs_changes', notes: '', created_at: '2026-01-01T00:00:00Z' },
      { review_id: 'review-approved', task_id: taskId, execution_id: 'exec-002', decision: 'approved', notes: '', created_at: '2026-01-02T00:00:00Z' },
      { review_id: 'review-rejected', task_id: taskId, execution_id: 'exec-003', decision: 'rejected', notes: '', created_at: '2026-01-03T00:00:00Z' },
    ]
    vi.mocked(api.listStageTransitions).mockResolvedValue({ stage_transitions: transitions })
    vi.mocked(api.listReviews).mockResolvedValue({ reviews })

    render(<TimelinePanel projectId={projectId} taskId={taskId} />)
    await screen.findByText(/review rejected/)

    const items = document.querySelectorAll('.timeline-transition')
    expect(items).toHaveLength(3)
    expect(items[0].textContent).toContain('changes requested')
    expect(items[1].textContent).toContain('review approved')
    expect(items[2].textContent).toContain('review rejected')
  })

  it('falls back to the generic label when a finalize_review transition has no linked review', async () => {
    const transitions: StageTransition[] = [
      { task_id: taskId, from_stage: 'review', to_stage: 'implementation', trigger: 'finalize_review', created_at: '2026-01-01T00:00:00Z' },
    ]
    vi.mocked(api.listStageTransitions).mockResolvedValue({ stage_transitions: transitions })
    vi.mocked(api.listReviews).mockResolvedValue({ reviews: [] })

    render(<TimelinePanel projectId={projectId} taskId={taskId} />)

    expect(await screen.findByText(/finalized review/)).toBeInTheDocument()
  })

  it('renders nothing when there are no transitions, reviews, or knowledge activity', async () => {
    vi.mocked(api.listStageTransitions).mockResolvedValue({ stage_transitions: [] })
    vi.mocked(api.listReviews).mockResolvedValue({ reviews: [] })

    const { container } = render(<TimelinePanel projectId={projectId} taskId={taskId} knowledgeActivity={[]} />)
    await vi.waitFor(() => expect(api.listStageTransitions).toHaveBeenCalled())
    expect(container.textContent).toBe('')
  })

  it('merges knowledge_activity entries into the chronological list, labeled by action', async () => {
    vi.mocked(api.listStageTransitions).mockResolvedValue({
      stage_transitions: [
        { task_id: taskId, from_stage: 'review', to_stage: 'implementation', trigger: 'finalize_review', created_at: '2026-01-01T00:00:00Z' },
      ],
    })
    vi.mocked(api.listReviews).mockResolvedValue({ reviews: [] })
    const knowledgeActivity: KnowledgeActivityEntry[] = [
      { concept_id: 'process/replan-when-requirements-invalidate-spec', type: 'Operational Practice', action: 'created', created_at: '2026-01-02T00:00:00Z' },
      { concept_id: 'coding-standards/logging', type: 'Coding Standard', action: 'updated', created_at: '2026-01-03T00:00:00Z' },
      { concept_id: 'x', action: 'rejected', created_at: '2026-01-04T00:00:00Z' },
    ]

    render(<TimelinePanel projectId={projectId} taskId={taskId} knowledgeActivity={knowledgeActivity} />)
    await screen.findByText(/finalized review/)

    const items = document.querySelectorAll('.execution-history > li')
    expect(items).toHaveLength(4)
    expect(items[1].textContent).toContain('knowledge concept created: process/replan-when-requirements-invalidate-spec')
    expect(items[2].textContent).toContain('knowledge concept updated: coding-standards/logging')
    expect(items[3].textContent).toContain('knowledge concept proposal rejected: x')
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

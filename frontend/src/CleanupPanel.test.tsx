import { describe, expect, it, vi, beforeEach } from 'vitest'
import { render, screen } from '@testing-library/react'
import userEvent from '@testing-library/user-event'
import { CleanupPanel } from './CleanupPanel'
import * as api from './api'
import type { Task } from './types'

vi.mock('./api')

const projectId = 'demo'
const taskId = 'task-a'

function makeTask(overrides: Partial<Task> = {}): Task {
  return {
    id: taskId,
    title: 'A task',
    project: projectId,
    stage: 'cleanup',
    created_at: '2026-01-01T00:00:00Z',
    updated_at: '2026-01-01T00:00:00Z',
    objective: '',
    constraints: [],
    assumptions: [],
    success_criteria: [],
    references: { knowledge: [], repo: [] },
    pull_request: { url: 'https://github.com/org/repo/pull/7', number: 7, branch: 'task-exec/task-a/exec-002' },
    ...overrides,
  }
}

beforeEach(() => {
  vi.mocked(api.runTaskCleanup).mockReset()
})

describe('CleanupPanel', () => {
  it('renders the persisted per-worktree report', () => {
    const task = makeTask({
      cleanup_status: [
        { execution_id: 'exec-001', outcome: 'skipped', reason: 'worktree has uncommitted changes' },
        { execution_id: 'exec-002', outcome: 'removed' },
      ],
    })
    render(<CleanupPanel projectId={projectId} taskId={taskId} task={task} onUpdated={vi.fn()} />)

    expect(screen.getByText('exec-001')).toBeInTheDocument()
    expect(screen.getByText(/worktree has uncommitted changes/)).toBeInTheDocument()
    expect(screen.getByText('exec-002')).toBeInTheDocument()
  })

  it('shows a placeholder when there is no report yet', () => {
    render(<CleanupPanel projectId={projectId} taskId={taskId} task={makeTask()} onUpdated={vi.fn()} />)
    expect(screen.getByText('No cleanup report yet.')).toBeInTheDocument()
  })

  it('disables Force-remove until there is a skipped/failed worktree', () => {
    const cleanTask = makeTask({ cleanup_status: [{ execution_id: 'exec-001', outcome: 'removed' }] })
    render(<CleanupPanel projectId={projectId} taskId={taskId} task={cleanTask} onUpdated={vi.fn()} />)
    expect(screen.getByRole('button', { name: 'Force-remove' })).toBeDisabled()
  })

  it('Retry calls runTaskCleanup without force and reports the updated task', async () => {
    const user = userEvent.setup()
    const task = makeTask({ cleanup_status: [{ execution_id: 'exec-001', outcome: 'skipped', reason: 'dirty' }] })
    const merged = makeTask({ stage: 'merged' })
    vi.mocked(api.runTaskCleanup).mockResolvedValue(merged)
    const onUpdated = vi.fn()

    render(<CleanupPanel projectId={projectId} taskId={taskId} task={task} onUpdated={onUpdated} />)
    await user.click(screen.getByRole('button', { name: 'Retry' }))

    expect(api.runTaskCleanup).toHaveBeenCalledWith(projectId, taskId, false)
    expect(onUpdated).toHaveBeenCalledWith(merged)
  })

  it('Force-remove calls runTaskCleanup with force: true', async () => {
    const user = userEvent.setup()
    const task = makeTask({ cleanup_status: [{ execution_id: 'exec-001', outcome: 'failed', reason: 'boom' }] })
    vi.mocked(api.runTaskCleanup).mockResolvedValue(makeTask({ stage: 'merged' }))

    render(<CleanupPanel projectId={projectId} taskId={taskId} task={task} onUpdated={vi.fn()} />)
    await user.click(screen.getByRole('button', { name: 'Force-remove' }))

    expect(api.runTaskCleanup).toHaveBeenCalledWith(projectId, taskId, true)
  })

  it('shows an error message when the cleanup request fails', async () => {
    const user = userEvent.setup()
    const task = makeTask({ cleanup_status: [{ execution_id: 'exec-001', outcome: 'skipped', reason: 'dirty' }] })
    vi.mocked(api.runTaskCleanup).mockRejectedValue(new Error('boom'))

    render(<CleanupPanel projectId={projectId} taskId={taskId} task={task} onUpdated={vi.fn()} />)
    await user.click(screen.getByRole('button', { name: 'Retry' }))

    expect(await screen.findByText('boom')).toBeInTheDocument()
  })
})

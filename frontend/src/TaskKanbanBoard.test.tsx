import { describe, expect, it, vi } from 'vitest'
import { render, screen } from '@testing-library/react'
import userEvent from '@testing-library/user-event'
import { TaskKanbanBoard } from './TaskKanbanBoard'
import type { Task } from './types'

function makeTask(overrides: Partial<Task>): Task {
  return {
    id: 'task-a',
    title: 'Task A',
    project: 'demo',
    status: 'draft',
    stage: 'requirements',
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

describe('TaskKanbanBoard', () => {
  it('renders each task under its own stage column', () => {
    const tasks = [
      makeTask({ id: 'req-task', title: 'Requirements task', stage: 'requirements' }),
      makeTask({ id: 'plan-task', title: 'Planning task', stage: 'planning' }),
    ]
    render(<TaskKanbanBoard tasks={tasks} onSelect={vi.fn()} />)

    const requirementsColumn = screen.getByText('Requirements').closest('.kanban-column')
    const planningColumn = screen.getByText('Planning').closest('.kanban-column')
    expect(requirementsColumn).toContainElement(screen.getByText('Requirements task'))
    expect(planningColumn).toContainElement(screen.getByText('Planning task'))
  })

  it('calls onSelect with the clicked task', async () => {
    const user = userEvent.setup()
    const onSelect = vi.fn()
    const task = makeTask({ id: 'clicked-task', title: 'Clicked task' })
    render(<TaskKanbanBoard tasks={[task]} onSelect={onSelect} />)

    await user.click(screen.getByText('Clicked task'))
    expect(onSelect).toHaveBeenCalledWith(task)
  })
})

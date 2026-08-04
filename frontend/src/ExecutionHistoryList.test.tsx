import { describe, expect, it, vi } from 'vitest'
import { render, screen } from '@testing-library/react'
import userEvent from '@testing-library/user-event'
import { ExecutionHistoryList } from './ExecutionHistoryList'
import * as api from './api'
import type { Execution, ExecutionLog } from './types'

vi.mock('./api')

const projectId = 'demo'
const taskId = 'task-a'

function makeExecution(overrides: Partial<Execution> = {}): Execution {
  return {
    execution_id: 'exec-001',
    task_id: taskId,
    executor: { type: 'claude-code', version: '' },
    input: { plan_ref: 'plan.yaml', context_refs: [], review_feedback: '', continued_from_execution_id: '' },
    output: {
      artifacts: [],
      git_branch: 'task-exec/task-a/exec-001',
      commits: ['abc123'],
      forked_from_branch: '',
      workspace_dirty: false,
    },
    metrics: { duration_seconds: 1.5, tokens_used: 0, cost_estimate: 0 },
    status: 'success',
    created_at: '2026-01-01T00:00:00Z',
    ...overrides,
  }
}

describe('ExecutionHistoryList — replaying a persisted log', () => {
  it('attaches each result to the call with its own id, not to whichever call is currently last, when the log has results out of order', async () => {
    const user = userEvent.setup()
    const log: ExecutionLog = {
      execution_id: 'exec-001',
      events: [
        // Both calls recorded (same tool name, so a name-based guess
        // couldn't tell them apart either) before either result — the
        // exact shape a batching provider produces — with results
        // recorded in REVERSE declaration order.
        { kind: 'tool_call', id: 'call-A', tool_name: 'Bash', tool_input: '{"command":"cat unique-path-A"}', created_at: '2026-01-01T00:00:00Z' },
        { kind: 'tool_call', id: 'call-B', tool_name: 'Bash', tool_input: '{"command":"cat unique-path-B"}', created_at: '2026-01-01T00:00:01Z' },
        { kind: 'tool_result', id: 'call-B', tool_result: 'RESULT-FOR-B', created_at: '2026-01-01T00:00:02Z' },
        { kind: 'tool_result', id: 'call-A', tool_result: 'RESULT-FOR-A', created_at: '2026-01-01T00:00:03Z' },
      ],
    }
    vi.mocked(api.getExecutionLog).mockResolvedValue(log)

    render(<ExecutionHistoryList projectId={projectId} taskId={taskId} executions={[makeExecution()]} />)
    await user.click(screen.getByRole('listitem').querySelector('summary')!)

    await screen.findByText('RESULT-FOR-A')
    const rows = document.querySelectorAll('.tool-activity-row')
    expect(rows).toHaveLength(2)
    expect(rows[0].textContent).toContain('unique-path-A')
    expect(rows[0].textContent).toContain('RESULT-FOR-A')
    expect(rows[1].textContent).toContain('unique-path-B')
    expect(rows[1].textContent).toContain('RESULT-FOR-B')
  })

  it('falls back to trailing-pending pairing for a legacy log with no ids, reproducing the pre-fix rendering', async () => {
    const user = userEvent.setup()
    const log: ExecutionLog = {
      execution_id: 'exec-001',
      events: [
        { kind: 'tool_call', tool_name: 'Read', tool_input: '{"path":"a.go"}', created_at: '2026-01-01T00:00:00Z' },
        { kind: 'tool_result', tool_result: 'package main', created_at: '2026-01-01T00:00:01Z' },
      ],
    }
    vi.mocked(api.getExecutionLog).mockResolvedValue(log)

    render(<ExecutionHistoryList projectId={projectId} taskId={taskId} executions={[makeExecution()]} />)
    await user.click(screen.getByRole('listitem').querySelector('summary')!)

    await screen.findByText('package main')
    const rows = document.querySelectorAll('.tool-activity-row')
    expect(rows).toHaveLength(1)
    expect(rows[0].textContent).toContain('package main')
  })

  it('renders narration and tool blocks in real recorded order', async () => {
    const user = userEvent.setup()
    const log: ExecutionLog = {
      execution_id: 'exec-001',
      events: [
        { kind: 'text', text: 'build passes, now testing', created_at: '2026-01-01T00:00:00Z' },
        { kind: 'tool_call', id: 'call-1', tool_name: 'Bash', tool_input: '{"command":"go test"}', created_at: '2026-01-01T00:00:01Z' },
        { kind: 'tool_result', id: 'call-1', tool_result: 'ok', created_at: '2026-01-01T00:00:02Z' },
        { kind: 'text', text: 'all green', created_at: '2026-01-01T00:00:03Z' },
      ],
    }
    vi.mocked(api.getExecutionLog).mockResolvedValue(log)

    render(<ExecutionHistoryList projectId={projectId} taskId={taskId} executions={[makeExecution()]} />)
    await user.click(screen.getByRole('listitem').querySelector('summary')!)

    await screen.findByText('all green')
    const text = document.body.textContent ?? ''
    expect(text.indexOf('build passes, now testing')).toBeLessThan(text.indexOf('Bash'))
    expect(text.indexOf('Bash')).toBeLessThan(text.indexOf('all green'))
  })
})

describe('ExecutionHistoryList — usage summary', () => {
  it('shows tokens and cost in the summary line when the executor reported them', () => {
    const execution = makeExecution({ metrics: { duration_seconds: 12, tokens_used: 4321, cost_estimate: 0.15 } })
    render(<ExecutionHistoryList projectId={projectId} taskId={taskId} executions={[execution]} />)

    expect(screen.getByText(/4321 tokens/)).toBeInTheDocument()
    expect(screen.getByText(/\$0\.15/)).toBeInTheDocument()
  })

  it('omits tokens and cost when the executor did not report them', () => {
    render(<ExecutionHistoryList projectId={projectId} taskId={taskId} executions={[makeExecution()]} />)

    expect(screen.queryByText(/tokens/)).not.toBeInTheDocument()
  })
})

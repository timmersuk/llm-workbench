import { describe, expect, it, vi, beforeEach } from 'vitest'
import { render, screen, waitFor, within } from '@testing-library/react'
import userEvent from '@testing-library/user-event'
import { ExecutePanel } from './ExecutePanel'
import * as api from './api'
import type { Execution, ExecuteStreamEvent } from './types'

vi.mock('./api')

const projectId = 'demo'
const taskId = 'task-a'

function makeExecution(overrides: Partial<Execution> = {}): Execution {
  return {
    execution_id: 'exec-001',
    task_id: taskId,
    executor: { type: 'claude-code', version: '' },
    input: { plan_ref: 'plan.yaml', context_refs: [] },
    output: { artifacts: [], git_branch: 'task-exec/task-a/exec-001', commits: ['abc123'], forked_from_branch: '' },
    metrics: { duration_seconds: 1.5, tokens_used: 0, cost_estimate: 0 },
    status: 'success',
    created_at: '2026-01-01T00:00:00Z',
    ...overrides,
  }
}

beforeEach(() => {
  vi.mocked(api.listExecutions).mockResolvedValue({ executions: [] })
  vi.mocked(api.listAgentExecutors).mockResolvedValue({ executors: ['claude-code'] })
})

describe('ExecutePanel — past executions', () => {
  it('shows prior attempts on mount', async () => {
    vi.mocked(api.listExecutions).mockResolvedValue({ executions: [makeExecution()] })
    render(<ExecutePanel projectId={projectId} taskId={taskId} onExecuted={vi.fn()} />)

    expect(await screen.findByText(/exec-001: success/)).toBeInTheDocument()
    expect(screen.getByText(/task-exec\/task-a\/exec-001/)).toBeInTheDocument()
    expect(screen.getByText(/1 commit\(s\)/)).toBeInTheDocument()
  })

  it('renders nothing extra when there are no prior attempts', async () => {
    render(<ExecutePanel projectId={projectId} taskId={taskId} onExecuted={vi.fn()} />)
    await waitFor(() => expect(api.listExecutions).toHaveBeenCalledWith(projectId, taskId))
    expect(screen.queryByRole('listitem')).not.toBeInTheDocument()
  })
})

describe('ExecutePanel — running an execution', () => {
  it('streams text, tool_call, and tool_result events into the trace', async () => {
    const user = userEvent.setup()
    vi.mocked(api.startExecution).mockImplementation(async (_projectId, _taskId, _executor, onEvent) => {
      onEvent({ type: 'text', content: 'starting up' })
      onEvent({ type: 'tool_call', tool_name: 'Write', tool_input: '{"path":"a.go"}' })
      onEvent({ type: 'tool_result', tool_result: 'wrote file' })
      onEvent({ type: 'done', execution: makeExecution() })
    })

    render(<ExecutePanel projectId={projectId} taskId={taskId} onExecuted={vi.fn()} />)
    await user.click(await screen.findByRole('button', { name: 'Run Execution' }))

    expect(await screen.findByText('starting up')).toBeInTheDocument()
    expect(screen.getByText('Write')).toBeInTheDocument()
    expect(screen.getByText('Tool result')).toBeInTheDocument()
  })

  it('calls onExecuted with the recorded Execution once the run completes', async () => {
    const user = userEvent.setup()
    const onExecuted = vi.fn()
    const execution = makeExecution({ status: 'failure', failure: { type: 'execution', message: 'boom' } })
    vi.mocked(api.startExecution).mockImplementation(async (_projectId, _taskId, _executor, onEvent) => {
      onEvent({ type: 'done', execution })
    })

    render(<ExecutePanel projectId={projectId} taskId={taskId} onExecuted={onExecuted} />)
    await user.click(await screen.findByRole('button', { name: 'Run Execution' }))

    await waitFor(() => expect(onExecuted).toHaveBeenCalledWith(execution))
    expect(await screen.findByText(/exec-001: failure/)).toBeInTheDocument()
    expect(screen.getByText(/boom/)).toBeInTheDocument()
  })

  it('shows an inline error for a real failure', async () => {
    const user = userEvent.setup()
    vi.mocked(api.startExecution).mockImplementation(async (_projectId, _taskId, _executor, onEvent) => {
      onEvent({ type: 'error', error: 'workspace resolution failed' })
    })

    render(<ExecutePanel projectId={projectId} taskId={taskId} onExecuted={vi.fn()} />)
    await user.click(await screen.findByRole('button', { name: 'Run Execution' }))

    expect(await screen.findByText('workspace resolution failed')).toBeInTheDocument()
  })

  it('shows Stop while running and calls abort on the in-flight controller', async () => {
    const user = userEvent.setup()
    let capturedSignal: AbortSignal | undefined
    vi.mocked(api.startExecution).mockImplementation(
      (_projectId, _taskId, _executor, _onEvent, signal) =>
        new Promise((resolve) => {
          capturedSignal = signal
          signal?.addEventListener('abort', () => resolve())
        }),
    )

    render(<ExecutePanel projectId={projectId} taskId={taskId} onExecuted={vi.fn()} />)
    await user.click(await screen.findByRole('button', { name: 'Run Execution' }))

    const stopButton = await screen.findByRole('button', { name: 'Stop' })
    await user.click(stopButton)

    await waitFor(() => expect(capturedSignal?.aborted).toBe(true))
    await waitFor(() => expect(screen.getByRole('button', { name: 'Run Execution' })).toBeInTheDocument())
  })

  it('swallows an error event that arrives after a deliberate Stop', async () => {
    const user = userEvent.setup()
    vi.mocked(api.startExecution).mockImplementation((_projectId, _taskId, _executor, onEvent, signal) => {
      return new Promise((resolve) => {
        signal?.addEventListener('abort', () => {
          onEvent({ type: 'error', error: 'context canceled' } as ExecuteStreamEvent)
          resolve()
        })
      })
    })

    render(<ExecutePanel projectId={projectId} taskId={taskId} onExecuted={vi.fn()} />)
    await user.click(await screen.findByRole('button', { name: 'Run Execution' }))
    await user.click(await screen.findByRole('button', { name: 'Stop' }))

    await waitFor(() => expect(screen.getByRole('button', { name: 'Run Execution' })).toBeInTheDocument())
    expect(screen.queryByText('context canceled')).not.toBeInTheDocument()
  })
})

describe('ExecutePanel — tool_result error styling', () => {
  it('labels a failing tool result distinctly from a successful one', async () => {
    const user = userEvent.setup()
    vi.mocked(api.startExecution).mockImplementation(async (_projectId, _taskId, _executor, onEvent) => {
      onEvent({ type: 'tool_result', tool_result: 'exit 1', is_error: true })
    })

    const { container } = render(<ExecutePanel projectId={projectId} taskId={taskId} onExecuted={vi.fn()} />)
    await user.click(await screen.findByRole('button', { name: 'Run Execution' }))

    const summary = await screen.findByText('Tool error')
    expect(within(container).getByText('exit 1')).toBeInTheDocument()
    expect(summary).toBeInTheDocument()
  })
})

import { describe, expect, it, vi, beforeEach } from 'vitest'
import { act, render, screen, waitFor, within } from '@testing-library/react'
import userEvent from '@testing-library/user-event'
import { ExecutePanel } from './ExecutePanel'
import * as api from './api'
import type { Execution, ExecuteStreamEvent, Review } from './types'

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

function makeReview(overrides: Partial<Review> = {}): Review {
  return {
    review_id: 'review-001',
    task_id: taskId,
    execution_id: 'exec-001',
    decision: 'needs_changes',
    notes: 'address the border contrast gap',
    created_at: '2026-01-01T00:00:00Z',
    ...overrides,
  }
}

beforeEach(() => {
  vi.mocked(api.listExecutions).mockResolvedValue({ executions: [] })
  vi.mocked(api.listAgentExecutors).mockResolvedValue({ executors: ['claude-code'] })
  vi.mocked(api.listModels).mockResolvedValue({ models: [] })
  vi.mocked(api.getContinuableExecution).mockResolvedValue({ execution_id: '' })
  vi.mocked(api.listReviews).mockResolvedValue({ reviews: [] })
})

describe('ExecutePanel — past executions', () => {
  it('shows prior attempts on mount', async () => {
    vi.mocked(api.listExecutions).mockResolvedValue({ executions: [makeExecution()] })
    render(<ExecutePanel projectId={projectId} taskId={taskId} onExecuted={vi.fn()} />)

    const item = await screen.findByRole('listitem')
    expect(within(item).getByText(/exec-001: success/)).toBeInTheDocument()
    expect(within(item).getByText(/task-exec\/task-a\/exec-001/)).toBeInTheDocument()
    expect(within(item).getByText(/1 commit\(s\)/)).toBeInTheDocument()
  })

  it('shows a status line next to the run button reflecting the most recent attempt', async () => {
    vi.mocked(api.listExecutions).mockResolvedValue({
      executions: [makeExecution(), makeExecution({ execution_id: 'exec-002', status: 'failure', failure: { type: 'execution', message: 'boom' } })],
    })
    render(<ExecutePanel projectId={projectId} taskId={taskId} onExecuted={vi.fn()} />)

    expect(await screen.findByText('Last run exec-002: failure — boom')).toBeInTheDocument()
  })

  it('renders nothing extra when there are no prior attempts', async () => {
    render(<ExecutePanel projectId={projectId} taskId={taskId} onExecuted={vi.fn()} />)
    await waitFor(() => expect(api.listExecutions).toHaveBeenCalledWith(projectId, taskId))
    expect(screen.queryByRole('listitem')).not.toBeInTheDocument()
  })
})

describe('ExecutePanel — running an execution', () => {
  it('does not allow Local execution until model discovery supplies a selection', async () => {
    vi.mocked(api.listAgentExecutors).mockResolvedValue({ executors: ['local'] })
    vi.mocked(api.listModels).mockImplementation(() => new Promise(() => undefined))

    render(<ExecutePanel projectId={projectId} taskId={taskId} onExecuted={vi.fn()} />)

    expect(await screen.findByLabelText('Executor')).toHaveValue('local')
    expect(screen.getByRole('button', { name: 'Run Execution' })).toBeDisabled()
  })

  it('offers Local LLM execution with its selected model', async () => {
    const user = userEvent.setup()
    vi.mocked(api.listAgentExecutors).mockResolvedValue({ executors: ['local'] })
    vi.mocked(api.listModels).mockResolvedValue({ models: ['gpt-oss-20b', 'qwen3-coder'] })
    vi.mocked(api.startExecution).mockResolvedValue()

    render(<ExecutePanel projectId={projectId} taskId={taskId} onExecuted={vi.fn()} />)

    const executorSelect = await screen.findByLabelText('Executor')
    expect(executorSelect).toHaveValue('local')
    expect(screen.getByRole('option', { name: 'Local LLM chat' })).toBeInTheDocument()
    const modelSelect = await screen.findByLabelText('Model')
    await user.selectOptions(modelSelect, 'qwen3-coder')
    await user.click(screen.getByRole('button', { name: 'Run Execution' }))

    await waitFor(() =>
      expect(api.startExecution).toHaveBeenCalledWith(
        projectId,
        taskId,
        'local',
        expect.anything(),
        expect.anything(),
        undefined,
        'qwen3-coder',
        expect.anything(),
      ),
    )
  })

  it('handles a null model list without crashing the task page', async () => {
    const consoleError = vi.spyOn(console, 'error').mockImplementation(() => undefined)
    vi.mocked(api.listAgentExecutors).mockResolvedValue({ executors: ['codex'] })
    vi.mocked(api.listModels).mockResolvedValue({ models: null } as never)

    render(<ExecutePanel projectId={projectId} taskId={taskId} onExecuted={vi.fn()} />)

    expect(await screen.findByLabelText('Executor')).toHaveValue('codex')
    await act(async () => undefined)
    expect(screen.queryByLabelText('Model')).not.toBeInTheDocument()
    expect(screen.getByRole('button', { name: 'Run Execution' })).toBeDisabled()
    expect(consoleError).not.toHaveBeenCalled()
    consoleError.mockRestore()
  })

  it('does not allow Codex execution until model discovery supplies a selection', async () => {
    vi.mocked(api.listAgentExecutors).mockResolvedValue({ executors: ['codex'] })
    vi.mocked(api.listModels).mockImplementation(() => new Promise(() => undefined))

    render(<ExecutePanel projectId={projectId} taskId={taskId} onExecuted={vi.fn()} />)

    expect(await screen.findByLabelText('Executor')).toHaveValue('codex')
    expect(screen.getByRole('button', { name: 'Run Execution' })).toBeDisabled()
  })

  it('offers the models supported by the selected Codex executor', async () => {
    const user = userEvent.setup()
    vi.mocked(api.listAgentExecutors).mockResolvedValue({ executors: ['codex'] })
    vi.mocked(api.listModels).mockResolvedValue({ models: ['gpt-5.3-codex', 'gpt-5.4'] })
    vi.mocked(api.startExecution).mockResolvedValue()

    render(<ExecutePanel projectId={projectId} taskId={taskId} onExecuted={vi.fn()} />)

    expect(await screen.findByLabelText('Executor')).toHaveValue('codex')
    const modelSelect = await screen.findByLabelText('Model')
    expect(modelSelect).toHaveValue('gpt-5.3-codex')
    expect(screen.getByRole('option', { name: 'gpt-5.4' })).toBeInTheDocument()
    await user.selectOptions(modelSelect, 'gpt-5.4')
    await user.click(screen.getByRole('button', { name: 'Run Execution' }))

    await waitFor(() =>
      expect(api.startExecution).toHaveBeenCalledWith(
        projectId,
        taskId,
        'codex',
        expect.anything(),
        expect.anything(),
        undefined,
        'gpt-5.4',
        expect.anything(),
      ),
    )
  })

  it('filters the effort select to the selected executor\'s advertised efforts, and resets to its default when the previous choice is unsupported', async () => {
    const user = userEvent.setup()
    vi.mocked(api.listAgentExecutors).mockResolvedValue({ executors: ['claude-code', 'codex'] })
    vi.mocked(api.listModels).mockImplementation((executor) =>
      executor === 'claude-code'
        ? Promise.resolve({ models: ['sonnet'], efforts: ['high'], default_effort: 'high' })
        : Promise.resolve({ models: ['gpt-5.4'], efforts: ['low', 'medium'], default_effort: 'medium' }),
    )

    render(<ExecutePanel projectId={projectId} taskId={taskId} onExecuted={vi.fn()} />)

    // claude-code only advertises "high" — the effort select is narrowed to
    // that single option and the previous (unset) choice resolves to it.
    await waitFor(() => expect(screen.getByLabelText('Effort')).toHaveValue('high'))
    expect(screen.queryByRole('option', { name: 'low' })).not.toBeInTheDocument()
    expect(screen.queryByRole('option', { name: 'medium' })).not.toBeInTheDocument()

    // Switching to codex (low/medium only) drops the now-unsupported "high"
    // selection and falls back to its declared default, "medium" — not
    // silently left on a value the newly-selected executor never offered.
    await user.selectOptions(screen.getByLabelText('Executor'), 'codex')
    await waitFor(() => expect(screen.getByLabelText('Effort')).toHaveValue('medium'))
    expect(screen.getByRole('option', { name: 'low' })).toBeInTheDocument()
    expect(screen.getByRole('option', { name: 'medium' })).toBeInTheDocument()
    expect(screen.queryByRole('option', { name: 'high' })).not.toBeInTheDocument()
  })

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
    expect(screen.getByText('wrote file')).toBeInTheDocument()
  })

  it('attaches each result to the call with its own id, not to whichever call is currently last, when results arrive out of order', async () => {
    const user = userEvent.setup()
    vi.mocked(api.startExecution).mockImplementation(async (_projectId, _taskId, _executor, onEvent) => {
      // Both calls declared (same tool name, so a name-based guess couldn't
      // tell them apart either) before either result arrives — the exact
      // shape a batching provider produces. Results then delivered in
      // REVERSE declaration order.
      onEvent({ type: 'tool_call', id: 'call-A', tool_name: 'Bash', tool_input: '{"command":"cat unique-path-A"}' })
      onEvent({ type: 'tool_call', id: 'call-B', tool_name: 'Bash', tool_input: '{"command":"cat unique-path-B"}' })
      onEvent({ type: 'tool_result', id: 'call-B', tool_result: 'RESULT-FOR-B' })
      onEvent({ type: 'tool_result', id: 'call-A', tool_result: 'RESULT-FOR-A' })
      onEvent({ type: 'done', execution: makeExecution() })
    })

    render(<ExecutePanel projectId={projectId} taskId={taskId} onExecuted={vi.fn()} />)
    await user.click(await screen.findByRole('button', { name: 'Run Execution' }))

    await screen.findByText('RESULT-FOR-A')
    const rows = document.querySelectorAll('.tool-activity-row')
    expect(rows).toHaveLength(2)
    expect(rows[0].textContent).toContain('unique-path-A')
    expect(rows[0].textContent).toContain('RESULT-FOR-A')
    expect(rows[1].textContent).toContain('unique-path-B')
    expect(rows[1].textContent).toContain('RESULT-FOR-B')
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
    const item = await screen.findByRole('listitem')
    expect(within(item).getByText(/exec-001: failure/)).toBeInTheDocument()
    expect(within(item).getByText(/boom/)).toBeInTheDocument()
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

describe('ExecutePanel — continuing from a failed execution', () => {
  it('shows no continue/fresh choice when nothing is eligible', async () => {
    render(<ExecutePanel projectId={projectId} taskId={taskId} onExecuted={vi.fn()} />)
    await waitFor(() => expect(api.getContinuableExecution).toHaveBeenCalledWith(projectId, taskId))
    expect(screen.queryByText(/didn't finish/)).not.toBeInTheDocument()
  })

  it('shows the choice defaulting to Continue when an execution is eligible', async () => {
    vi.mocked(api.getContinuableExecution).mockResolvedValue({ execution_id: 'exec-002' })
    render(<ExecutePanel projectId={projectId} taskId={taskId} onExecuted={vi.fn()} />)

    const continueRadio = await screen.findByRole('radio', { name: /Continue from exec-002/ })
    const freshRadio = screen.getByRole('radio', { name: 'Start fresh' })
    expect(continueRadio).toBeChecked()
    expect(freshRadio).not.toBeChecked()
  })

  it('hides the choice once a run is in flight', async () => {
    const user = userEvent.setup()
    vi.mocked(api.getContinuableExecution).mockResolvedValue({ execution_id: 'exec-002' })
    vi.mocked(api.startExecution).mockImplementation(
      (_projectId, _taskId, _executor, _onEvent, signal) =>
        new Promise((resolve) => {
          signal?.addEventListener('abort', () => resolve())
        }),
    )
    render(<ExecutePanel projectId={projectId} taskId={taskId} onExecuted={vi.fn()} />)

    await screen.findByRole('radio', { name: /Continue from exec-002/ })
    await user.click(screen.getByRole('button', { name: 'Run Execution' }))

    expect(screen.queryByText(/didn't finish/)).not.toBeInTheDocument()
  })

  it('passes the eligible execution id through startExecution when Continue is selected', async () => {
    const user = userEvent.setup()
    vi.mocked(api.getContinuableExecution).mockResolvedValue({ execution_id: 'exec-002' })
    let gotContinueFrom: string | undefined
    vi.mocked(api.startExecution).mockImplementation(async (_projectId, _taskId, _executor, _onEvent, _signal, continueFrom) => {
      gotContinueFrom = continueFrom
    })

    render(<ExecutePanel projectId={projectId} taskId={taskId} onExecuted={vi.fn()} />)
    await screen.findByRole('radio', { name: /Continue from exec-002/ })
    await user.click(screen.getByRole('button', { name: 'Run Execution' }))

    await waitFor(() => expect(api.startExecution).toHaveBeenCalled())
    expect(gotContinueFrom).toBe('exec-002')
  })

  it('passes no continuation when Start fresh is selected instead', async () => {
    const user = userEvent.setup()
    vi.mocked(api.getContinuableExecution).mockResolvedValue({ execution_id: 'exec-002' })
    let gotContinueFrom: string | undefined
    vi.mocked(api.startExecution).mockImplementation(async (_projectId, _taskId, _executor, _onEvent, _signal, continueFrom) => {
      gotContinueFrom = continueFrom
    })

    render(<ExecutePanel projectId={projectId} taskId={taskId} onExecuted={vi.fn()} />)
    await user.click(await screen.findByRole('radio', { name: 'Start fresh' }))
    await user.click(screen.getByRole('button', { name: 'Run Execution' }))

    await waitFor(() => expect(api.startExecution).toHaveBeenCalled())
    expect(gotContinueFrom).toBeUndefined()
  })

  it('re-fetches the continuable hint once the run completes', async () => {
    const user = userEvent.setup()
    vi.mocked(api.getContinuableExecution).mockResolvedValueOnce({ execution_id: '' })
    vi.mocked(api.startExecution).mockImplementation(async (_projectId, _taskId, _executor, onEvent) => {
      onEvent({ type: 'done', execution: makeExecution({ execution_id: 'exec-003', status: 'failure' }) })
    })

    render(<ExecutePanel projectId={projectId} taskId={taskId} onExecuted={vi.fn()} />)
    expect(await screen.findByRole('button', { name: 'Run Execution' })).not.toBeDisabled()

    vi.mocked(api.getContinuableExecution).mockResolvedValue({ execution_id: 'exec-003' })
    await user.click(screen.getByRole('button', { name: 'Run Execution' }))

    expect(await screen.findByRole('radio', { name: /Continue from exec-003/ })).toBeInTheDocument()
  })
})

describe('ExecutePanel — continuing from review feedback', () => {
  it('shows no banner and the plain button label when the latest review is not needs_changes', async () => {
    vi.mocked(api.listReviews).mockResolvedValue({ reviews: [makeReview({ decision: 'approved' })] })
    render(<ExecutePanel projectId={projectId} taskId={taskId} onExecuted={vi.fn()} />)

    expect(await screen.findByRole('button', { name: 'Run Execution' })).toBeInTheDocument()
    expect(screen.queryByText(/sent back from review/)).not.toBeInTheDocument()
  })

  it('shows a banner quoting the reviewer notes and relabels the button when the latest review needs changes', async () => {
    vi.mocked(api.listReviews).mockResolvedValue({ reviews: [makeReview({ notes: 'address the border contrast gap' })] })
    render(<ExecutePanel projectId={projectId} taskId={taskId} onExecuted={vi.fn()} />)

    expect(await screen.findByText(/sent back from review with requested changes/)).toBeInTheDocument()
    expect(screen.getByText(/address the border contrast gap/)).toBeInTheDocument()
    expect(screen.getByRole('button', { name: 'Continue from Review Feedback' })).toBeInTheDocument()
  })

  it('only reacts to the latest review when multiple are recorded', async () => {
    vi.mocked(api.listReviews).mockResolvedValue({
      reviews: [makeReview({ review_id: 'review-001', decision: 'needs_changes' }), makeReview({ review_id: 'review-002', decision: 'approved' })],
    })
    render(<ExecutePanel projectId={projectId} taskId={taskId} onExecuted={vi.fn()} />)

    expect(await screen.findByRole('button', { name: 'Run Execution' })).toBeInTheDocument()
    expect(screen.queryByText(/sent back from review/)).not.toBeInTheDocument()
  })
})

describe('ExecutePanel — tool activity error styling', () => {
  it('shows a distinct status glyph for a failing tool call, paired with its call', async () => {
    const user = userEvent.setup()
    vi.mocked(api.startExecution).mockImplementation(async (_projectId, _taskId, _executor, onEvent) => {
      onEvent({ type: 'tool_call', tool_name: 'Bash', tool_input: '{"command":"false"}' })
      onEvent({ type: 'tool_result', tool_result: 'exit 1', is_error: true })
    })

    const { container } = render(<ExecutePanel projectId={projectId} taskId={taskId} onExecuted={vi.fn()} />)
    await user.click(await screen.findByRole('button', { name: 'Run Execution' }))

    expect(await screen.findByText('exit 1')).toBeInTheDocument()
    expect(within(container).getByText('Bash')).toBeInTheDocument()
    expect(container.querySelector('.tool-status-error')).toBeInTheDocument()
  })
})

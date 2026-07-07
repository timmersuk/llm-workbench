import { describe, expect, it, vi, beforeEach } from 'vitest'
import { act, render, screen, waitFor } from '@testing-library/react'
import userEvent from '@testing-library/user-event'
import { GrillMePanel } from './GrillMePanel'
import * as api from './api'
import type { ChatStreamEvent, Conversation, Task, TaskContext } from './types'

vi.mock('./api')

const projectId = 'demo'
const taskId = 'task-a'

const emptyContext: TaskContext = {
  summary: '',
  background: '',
  files: [],
  detail: '',
  verification: [],
  open_questions: [],
}

function makeTask(overrides: Partial<Task> = {}): Task {
  return {
    id: taskId,
    title: 'Task A',
    project: projectId,
    status: 'draft',
    stage: 'planning',
    created_at: '2026-01-01T00:00:00Z',
    updated_at: '2026-01-01T00:00:00Z',
    objective: 'ship it',
    constraints: [],
    assumptions: [],
    success_criteria: [],
    references: { knowledge: [], repo: [] },
    ...overrides,
  }
}

beforeEach(() => {
  vi.mocked(api.listModels).mockResolvedValue({ models: ['model-a', 'model-b'] })
  vi.mocked(api.listAgentExecutors).mockResolvedValue({ executors: [] })
  vi.mocked(api.getStageConversation).mockResolvedValue({ stage: 'requirements', messages: [] })
})

describe('GrillMePanel — initial load', () => {
  it('renders a populated transcript from getStageConversation', async () => {
    const conv: Conversation = {
      stage: 'requirements',
      messages: [
        { role: 'user', content: 'Add a login page', created_at: '2026-01-01T00:00:00Z' },
        { role: 'assistant', content: 'Sure, tell me more.', created_at: '2026-01-01T00:00:01Z' },
      ],
    }
    vi.mocked(api.getStageConversation).mockResolvedValue(conv)

    render(<GrillMePanel projectId={projectId} taskId={taskId} onFinalized={vi.fn()} />)

    expect(await screen.findByText('Add a login page')).toBeInTheDocument()
    expect(screen.getByText('Sure, tell me more.')).toBeInTheDocument()
  })

  it('renders no messages, without crashing, when messages is null', async () => {
    vi.mocked(api.getStageConversation).mockResolvedValue({ stage: 'requirements', messages: null })

    render(<GrillMePanel projectId={projectId} taskId={taskId} onFinalized={vi.fn()} />)

    await waitFor(() => expect(api.getStageConversation).toHaveBeenCalled())
    expect(screen.queryByText(/user:|assistant:/)).not.toBeInTheDocument()
  })

  it('shows an inline error when getStageConversation rejects', async () => {
    vi.mocked(api.getStageConversation).mockRejectedValue(new Error('network down'))

    render(<GrillMePanel projectId={projectId} taskId={taskId} onFinalized={vi.fn()} />)

    expect(await screen.findByText('Could not load conversation: network down')).toBeInTheDocument()
  })

  it('populates the model select and auto-selects the first model', async () => {
    render(<GrillMePanel projectId={projectId} taskId={taskId} onFinalized={vi.fn()} />)

    await waitFor(() => expect(screen.getByLabelText('Model')).toHaveValue('model-a'))
    expect(screen.getByRole('option', { name: 'model-b' })).toBeInTheDocument()
  })

  it('only offers Local LLM chat as the executor when no agent executors are enabled server-side', async () => {
    render(<GrillMePanel projectId={projectId} taskId={taskId} onFinalized={vi.fn()} />)

    await waitFor(() => expect(api.listAgentExecutors).toHaveBeenCalled())
    expect(screen.getByRole('option', { name: 'Local LLM chat' })).toBeInTheDocument()
    expect(screen.queryByRole('option', { name: 'Claude Code' })).not.toBeInTheDocument()
  })

  it('offers Claude Code as an executor once the server reports it enabled', async () => {
    vi.mocked(api.listAgentExecutors).mockResolvedValue({ executors: ['claude-code'] })

    render(<GrillMePanel projectId={projectId} taskId={taskId} onFinalized={vi.fn()} />)

    expect(await screen.findByRole('option', { name: 'Claude Code' })).toBeInTheDocument()
  })

  it('never offers "local" as a stage-conversation executor, even if the server reports it', async () => {
    vi.mocked(api.listAgentExecutors).mockResolvedValue({ executors: ['local', 'claude-code'] })

    render(<GrillMePanel projectId={projectId} taskId={taskId} onFinalized={vi.fn()} />)

    await screen.findByRole('option', { name: 'Claude Code' })
    expect(screen.queryByRole('option', { name: 'local' })).not.toBeInTheDocument()
    // "Local LLM chat" (the "" bypass sentinel) is still the always-on entry.
    expect(screen.getByRole('option', { name: 'Local LLM chat' })).toBeInTheDocument()
  })

  it('disables the model select with no models when listModels rejects', async () => {
    vi.mocked(api.listModels).mockRejectedValue(new Error('boom'))

    render(<GrillMePanel projectId={projectId} taskId={taskId} onFinalized={vi.fn()} />)

    expect(await screen.findByText('No models available')).toBeInTheDocument()
    expect(screen.getByLabelText('Model')).toBeDisabled()
  })
})

describe('GrillMePanel — sending a message and streaming the reply', () => {
  it('accumulates streamed content token-by-token', async () => {
    const user = userEvent.setup()
    let deliver!: (event: ChatStreamEvent) => void
    let finish!: () => void
    vi.mocked(api.postStageMessage).mockImplementation((_p, _t, _s, _c, _m, _e, onEvent) => {
      deliver = onEvent
      return new Promise((resolve) => {
        finish = resolve
      })
    })

    render(<GrillMePanel projectId={projectId} taskId={taskId} onFinalized={vi.fn()} />)
    await waitFor(() => expect(api.getStageConversation).toHaveBeenCalled())

    await user.type(screen.getByPlaceholderText('Reply...'), 'Hello there')
    await user.click(screen.getByRole('button', { name: 'Send' }))

    expect(screen.getByRole('button', { name: 'Sending...' })).toBeDisabled()

    act(() => deliver({ content: 'Hel' }))
    expect(screen.getByText(/Hel$/)).toBeInTheDocument()

    act(() => deliver({ content: 'lo' }))
    expect(screen.getByText(/Hello$/)).toBeInTheDocument()

    // handleSend clears the draft textarea as soon as sending starts, so
    // once the stream finishes the button reverts to label "Send" but
    // stays disabled — there's nothing left to send, not a leftover
    // "still sending" state.
    await act(async () => finish())
    await waitFor(() => expect(screen.getByRole('button', { name: 'Send' })).toBeDisabled())
  })

  it('surfaces a mid-stream error event inline', async () => {
    const user = userEvent.setup()
    let deliver!: (event: ChatStreamEvent) => void
    let finish!: () => void
    vi.mocked(api.postStageMessage).mockImplementation((_p, _t, _s, _c, _m, _e, onEvent) => {
      deliver = onEvent
      return new Promise((resolve) => {
        finish = resolve
      })
    })

    render(<GrillMePanel projectId={projectId} taskId={taskId} onFinalized={vi.fn()} />)
    await waitFor(() => expect(api.getStageConversation).toHaveBeenCalled())

    await user.type(screen.getByPlaceholderText('Reply...'), 'Hello')
    await user.click(screen.getByRole('button', { name: 'Send' }))

    act(() => deliver({ error: 'upstream exploded' }))
    expect(screen.getByText('upstream exploded')).toBeInTheDocument()

    await act(async () => finish())
  })
})

describe('GrillMePanel — Draft review', () => {
  async function sendAndReceiveToolCall(argsObject: Record<string, unknown>) {
    const user = userEvent.setup()
    let deliver!: (event: ChatStreamEvent) => void
    vi.mocked(api.postStageMessage).mockImplementation((_p, _t, _s, _c, _m, _e, onEvent) => {
      deliver = onEvent
      return Promise.resolve()
    })

    render(<GrillMePanel projectId={projectId} taskId={taskId} onFinalized={vi.fn()} />)
    await waitFor(() => expect(api.getStageConversation).toHaveBeenCalled())

    await user.type(screen.getByPlaceholderText('Reply...'), 'Please propose a draft')
    await user.click(screen.getByRole('button', { name: 'Send' }))

    act(() => deliver({ tool_call: { name: 'propose_context', arguments: JSON.stringify(argsObject) } }))
  }

  it('surfaces the Draft review UI populated from the tool_call arguments', async () => {
    await sendAndReceiveToolCall({ objective: 'Ship the login page', context: { summary: 'Adds a login page' } })

    expect(screen.getByText('Proposed a draft (propose_context)')).toBeInTheDocument()
    expect(screen.getByText('Proposed draft')).toBeInTheDocument()
    expect(screen.getByLabelText('Objective')).toHaveValue('Ship the login page')
    expect(screen.getByLabelText('Summary')).toHaveValue('Adds a login page')
    expect(screen.getByRole('button', { name: 'Finalize' })).toBeInTheDocument()
    expect(screen.getByRole('button', { name: 'Discard' })).toBeInTheDocument()
  })

  it('discards the draft without calling any API', async () => {
    const user = userEvent.setup()
    await sendAndReceiveToolCall({ objective: 'x', context: { summary: 'y' } })

    await user.click(screen.getByRole('button', { name: 'Discard' }))

    expect(screen.queryByText('Proposed draft')).not.toBeInTheDocument()
    expect(api.finalizeRequirements).not.toHaveBeenCalled()
  })

  it('Finalize calls finalizeRequirements and reports the result via onFinalized', async () => {
    const user = userEvent.setup()
    const onFinalized = vi.fn()
    const resultTask = makeTask({ stage: 'planning' })
    vi.mocked(api.finalizeRequirements).mockResolvedValue({ task: resultTask, context: emptyContext })

    let deliver!: (event: ChatStreamEvent) => void
    vi.mocked(api.postStageMessage).mockImplementation((_p, _t, _s, _c, _m, _e, onEvent) => {
      deliver = onEvent
      return Promise.resolve()
    })

    render(<GrillMePanel projectId={projectId} taskId={taskId} onFinalized={onFinalized} />)
    await waitFor(() => expect(api.getStageConversation).toHaveBeenCalled())

    await user.type(screen.getByPlaceholderText('Reply...'), 'Please propose a draft')
    await user.click(screen.getByRole('button', { name: 'Send' }))
    act(() => deliver({ tool_call: { name: 'propose_context', arguments: JSON.stringify({ objective: 'x', context: { summary: 'y' } }) } }))

    await user.click(screen.getByRole('button', { name: 'Finalize' }))

    await waitFor(() => expect(onFinalized).toHaveBeenCalledWith(resultTask, emptyContext))
    expect(screen.queryByText('Proposed draft')).not.toBeInTheDocument()
  })

  it('shows a finalize error and keeps the draft when finalizeRequirements rejects', async () => {
    const user = userEvent.setup()
    vi.mocked(api.finalizeRequirements).mockRejectedValue(new Error('task is not in the expected stage'))

    let deliver!: (event: ChatStreamEvent) => void
    vi.mocked(api.postStageMessage).mockImplementation((_p, _t, _s, _c, _m, _e, onEvent) => {
      deliver = onEvent
      return Promise.resolve()
    })

    render(<GrillMePanel projectId={projectId} taskId={taskId} onFinalized={vi.fn()} />)
    await waitFor(() => expect(api.getStageConversation).toHaveBeenCalled())

    await user.type(screen.getByPlaceholderText('Reply...'), 'Please propose a draft')
    await user.click(screen.getByRole('button', { name: 'Send' }))
    act(() => deliver({ tool_call: { name: 'propose_context', arguments: JSON.stringify({ objective: 'x', context: { summary: 'y' } }) } }))

    await user.click(screen.getByRole('button', { name: 'Finalize' }))

    expect(await screen.findByText('task is not in the expected stage')).toBeInTheDocument()
    expect(screen.getByText('Proposed draft')).toBeInTheDocument()
  })

  it('silently ignores malformed tool_call arguments JSON — no draft section, no crash', async () => {
    const user = userEvent.setup()
    let deliver!: (event: ChatStreamEvent) => void
    vi.mocked(api.postStageMessage).mockImplementation((_p, _t, _s, _c, _m, _e, onEvent) => {
      deliver = onEvent
      return Promise.resolve()
    })

    render(<GrillMePanel projectId={projectId} taskId={taskId} onFinalized={vi.fn()} />)
    await waitFor(() => expect(api.getStageConversation).toHaveBeenCalled())

    await user.type(screen.getByPlaceholderText('Reply...'), 'Please propose a draft')
    await user.click(screen.getByRole('button', { name: 'Send' }))
    act(() => deliver({ tool_call: { name: 'propose_context', arguments: '{not valid json' } }))

    expect(screen.queryByText('Proposed draft')).not.toBeInTheDocument()
    expect(screen.getByText('Proposed a draft (propose_context)')).toBeInTheDocument()
  })
})

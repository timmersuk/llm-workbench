import { describe, expect, it, vi, beforeEach } from 'vitest'
import { act, render, screen, waitFor, within } from '@testing-library/react'
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
  // 'local' healthy, nothing else — the common-case baseline. Tests that
  // specifically exercise "nothing healthy, including local" override this.
  vi.mocked(api.listAgentExecutors).mockResolvedValue({ executors: ['local'] })
  vi.mocked(api.getStageConversation).mockResolvedValue({ stage: 'requirements', messages: [] })
  // An empty conversation (the default above) auto-starts the interview —
  // resolve immediately by default so tests not specifically about the
  // kickoff aren't left with a stream that never settles.
  vi.mocked(api.startStageConversation).mockResolvedValue()
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

  it('renders a persisted message’s real segments in order (docs/adr/0023), not the tools-then-text fallback', async () => {
    const conv: Conversation = {
      stage: 'requirements',
      messages: [
        { role: 'user', content: 'run the checks', created_at: '2026-01-01T00:00:00Z' },
        {
          role: 'assistant',
          content: 'build passes, now testingtests pass, now checking frontendall green',
          tool_activity: [
            { name: 'Bash', arguments: '{"command":"go test ./..."}', result: 'ok' },
            { name: 'Grep', arguments: '{"pattern":"TODO"}', result: 'no matches' },
          ],
          segments: [
            { kind: 'text', text: 'build passes, now testing' },
            { kind: 'tools', tool_activity: [{ name: 'Bash', arguments: '{"command":"go test ./..."}', result: 'ok' }] },
            { kind: 'text', text: 'tests pass, now checking frontend' },
            { kind: 'tools', tool_activity: [{ name: 'Grep', arguments: '{"pattern":"TODO"}', result: 'no matches' }] },
            { kind: 'text', text: 'all green' },
          ],
          created_at: '2026-01-01T00:00:01Z',
        },
      ],
    }
    vi.mocked(api.getStageConversation).mockResolvedValue(conv)

    render(<GrillMePanel projectId={projectId} taskId={taskId} onFinalized={vi.fn()} />)
    await screen.findByText('build passes, now testing')

    const text = document.body.textContent ?? ''
    const iNarration1 = text.indexOf('build passes, now testing')
    const iBash = text.indexOf('Bash')
    const iNarration2 = text.indexOf('tests pass, now checking frontend')
    const iGrep = text.indexOf('Grep')
    const iNarration3 = text.indexOf('all green')

    expect(iNarration1).toBeGreaterThanOrEqual(0)
    expect(iBash).toBeGreaterThan(iNarration1)
    expect(iNarration2).toBeGreaterThan(iBash)
    expect(iGrep).toBeGreaterThan(iNarration2)
    expect(iNarration3).toBeGreaterThan(iGrep)
  })

  it('does not crash, and still starts on demand, when messages is null', async () => {
    const user = userEvent.setup()
    vi.mocked(api.getStageConversation).mockResolvedValue({ stage: 'requirements', messages: null })

    render(<GrillMePanel projectId={projectId} taskId={taskId} onFinalized={vi.fn()} />)

    await user.click(await screen.findByRole('button', { name: 'Start GrillMe' }))
    await waitFor(() => expect(api.startStageConversation).toHaveBeenCalled())
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

  it('only offers Local LLM chat as the executor when no agent executors are enabled server-side, and local is healthy', async () => {
    vi.mocked(api.listAgentExecutors).mockResolvedValue({ executors: ['local'] })

    render(<GrillMePanel projectId={projectId} taskId={taskId} onFinalized={vi.fn()} />)

    await waitFor(() => expect(api.listAgentExecutors).toHaveBeenCalled())
    expect(screen.getByRole('option', { name: 'Local LLM chat' })).toBeInTheDocument()
    expect(screen.queryByRole('option', { name: 'Claude Code' })).not.toBeInTheDocument()
  })

  it('offers no executor at all, and hides the Model row, when nothing is healthy — not even local', async () => {
    vi.mocked(api.listAgentExecutors).mockResolvedValue({ executors: [] })

    render(<GrillMePanel projectId={projectId} taskId={taskId} onFinalized={vi.fn()} />)

    await waitFor(() => expect(api.listAgentExecutors).toHaveBeenCalled())
    expect(screen.getByRole('option', { name: 'No executor available' })).toBeInTheDocument()
    expect(screen.queryByRole('option', { name: 'Local LLM chat' })).not.toBeInTheDocument()
    expect(screen.getByLabelText('Executor')).toBeDisabled()
    expect(screen.queryByLabelText('Model')).not.toBeInTheDocument()
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
    // "local" being healthy still surfaces as "Local LLM chat" (the "" value) instead.
    expect(screen.getByRole('option', { name: 'Local LLM chat' })).toBeInTheDocument()
  })

  it('disables the model select with no models when listModels rejects', async () => {
    vi.mocked(api.listModels).mockRejectedValue(new Error('boom'))

    render(<GrillMePanel projectId={projectId} taskId={taskId} onFinalized={vi.fn()} />)

    expect(await screen.findByText('No models available')).toBeInTheDocument()
    expect(screen.getByLabelText('Model')).toBeDisabled()
  })

  it('preselects Claude Code as the executor once the server reports it healthy', async () => {
    vi.mocked(api.listAgentExecutors).mockResolvedValue({ executors: ['claude-code'] })

    render(<GrillMePanel projectId={projectId} taskId={taskId} onFinalized={vi.fn()} />)

    await waitFor(() => expect(screen.getByLabelText('Executor')).toHaveValue('claude-code'))
  })

  it('leaves Local LLM chat selected when Claude Code is not reported healthy, but local is', async () => {
    vi.mocked(api.listAgentExecutors).mockResolvedValue({ executors: ['local'] })

    render(<GrillMePanel projectId={projectId} taskId={taskId} onFinalized={vi.fn()} />)

    await waitFor(() => expect(api.listAgentExecutors).toHaveBeenCalled())
    expect(screen.getByLabelText('Executor')).toHaveValue('local')
    expect(screen.getByRole('option', { name: 'Local LLM chat' })).toBeInTheDocument()
  })

  it('labels the panel as GrillMe with an explanatory intro, distinct from the chat history', async () => {
    render(<GrillMePanel projectId={projectId} taskId={taskId} onFinalized={vi.fn()} />)

    expect(await screen.findByRole('heading', { name: 'GrillMe' })).toBeInTheDocument()
    expect(screen.getByText(/Reply below to answer GrillMe's questions/)).toBeInTheDocument()
  })

  it('rehydrates the most recent proposed draft from the loaded conversation', async () => {
    const conv: Conversation = {
      stage: 'requirements',
      messages: [
        { role: 'user', content: "let's start", created_at: '2026-01-01T00:00:00Z' },
        {
          role: 'assistant',
          content: "here's a first pass",
          tool_call: { id: 'call-1', name: 'propose_context', arguments: JSON.stringify({ objective: 'Ship login', context: { summary: 'Adds a login page' } }) },
          created_at: '2026-01-01T00:00:01Z',
        },
      ],
    }
    vi.mocked(api.getStageConversation).mockResolvedValue(conv)

    render(<GrillMePanel projectId={projectId} taskId={taskId} onFinalized={vi.fn()} />)

    expect(await screen.findByText('Proposed draft')).toBeInTheDocument()
    expect(screen.getByLabelText('Objective')).toHaveValue('Ship login')
    expect(screen.getByLabelText('Summary')).toHaveValue('Adds a login page')
  })
})

describe('GrillMePanel — starting an empty conversation on demand', () => {
  it('does not auto-start on mount — it waits for an explicit Start GrillMe, then calls startStageConversation not postStageMessage', async () => {
    const user = userEvent.setup()
    render(<GrillMePanel projectId={projectId} taskId={taskId} onFinalized={vi.fn()} />)

    const start = await screen.findByRole('button', { name: 'Start GrillMe' })
    expect(api.startStageConversation).not.toHaveBeenCalled()
    expect(screen.queryByPlaceholderText('Reply...')).not.toBeInTheDocument()

    await user.click(start)
    await waitFor(() => expect(api.startStageConversation).toHaveBeenCalledTimes(1))
    expect(api.postStageMessage).not.toHaveBeenCalled()
  })

  it('streams the opening question into an assistant-only bubble, with no user message', async () => {
    const user = userEvent.setup()
    let deliver!: (event: ChatStreamEvent) => void
    vi.mocked(api.startStageConversation).mockImplementation((_p, _t, _s, _m, _e, onEvent) => {
      deliver = onEvent
      return new Promise(() => undefined)
    })

    render(<GrillMePanel projectId={projectId} taskId={taskId} onFinalized={vi.fn()} />)
    await user.click(await screen.findByRole('button', { name: 'Start GrillMe' }))
    await waitFor(() => expect(api.startStageConversation).toHaveBeenCalled())

    act(() => deliver({ content: "What's the objective?" }))

    expect(screen.getByText("What's the objective?")).toBeInTheDocument()
    expect(screen.queryByText('user:')).not.toBeInTheDocument()
  })

  it('waits for the preselected executor to resolve before starting, passing it through', async () => {
    const user = userEvent.setup()
    vi.mocked(api.listAgentExecutors).mockResolvedValue({ executors: ['claude-code'] })

    render(<GrillMePanel projectId={projectId} taskId={taskId} onFinalized={vi.fn()} />)
    await waitFor(() => expect(screen.getByLabelText('Executor')).toHaveValue('claude-code'))
    await user.click(await screen.findByRole('button', { name: 'Start GrillMe' }))

    await waitFor(() =>
      expect(api.startStageConversation).toHaveBeenCalledWith(
        projectId,
        taskId,
        'requirements',
        expect.anything(),
        'claude-code',
        expect.anything(),
        expect.anything(),
        expect.anything(),
      ),
    )
  })

  it('does not show a Start button, and never calls startStageConversation, when the conversation already has messages', async () => {
    vi.mocked(api.getStageConversation).mockResolvedValue({
      stage: 'requirements',
      messages: [{ role: 'assistant', content: 'already asked something', created_at: '2026-01-01T00:00:00Z' }],
    })

    render(<GrillMePanel projectId={projectId} taskId={taskId} onFinalized={vi.fn()} />)

    await screen.findByText('already asked something')
    expect(screen.queryByRole('button', { name: 'Start GrillMe' })).not.toBeInTheDocument()
    expect(api.startStageConversation).not.toHaveBeenCalled()
  })
})

describe('GrillMePanel — sending a message and streaming the reply', () => {
  it('shows the just-sent user message immediately, before any reply streams back', async () => {
    const user = userEvent.setup()
    vi.mocked(api.postStageMessage).mockImplementation(() => new Promise(() => undefined)) // stays in-flight

    render(<GrillMePanel projectId={projectId} taskId={taskId} onFinalized={vi.fn()} />)
    await user.click(await screen.findByRole('button', { name: 'Start GrillMe' }))

    await user.type(screen.getByPlaceholderText('Reply...'), 'Add a login page')
    await user.click(screen.getByRole('button', { name: 'Send' }))

    expect(screen.getByText('Add a login page')).toBeInTheDocument()
  })

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
    await user.click(await screen.findByRole('button', { name: 'Start GrillMe' }))

    await user.type(screen.getByPlaceholderText('Reply...'), 'Hello there')
    await user.click(screen.getByRole('button', { name: 'Send' }))

    expect(screen.getByRole('button', { name: 'Stop' })).toBeInTheDocument()
    expect(screen.getByPlaceholderText('Reply...')).toBeDisabled()

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

  it('renders narration and tool activity in the real order they stream (docs/adr/0023), not tools-then-text', async () => {
    const user = userEvent.setup()
    let deliver!: (event: ChatStreamEvent) => void
    vi.mocked(api.postStageMessage).mockImplementation((_p, _t, _s, _c, _m, _e, onEvent) => {
      deliver = onEvent
      return new Promise(() => undefined) // stays in-flight for the duration of this test
    })

    render(<GrillMePanel projectId={projectId} taskId={taskId} onFinalized={vi.fn()} />)
    await user.click(await screen.findByRole('button', { name: 'Start GrillMe' }))

    await user.type(screen.getByPlaceholderText('Reply...'), 'run the checks')
    await user.click(screen.getByRole('button', { name: 'Send' }))

    act(() => deliver({ content: 'build passes, now testing' }))
    act(() => deliver({ tool_activity: { id: 'call-1', phase: 'call', name: 'Bash', arguments: '{"command":"go test ./..."}' } }))
    act(() => deliver({ tool_activity: { id: 'call-1', phase: 'result', name: 'Bash', result: 'ok', is_error: false } }))
    act(() => deliver({ content: 'tests pass, now checking frontend' }))
    act(() => deliver({ tool_activity: { id: 'call-2', phase: 'call', name: 'Grep', arguments: '{"pattern":"TODO"}' } }))
    act(() => deliver({ tool_activity: { id: 'call-2', phase: 'result', name: 'Grep', result: 'no matches', is_error: false } }))
    act(() => deliver({ content: 'all green' }))

    const text = document.body.textContent ?? ''
    const iNarration1 = text.indexOf('build passes, now testing')
    const iBash = text.indexOf('Bash')
    const iNarration2 = text.indexOf('tests pass, now checking frontend')
    const iGrep = text.indexOf('Grep')
    const iNarration3 = text.indexOf('all green')

    expect(iNarration1).toBeGreaterThanOrEqual(0)
    expect(iBash).toBeGreaterThan(iNarration1)
    expect(iNarration2).toBeGreaterThan(iBash)
    expect(iGrep).toBeGreaterThan(iNarration2)
    expect(iNarration3).toBeGreaterThan(iGrep)
  })

  it('attaches each result to the call with its own id, not to whichever call is currently last, when results arrive out of order', async () => {
    const user = userEvent.setup()
    let deliver!: (event: ChatStreamEvent) => void
    vi.mocked(api.postStageMessage).mockImplementation((_p, _t, _s, _c, _m, _e, onEvent) => {
      deliver = onEvent
      return new Promise(() => undefined) // stays in-flight for the duration of this test
    })

    render(<GrillMePanel projectId={projectId} taskId={taskId} onFinalized={vi.fn()} />)
    await user.click(await screen.findByRole('button', { name: 'Start GrillMe' }))

    await user.type(screen.getByPlaceholderText('Reply...'), 'run the checks')
    await user.click(screen.getByRole('button', { name: 'Send' }))

    // Both calls declared (same tool name, so a name-based guess couldn't
    // tell them apart either) before either result arrives — the exact
    // shape a batching provider produces. Results then delivered in
    // REVERSE declaration order.
    act(() => deliver({ tool_activity: { id: 'call-A', phase: 'call', name: 'Bash', arguments: '{"command":"cat unique-path-A"}' } }))
    act(() => deliver({ tool_activity: { id: 'call-B', phase: 'call', name: 'Bash', arguments: '{"command":"cat unique-path-B"}' } }))
    act(() => deliver({ tool_activity: { id: 'call-B', phase: 'result', name: 'Bash', result: 'RESULT-FOR-B', is_error: false } }))
    act(() => deliver({ tool_activity: { id: 'call-A', phase: 'result', name: 'Bash', result: 'RESULT-FOR-A', is_error: false } }))

    const rows = document.querySelectorAll('.tool-activity-row')
    expect(rows).toHaveLength(2)
    expect(rows[0].textContent).toContain('unique-path-A')
    expect(rows[0].textContent).toContain('RESULT-FOR-A')
    expect(rows[1].textContent).toContain('unique-path-B')
    expect(rows[1].textContent).toContain('RESULT-FOR-B')
  })

  it('sends on Enter without needing the Send button', async () => {
    const user = userEvent.setup()
    vi.mocked(api.postStageMessage).mockResolvedValue()

    render(<GrillMePanel projectId={projectId} taskId={taskId} onFinalized={vi.fn()} />)
    await user.click(await screen.findByRole('button', { name: 'Start GrillMe' }))

    await user.type(screen.getByPlaceholderText('Reply...'), 'Hello there{Enter}')

    expect(api.postStageMessage).toHaveBeenCalledWith(
      projectId,
      taskId,
      'requirements',
      'Hello there',
      expect.anything(),
      expect.anything(),
      expect.anything(),
      expect.anything(),
      expect.anything(),
    )
    expect(screen.getByPlaceholderText('Reply...')).toHaveValue('')
  })

  it('Alt+Enter inserts a newline instead of sending', async () => {
    const user = userEvent.setup()
    vi.mocked(api.postStageMessage).mockResolvedValue()

    render(<GrillMePanel projectId={projectId} taskId={taskId} onFinalized={vi.fn()} />)
    await user.click(await screen.findByRole('button', { name: 'Start GrillMe' }))

    const textarea = screen.getByPlaceholderText('Reply...')
    await user.type(textarea, 'line one{Alt>}{Enter}{/Alt}line two')

    expect(textarea).toHaveValue('line one\nline two')
    expect(api.postStageMessage).not.toHaveBeenCalled()
  })

  it('while a reply is still streaming, the input is blocked entirely rather than accepting a second send', async () => {
    const user = userEvent.setup()
    let finishFirstSend!: () => void
    vi.mocked(api.postStageMessage).mockImplementation(
      () =>
        new Promise((resolve) => {
          finishFirstSend = resolve
        }),
    )

    render(<GrillMePanel projectId={projectId} taskId={taskId} onFinalized={vi.fn()} />)
    await user.click(await screen.findByRole('button', { name: 'Start GrillMe' }))

    const textarea = screen.getByPlaceholderText('Reply...')
    await user.type(textarea, 'first message{Enter}')
    expect(await screen.findByRole('button', { name: 'Stop' })).toBeInTheDocument()
    expect(api.postStageMessage).toHaveBeenCalledTimes(1)

    // The textarea is disabled for the whole in-flight send — typing (or
    // pressing Enter) while streaming must have no effect at all, not
    // fall back to inserting a newline.
    expect(textarea).toBeDisabled()
    await user.type(textarea, 'typed while busy{Enter}more text')
    expect(textarea).toHaveValue('')
    expect(api.postStageMessage).toHaveBeenCalledTimes(1)

    await act(async () => finishFirstSend())
  })

  it('rolls back the optimistic message pair and restores the draft when postStageMessage rejects before streaming', async () => {
    // Regression test: postStageMessage rejecting (as opposed to a
    // mid-stream {error} event, covered below) means the server never
    // persisted a turn at all — e.g. it was unreachable, or the request
    // 400'd before handlePostStageMessage ever sent its SSE headers. If the
    // optimistically-appended [user, assistant] pair were left in place,
    // local message indices would permanently disagree with the server's
    // persisted conversation — exactly what previously surfaced later as a
    // "message index N out of range" 400 the moment Edit/Regenerate/Delete
    // acted on a message the server never knew existed.
    const user = userEvent.setup()
    vi.mocked(api.postStageMessage).mockRejectedValue(new Error('Failed to fetch'))

    const { container } = render(<GrillMePanel projectId={projectId} taskId={taskId} onFinalized={vi.fn()} />)
    await user.click(await screen.findByRole('button', { name: 'Start GrillMe' }))
    // Only the kickoff assistant bubble exists before the failed send.
    await waitFor(() => expect(container.querySelectorAll('.chat-message')).toHaveLength(1))

    await user.type(screen.getByPlaceholderText('Reply...'), 'is the server up?')
    await user.click(screen.getByRole('button', { name: 'Send' }))

    await waitFor(() => expect(screen.getByRole('button', { name: 'Send' })).not.toBeDisabled())
    expect(container.querySelectorAll('.chat-message')).toHaveLength(1)
    expect(screen.getByPlaceholderText('Reply...')).toHaveValue('is the server up?')
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
    await user.click(await screen.findByRole('button', { name: 'Start GrillMe' }))

    await user.type(screen.getByPlaceholderText('Reply...'), 'Hello')
    await user.click(screen.getByRole('button', { name: 'Send' }))

    act(() => deliver({ error: 'upstream exploded' }))
    expect(screen.getByText('upstream exploded')).toBeInTheDocument()

    await act(async () => finish())
  })
})

describe('GrillMePanel — message actions', () => {
  const twoMessageConversation: Conversation = {
    stage: 'requirements',
    messages: [
      { role: 'user', content: 'first question', created_at: '2026-01-01T00:00:00Z' },
      { role: 'assistant', content: 'first answer', created_at: '2026-01-01T00:00:01Z' },
    ],
  }

  async function renderWithHistory() {
    vi.mocked(api.getStageConversation).mockResolvedValue(twoMessageConversation)
    const { container } = render(<GrillMePanel projectId={projectId} taskId={taskId} onFinalized={vi.fn()} />)
    await screen.findByText('first answer')
    return container
  }

  it('Copy writes that specific message content to the clipboard', async () => {
    const user = userEvent.setup()
    const container = await renderWithHistory()

    const writeText = vi.fn().mockResolvedValue(undefined)
    Object.defineProperty(navigator, 'clipboard', { value: { writeText }, configurable: true })

    const messages = container.querySelectorAll('.chat-message')
    await user.click(within(messages[0] as HTMLElement).getByRole('button', { name: 'Copy' }))

    expect(writeText).toHaveBeenCalledWith('first question')
  })

  it('Delete calls deleteStageMessage with the right index and removes only that message', async () => {
    const user = userEvent.setup()
    const container = await renderWithHistory()
    vi.mocked(api.deleteStageMessage).mockResolvedValue({
      stage: 'requirements',
      messages: [twoMessageConversation.messages![1]],
    })

    const messages = container.querySelectorAll('.chat-message')
    await user.click(within(messages[0] as HTMLElement).getByRole('button', { name: 'Delete' }))

    await waitFor(() => expect(api.deleteStageMessage).toHaveBeenCalledWith(projectId, taskId, 'requirements', 0))
    expect(screen.queryByText('first question')).not.toBeInTheDocument()
    expect(screen.getByText('first answer')).toBeInTheDocument()
  })

  it('Edit populates the input with that message, and Save calls regenerateStageMessage with the edited text', async () => {
    const user = userEvent.setup()
    const container = await renderWithHistory()
    vi.mocked(api.regenerateStageMessage).mockResolvedValue()

    const messages = container.querySelectorAll('.chat-message')
    await user.click(within(messages[0] as HTMLElement).getByRole('button', { name: 'Edit' }))

    expect(screen.getByDisplayValue('first question')).toBeInTheDocument()
    expect(screen.getByPlaceholderText('Editing message...')).toBeInTheDocument()

    await user.clear(screen.getByPlaceholderText('Editing message...'))
    await user.type(screen.getByPlaceholderText('Editing message...'), 'edited question')
    await user.click(screen.getByRole('button', { name: 'Save' }))

    await waitFor(() =>
      expect(api.regenerateStageMessage).toHaveBeenCalledWith(
        projectId,
        taskId,
        'requirements',
        0,
        'edited question',
        expect.anything(),
        expect.anything(),
        expect.anything(),
        expect.anything(),
        expect.anything(),
      ),
    )
  })

  it('Cancel exits edit mode without calling any API', async () => {
    const user = userEvent.setup()
    const container = await renderWithHistory()

    const messages = container.querySelectorAll('.chat-message')
    await user.click(within(messages[0] as HTMLElement).getByRole('button', { name: 'Edit' }))
    await user.click(screen.getByRole('button', { name: 'Cancel' }))

    expect(screen.getByPlaceholderText('Reply...')).toHaveValue('')
    expect(api.regenerateStageMessage).not.toHaveBeenCalled()
  })

  it('rolls back to the pre-attempt transcript and restores the draft when regenerateStageMessage rejects before streaming', async () => {
    // Regression test for the actual bug report: sending "it's all good"
    // while the server was down left a local-only [user, assistant] pair
    // in place (the old behavior); clicking Regenerate on that dangling
    // assistant bubble then sent an index the server had never heard of,
    // surfacing as a 400 "message index N out of range". Asserting the
    // full pre-attempt transcript is restored (not just that the failed
    // pair disappears) is what actually proves indices can't drift.
    const user = userEvent.setup()
    const container = await renderWithHistory()
    vi.mocked(api.regenerateStageMessage).mockRejectedValue(new Error('Failed to fetch'))

    const messages = container.querySelectorAll('.chat-message')
    await user.click(within(messages[1] as HTMLElement).getByRole('button', { name: 'Regenerate' }))

    await waitFor(() => expect(api.regenerateStageMessage).toHaveBeenCalled())
    await waitFor(() => expect(container.querySelectorAll('.chat-message')).toHaveLength(2))
    const restored = container.querySelectorAll('.chat-message')
    expect(within(restored[0] as HTMLElement).getByText('first question')).toBeInTheDocument()
    expect(within(restored[1] as HTMLElement).getByText('first answer')).toBeInTheDocument()
    expect(screen.getByPlaceholderText('Reply...')).toHaveValue('first question')
  })

  it('Regenerate on an assistant message resends the preceding user message unchanged', async () => {
    const user = userEvent.setup()
    const container = await renderWithHistory()
    vi.mocked(api.regenerateStageMessage).mockResolvedValue()

    const messages = container.querySelectorAll('.chat-message')
    await user.click(within(messages[1] as HTMLElement).getByRole('button', { name: 'Regenerate' }))

    await waitFor(() =>
      expect(api.regenerateStageMessage).toHaveBeenCalledWith(
        projectId,
        taskId,
        'requirements',
        0,
        'first question',
        expect.anything(),
        expect.anything(),
        expect.anything(),
        expect.anything(),
        expect.anything(),
      ),
    )
    expect(screen.queryByText('first answer')).not.toBeInTheDocument()
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
    await user.click(await screen.findByRole('button', { name: 'Start GrillMe' }))

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
    expect(screen.getByRole('button', { name: 'Dismiss draft' })).toBeInTheDocument()
  })

  it('dismisses the draft without calling any API', async () => {
    const user = userEvent.setup()
    await sendAndReceiveToolCall({ objective: 'x', context: { summary: 'y' } })

    await user.click(screen.getByRole('button', { name: 'Dismiss draft' }))

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
    await user.click(await screen.findByRole('button', { name: 'Start GrillMe' }))

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
    await user.click(await screen.findByRole('button', { name: 'Start GrillMe' }))

    await user.type(screen.getByPlaceholderText('Reply...'), 'Please propose a draft')
    await user.click(screen.getByRole('button', { name: 'Send' }))
    act(() => deliver({ tool_call: { name: 'propose_context', arguments: JSON.stringify({ objective: 'x', context: { summary: 'y' } }) } }))

    await user.click(screen.getByRole('button', { name: 'Finalize' }))

    expect(await screen.findByText('task is not in the expected stage')).toBeInTheDocument()
    expect(screen.getByText('Proposed draft')).toBeInTheDocument()
  })

  it('Request changes sends the comment plus the edited draft as a fenced JSON block, then clears the draft', async () => {
    const user = userEvent.setup()
    await sendAndReceiveToolCall({ objective: 'x', context: { summary: 'y' } })

    const postCalls: Array<Parameters<typeof api.postStageMessage>> = []
    vi.mocked(api.postStageMessage).mockImplementation((...args) => {
      postCalls.push(args)
      return Promise.resolve()
    })

    await user.clear(screen.getByLabelText('Objective'))
    await user.type(screen.getByLabelText('Objective'), 'better objective')
    await user.type(screen.getByPlaceholderText(/What should change/), 'tighten this up')
    await user.click(screen.getByRole('button', { name: 'Request changes' }))

    expect(postCalls).toHaveLength(1)
    const sentContent = postCalls[0][3]
    expect(sentContent).toContain('tighten this up')
    expect(sentContent).toContain('```json')
    expect(sentContent).toContain('better objective')
    expect(screen.queryByText('Proposed draft')).not.toBeInTheDocument()
  })

  it('silently ignores malformed tool_call arguments JSON — no draft section, no crash', async () => {
    const user = userEvent.setup()
    let deliver!: (event: ChatStreamEvent) => void
    vi.mocked(api.postStageMessage).mockImplementation((_p, _t, _s, _c, _m, _e, onEvent) => {
      deliver = onEvent
      return Promise.resolve()
    })

    render(<GrillMePanel projectId={projectId} taskId={taskId} onFinalized={vi.fn()} />)
    await user.click(await screen.findByRole('button', { name: 'Start GrillMe' }))

    await user.type(screen.getByPlaceholderText('Reply...'), 'Please propose a draft')
    await user.click(screen.getByRole('button', { name: 'Send' }))
    act(() => deliver({ tool_call: { name: 'propose_context', arguments: '{not valid json' } }))

    expect(screen.queryByText('Proposed draft')).not.toBeInTheDocument()
    expect(screen.getByText('Proposed a draft (propose_context)')).toBeInTheDocument()
  })
})

describe('GrillMePanel — ask_question', () => {
  async function sendAndReceiveQuestion(argsObject: Record<string, unknown>) {
    const user = userEvent.setup()
    let deliver!: (event: ChatStreamEvent) => void
    vi.mocked(api.postStageMessage).mockImplementation((_p, _t, _s, _c, _m, _e, onEvent) => {
      deliver = onEvent
      return Promise.resolve()
    })

    render(<GrillMePanel projectId={projectId} taskId={taskId} onFinalized={vi.fn()} />)
    await user.click(await screen.findByRole('button', { name: 'Start GrillMe' }))

    await user.type(screen.getByPlaceholderText('Reply...'), 'Please ask me a question')
    await user.click(screen.getByRole('button', { name: 'Send' }))

    act(() => deliver({ tool_call: { name: 'ask_question', arguments: JSON.stringify(argsObject) } }))
    return user
  }

  it('renders each option as a button and visually distinguishes the recommended one', async () => {
    await sendAndReceiveQuestion({
      options: ['REST', 'GraphQL', 'gRPC'],
      recommended_option: 'REST',
      recommendation_reason: 'Matches the rest of the codebase',
    })

    expect(screen.getByRole('button', { name: /REST/ })).toBeInTheDocument()
    expect(screen.getByRole('button', { name: 'GraphQL' })).toBeInTheDocument()
    expect(screen.getByRole('button', { name: 'gRPC' })).toBeInTheDocument()
    expect(screen.getByRole('button', { name: /REST/ })).toHaveClass('ask-question-option-recommended')
    expect(screen.getByRole('button', { name: 'GraphQL' })).not.toHaveClass('ask-question-option-recommended')
    expect(screen.getByText('Matches the rest of the codebase')).toBeInTheDocument()
    // The generic "Proposed a draft (...)" chip must not appear for
    // ask_question — it isn't a Draft proposal.
    expect(screen.queryByText(/Proposed a draft/)).not.toBeInTheDocument()
  })

  it('clicking an option sends its label through the normal postStageMessage path and clears the options', async () => {
    const user = await sendAndReceiveQuestion({
      options: ['Yes', 'No'],
      recommended_option: 'Yes',
      recommendation_reason: 'Simpler',
    })
    vi.mocked(api.postStageMessage).mockResolvedValue()

    await user.click(screen.getByRole('button', { name: /Yes/ }))

    await waitFor(() =>
      expect(api.postStageMessage).toHaveBeenLastCalledWith(
        projectId,
        taskId,
        'requirements',
        'Yes',
        expect.anything(),
        expect.anything(),
        expect.anything(),
        expect.anything(),
        expect.anything(),
      ),
    )
    expect(screen.queryByRole('button', { name: /No/ })).not.toBeInTheDocument()
  })

  it('leaves the free-text textarea usable alongside the option buttons, and sending typed text also clears the options', async () => {
    const user = await sendAndReceiveQuestion({
      options: ['Yes', 'No'],
      recommended_option: 'Yes',
      recommendation_reason: 'Simpler',
    })
    vi.mocked(api.postStageMessage).mockResolvedValue()

    const textarea = screen.getByPlaceholderText('Reply...')
    expect(textarea).not.toBeDisabled()
    await user.type(textarea, 'Actually, something else entirely{Enter}')

    await waitFor(() =>
      expect(api.postStageMessage).toHaveBeenLastCalledWith(
        projectId,
        taskId,
        'requirements',
        'Actually, something else entirely',
        expect.anything(),
        expect.anything(),
        expect.anything(),
        expect.anything(),
        expect.anything(),
      ),
    )
    expect(screen.queryByRole('button', { name: /No/ })).not.toBeInTheDocument()
  })

  it('degrades silently when recommended_option does not match any option — all options still render, none highlighted', async () => {
    await sendAndReceiveQuestion({
      options: ['A', 'B'],
      recommended_option: 'C',
      recommendation_reason: 'irrelevant',
    })

    expect(screen.getByRole('button', { name: 'A' })).toBeInTheDocument()
    expect(screen.getByRole('button', { name: 'B' })).toBeInTheDocument()
    expect(screen.getByRole('button', { name: 'A' })).not.toHaveClass('ask-question-option-recommended')
    expect(screen.getByRole('button', { name: 'B' })).not.toHaveClass('ask-question-option-recommended')
  })

  it('does not crash and shows no option buttons when options is missing entirely', async () => {
    await sendAndReceiveQuestion({ recommended_option: 'x', recommendation_reason: 'y' })

    expect(screen.queryByRole('button', { name: 'x' })).not.toBeInTheDocument()
    expect(screen.getByPlaceholderText('Reply...')).toBeInTheDocument()
  })

  it('rehydrates pending options only when ask_question is the very last loaded message', async () => {
    vi.mocked(api.getStageConversation).mockResolvedValue({
      stage: 'requirements',
      messages: [
        {
          role: 'assistant',
          content: 'What transport?',
          tool_call: { id: 'call-1', name: 'ask_question', arguments: JSON.stringify({ options: ['REST', 'gRPC'], recommended_option: 'REST', recommendation_reason: 'simplicity' }) },
          created_at: '2026-01-01T00:00:00Z',
        },
      ],
    })

    render(<GrillMePanel projectId={projectId} taskId={taskId} onFinalized={vi.fn()} />)

    expect(await screen.findByRole('button', { name: /REST/ })).toBeInTheDocument()
    expect(screen.getByRole('button', { name: 'gRPC' })).toBeInTheDocument()
  })

  it('does not rehydrate a stale ask_question that is no longer the last message', async () => {
    vi.mocked(api.getStageConversation).mockResolvedValue({
      stage: 'requirements',
      messages: [
        {
          role: 'assistant',
          content: 'What transport?',
          tool_call: { id: 'call-1', name: 'ask_question', arguments: JSON.stringify({ options: ['REST', 'gRPC'], recommended_option: 'REST', recommendation_reason: 'simplicity' }) },
          created_at: '2026-01-01T00:00:00Z',
        },
        { role: 'user', content: 'REST', created_at: '2026-01-01T00:00:01Z' },
        { role: 'assistant', content: 'Great, next question...', created_at: '2026-01-01T00:00:02Z' },
      ],
    })

    render(<GrillMePanel projectId={projectId} taskId={taskId} onFinalized={vi.fn()} />)

    await screen.findByText('Great, next question...')
    expect(screen.queryByRole('button', { name: /REST/ })).not.toBeInTheDocument()
  })

  it('an ask_question tool_call is never misparsed as the propose_context draft on rehydration', async () => {
    vi.mocked(api.getStageConversation).mockResolvedValue({
      stage: 'requirements',
      messages: [
        {
          role: 'assistant',
          content: 'What transport?',
          tool_call: { id: 'call-1', name: 'ask_question', arguments: JSON.stringify({ options: ['REST'], recommended_option: 'REST', recommendation_reason: 'x' }) },
          created_at: '2026-01-01T00:00:00Z',
        },
      ],
    })

    render(<GrillMePanel projectId={projectId} taskId={taskId} onFinalized={vi.fn()} />)

    await screen.findByRole('button', { name: /REST/ })
    expect(screen.queryByText('Proposed draft')).not.toBeInTheDocument()
  })
})

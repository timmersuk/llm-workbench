import { describe, expect, it, vi, beforeEach } from 'vitest'
import { act, render, screen, waitFor, within } from '@testing-library/react'
import userEvent from '@testing-library/user-event'
import { ChatPanel } from './ChatPanel'
import * as api from './api'
import type { ChatStreamEvent } from './types'

vi.mock('./api')

beforeEach(() => {
  vi.mocked(api.listAgentExecutors).mockResolvedValue({ executors: ['local'] })
  vi.mocked(api.closeChatSession).mockResolvedValue(undefined)
})

describe('ChatPanel — executor and model selection', () => {
  it('offers both registered executors, unfiltered', async () => {
    vi.mocked(api.listAgentExecutors).mockResolvedValue({ executors: ['local', 'claude-code'] })
    vi.mocked(api.listModels).mockResolvedValue({ models: [] })
    render(<ChatPanel />)

    expect(await screen.findByRole('option', { name: 'Local LLM chat' })).toBeInTheDocument()
    expect(screen.getByRole('option', { name: 'Claude Code' })).toBeInTheDocument()
  })

  it('populates the model select and auto-selects the first model when "local" is selected', async () => {
    vi.mocked(api.listModels).mockResolvedValue({ models: ['model-a', 'model-b'] })
    render(<ChatPanel />)

    await waitFor(() => expect(screen.getByLabelText('Model')).toHaveValue('model-a'))
    expect(screen.getByRole('option', { name: 'model-b' })).toBeInTheDocument()
  })

  it('hides the model select for an executor whose capabilities report no models', async () => {
    vi.mocked(api.listAgentExecutors).mockResolvedValue({ executors: ['claude-code'] })
    vi.mocked(api.listModels).mockResolvedValue({ models: [] })
    render(<ChatPanel />)

    await waitFor(() => expect(screen.getByLabelText('Executor')).toHaveValue('claude-code'))
    expect(screen.queryByLabelText('Model')).not.toBeInTheDocument()
  })

  it('shows the model select for claude-code when its capabilities report models', async () => {
    vi.mocked(api.listAgentExecutors).mockResolvedValue({ executors: ['claude-code'] })
    vi.mocked(api.listModels).mockResolvedValue({ models: ['sonnet', 'opus', 'haiku'] })
    render(<ChatPanel />)

    await waitFor(() => expect(screen.getByLabelText('Model')).toHaveValue('sonnet'))
    expect(screen.getByRole('option', { name: 'opus' })).toBeInTheDocument()
  })

  it('populates the effort select from capabilities and resets it on executor change', async () => {
    vi.mocked(api.listAgentExecutors).mockResolvedValue({ executors: ['local', 'claude-code'] })
    // Non-overlapping single-entry effort lists (neither containing the
    // 'medium' initial default) so a value change here can only come from
    // resolveEffort actually re-deriving from the newly-fetched capability,
    // not from the prior selection happening to still be valid.
    vi.mocked(api.listModels).mockImplementation((executor) =>
      executor === 'claude-code'
        ? Promise.resolve({ models: ['sonnet'], efforts: ['high'], default_effort: 'high' })
        : Promise.resolve({ models: ['model-a'], efforts: ['low'], default_effort: 'low' }),
    )
    const user = userEvent.setup()
    render(<ChatPanel />)

    await waitFor(() => expect(screen.getByLabelText('Effort')).toHaveValue('low'))
    await user.selectOptions(screen.getByLabelText('Executor'), 'claude-code')
    await waitFor(() => expect(screen.getByLabelText('Effort')).toHaveValue('high'))
  })

  it('shows an inline error when listModels rejects', async () => {
    vi.mocked(api.listModels).mockRejectedValue(new Error('boom'))
    render(<ChatPanel />)

    expect(await screen.findByText('Could not load models: boom')).toBeInTheDocument()
  })

  it('sends the selected effort alongside every turn', async () => {
    vi.mocked(api.listModels).mockResolvedValue({ models: ['model-a'], efforts: ['low', 'medium', 'high'], default_effort: 'medium' })
    vi.mocked(api.streamChatCompletion).mockImplementation((_content, _model, _executor, _sessionKey, onEvent) => {
      onEvent({ content: 'ok' })
      return Promise.resolve()
    })
    const user = userEvent.setup()
    render(<ChatPanel />)

    await waitFor(() => expect(screen.getByLabelText('Effort')).toHaveValue('medium'))
    await user.selectOptions(screen.getByLabelText('Effort'), 'high')
    await user.type(screen.getByPlaceholderText('Message the local LLM...'), 'hello')
    await user.click(screen.getByRole('button', { name: 'Send' }))

    await waitFor(() => expect(api.streamChatCompletion).toHaveBeenCalled())
    const call = vi.mocked(api.streamChatCompletion).mock.calls.at(-1)!
    expect(call[7]).toBe('high')
  })
})

describe('ChatPanel — message actions', () => {
  async function sendOneExchange(user: ReturnType<typeof userEvent.setup>, text: string, reply: string) {
    vi.mocked(api.listModels).mockResolvedValue({ models: ['model-a'] })
    vi.mocked(api.streamChatCompletion).mockImplementation((_content, _model, _executor, _sessionKey, onEvent) => {
      onEvent({ content: reply })
      return Promise.resolve()
    })
    const { container } = render(<ChatPanel />)
    await waitFor(() => expect(screen.getByLabelText('Model')).toHaveValue('model-a'))

    await user.type(screen.getByPlaceholderText('Message the local LLM...'), text)
    await user.click(screen.getByRole('button', { name: 'Send' }))
    await waitFor(() => expect(screen.getByText(new RegExp(`${reply}$`))).toBeInTheDocument())
    return container
  }

  it('Copy writes that specific message content to the clipboard', async () => {
    const user = userEvent.setup()
    const container = await sendOneExchange(user, 'hello', 'hi back')

    const writeText = vi.fn().mockResolvedValue(undefined)
    Object.defineProperty(navigator, 'clipboard', { value: { writeText }, configurable: true })

    const messages = container.querySelectorAll('.chat-message')
    await user.click(within(messages[0] as HTMLElement).getByRole('button', { name: 'Copy' }))

    expect(writeText).toHaveBeenCalledWith('hello')
  })

  it('Delete removes only that message and evicts the session', async () => {
    const user = userEvent.setup()
    const container = await sendOneExchange(user, 'hello', 'hi back')

    const messages = container.querySelectorAll('.chat-message')
    await user.click(within(messages[0] as HTMLElement).getByRole('button', { name: 'Delete' }))

    await waitFor(() => expect(screen.queryByText(/hello$/)).not.toBeInTheDocument())
    expect(screen.getByText(/hi back$/)).toBeInTheDocument()
    expect(api.closeChatSession).toHaveBeenCalled()
  })

  it('Edit populates the input, and Save evicts the session and resends with truncated history', async () => {
    const user = userEvent.setup()
    const container = await sendOneExchange(user, 'hello', 'hi back')

    const messages = container.querySelectorAll('.chat-message')
    await user.click(within(messages[0] as HTMLElement).getByRole('button', { name: 'Edit' }))

    expect(screen.getByDisplayValue('hello')).toBeInTheDocument()
    expect(screen.getByPlaceholderText('Editing message...')).toBeInTheDocument()

    await user.clear(screen.getByPlaceholderText('Editing message...'))
    await user.type(screen.getByPlaceholderText('Editing message...'), 'edited hello')
    await user.click(screen.getByRole('button', { name: 'Save' }))

    await waitFor(() => expect(api.closeChatSession).toHaveBeenCalled())
    const call = vi.mocked(api.streamChatCompletion).mock.calls.at(-1)!
    expect(call[0]).toBe('edited hello')
    expect(call[5]).toEqual([])
    expect(screen.getByText(/edited hello$/)).toBeInTheDocument()
    expect(screen.queryByText(/^hello$/)).not.toBeInTheDocument()
  })

  it('Cancel exits edit mode without sending anything', async () => {
    const user = userEvent.setup()
    const container = await sendOneExchange(user, 'hello', 'hi back')

    const messages = container.querySelectorAll('.chat-message')
    await user.click(within(messages[0] as HTMLElement).getByRole('button', { name: 'Edit' }))
    await user.click(screen.getByRole('button', { name: 'Cancel' }))

    expect(screen.getByPlaceholderText('Message the local LLM...')).toHaveValue('')
    expect(api.closeChatSession).not.toHaveBeenCalled()
  })

  it('Regenerate resends the preceding user message unchanged, truncating the stale reply', async () => {
    const user = userEvent.setup()
    const container = await sendOneExchange(user, 'hello', 'hi back')

    const messages = container.querySelectorAll('.chat-message')
    vi.mocked(api.streamChatCompletion).mockImplementation((_content, _model, _executor, _sessionKey, onEvent) => {
      onEvent({ content: 'a fresh reply' })
      return Promise.resolve()
    })
    await user.click(within(messages[1] as HTMLElement).getByRole('button', { name: 'Regenerate' }))

    await waitFor(() => expect(api.closeChatSession).toHaveBeenCalled())
    const call = vi.mocked(api.streamChatCompletion).mock.calls.at(-1)!
    expect(call[0]).toBe('hello')
    expect(call[5]).toEqual([])
    expect(screen.getByText(/a fresh reply$/)).toBeInTheDocument()
    expect(screen.queryByText(/hi back$/)).not.toBeInTheDocument()
  })

})

describe('ChatPanel — keyboard input', () => {
  it('sends on Enter without needing the Send button', async () => {
    const user = userEvent.setup()
    vi.mocked(api.listModels).mockResolvedValue({ models: ['model-a'] })
    vi.mocked(api.streamChatCompletion).mockResolvedValue()

    render(<ChatPanel />)
    await waitFor(() => expect(screen.getByLabelText('Model')).toHaveValue('model-a'))

    await user.type(screen.getByPlaceholderText('Message the local LLM...'), 'Hello there{Enter}')

    expect(api.streamChatCompletion).toHaveBeenCalled()
    const call = vi.mocked(api.streamChatCompletion).mock.calls.at(-1)!
    expect(call[0]).toBe('Hello there')
    expect(screen.getByPlaceholderText('Message the local LLM...')).toHaveValue('')
  })

  it('Alt+Enter inserts a newline instead of sending', async () => {
    const user = userEvent.setup()
    vi.mocked(api.listModels).mockResolvedValue({ models: ['model-a'] })

    render(<ChatPanel />)
    await waitFor(() => expect(screen.getByLabelText('Model')).toHaveValue('model-a'))

    const textarea = screen.getByPlaceholderText('Message the local LLM...')
    await user.type(textarea, 'line one{Alt>}{Enter}{/Alt}line two')

    expect(textarea).toHaveValue('line one\nline two')
    expect(api.streamChatCompletion).not.toHaveBeenCalled()
  })

  it('while a reply is still streaming, Enter has no effect', async () => {
    const user = userEvent.setup()
    vi.mocked(api.listModels).mockResolvedValue({ models: ['model-a'] })
    let finishFirstSend!: () => void
    vi.mocked(api.streamChatCompletion).mockImplementation(
      () =>
        new Promise((resolve) => {
          finishFirstSend = resolve
        }),
    )

    render(<ChatPanel />)
    await waitFor(() => expect(screen.getByLabelText('Model')).toHaveValue('model-a'))

    const textarea = screen.getByPlaceholderText('Message the local LLM...')
    await user.type(textarea, 'first message{Enter}')
    expect(api.streamChatCompletion).toHaveBeenCalledTimes(1)
    expect(textarea).toBeDisabled()

    await user.type(textarea, 'typed while busy{Enter}more text')
    expect(textarea).toHaveValue('')
    expect(api.streamChatCompletion).toHaveBeenCalledTimes(1)

    await act(async () => finishFirstSend())
  })
})

describe('ChatPanel — streaming', () => {
  it('accumulates streamed content and surfaces a mid-stream error', async () => {
    const user = userEvent.setup()
    vi.mocked(api.listModels).mockResolvedValue({ models: ['model-a'] })

    let deliver!: (event: ChatStreamEvent) => void
    let finish!: () => void
    vi.mocked(api.streamChatCompletion).mockImplementation((_content, _model, _executor, _sessionKey, onEvent) => {
      deliver = onEvent
      return new Promise((resolve) => {
        finish = resolve
      })
    })

    render(<ChatPanel />)
    await waitFor(() => expect(screen.getByLabelText('Model')).toHaveValue('model-a'))

    await user.type(screen.getByPlaceholderText('Message the local LLM...'), 'Hello there')
    await user.click(screen.getByRole('button', { name: 'Send' }))

    act(() => deliver({ content: 'Hi' }))
    expect(screen.getByText(/Hi$/)).toBeInTheDocument()

    act(() => deliver({ error: 'upstream exploded' }))
    expect(screen.getByText('upstream exploded')).toBeInTheDocument()

    await act(async () => finish())
  })

  it('sends the same session key for every turn until New chat is clicked', async () => {
    const user = userEvent.setup()
    vi.mocked(api.listModels).mockResolvedValue({ models: ['model-a'] })
    vi.mocked(api.streamChatCompletion).mockImplementation((_content, _model, _executor, _sessionKey, onEvent) => {
      onEvent({ content: 'ok' })
      return Promise.resolve()
    })

    render(<ChatPanel />)
    await waitFor(() => expect(screen.getByLabelText('Model')).toHaveValue('model-a'))

    await user.type(screen.getByPlaceholderText('Message the local LLM...'), 'first')
    await user.click(screen.getByRole('button', { name: 'Send' }))
    await user.type(screen.getByPlaceholderText('Message the local LLM...'), 'second')
    await user.click(screen.getByRole('button', { name: 'Send' }))

    const calls = vi.mocked(api.streamChatCompletion).mock.calls
    expect(calls[0][3]).toBe(calls[1][3])
  })

  it('New chat closes the session, clears the transcript, and starts a fresh session key', async () => {
    const user = userEvent.setup()
    vi.mocked(api.listModels).mockResolvedValue({ models: ['model-a'] })
    vi.mocked(api.closeChatSession).mockResolvedValue(undefined)
    let sentSessionKey = ''
    vi.mocked(api.streamChatCompletion).mockImplementation((_content, _model, _executor, sessionKey, onEvent) => {
      sentSessionKey = sessionKey
      onEvent({ content: 'ok' })
      return Promise.resolve()
    })

    render(<ChatPanel />)
    await waitFor(() => expect(screen.getByLabelText('Model')).toHaveValue('model-a'))

    await user.type(screen.getByPlaceholderText('Message the local LLM...'), 'hello')
    await user.click(screen.getByRole('button', { name: 'Send' }))
    expect(screen.getByText(/hello$/)).toBeInTheDocument()
    const firstSessionKey = sentSessionKey

    await user.click(screen.getByRole('button', { name: 'New chat' }))
    await waitFor(() => expect(api.closeChatSession).toHaveBeenCalledWith(firstSessionKey))
    expect(screen.queryByText(/hello$/)).not.toBeInTheDocument()

    await user.type(screen.getByPlaceholderText('Message the local LLM...'), 'again')
    await user.click(screen.getByRole('button', { name: 'Send' }))
    expect(sentSessionKey).not.toBe(firstSessionKey)
  })

  it('streams reasoning_content into an open "Thinking" panel that collapses once content arrives', async () => {
    const user = userEvent.setup()
    vi.mocked(api.listModels).mockResolvedValue({ models: ['model-a'] })

    let deliver!: (event: ChatStreamEvent) => void
    vi.mocked(api.streamChatCompletion).mockImplementation((_content, _model, _executor, _sessionKey, onEvent) => {
      deliver = onEvent
      return Promise.resolve()
    })

    render(<ChatPanel />)
    await waitFor(() => expect(screen.getByLabelText('Model')).toHaveValue('model-a'))

    await user.type(screen.getByPlaceholderText('Message the local LLM...'), 'Hello')
    await user.click(screen.getByRole('button', { name: 'Send' }))

    act(() => deliver({ reasoning_content: 'Thinking about it...' }))
    const details = screen.getByText('Thinking').closest('details')
    expect(details).toHaveAttribute('open')

    act(() => deliver({ content: 'Final answer' }))
    expect(details).not.toHaveAttribute('open')
  })

  it('lets the user manually toggle the Thinking panel open again', async () => {
    const user = userEvent.setup()
    vi.mocked(api.listModels).mockResolvedValue({ models: ['model-a'] })

    let deliver!: (event: ChatStreamEvent) => void
    vi.mocked(api.streamChatCompletion).mockImplementation((_content, _model, _executor, _sessionKey, onEvent) => {
      deliver = onEvent
      return Promise.resolve()
    })

    render(<ChatPanel />)
    await waitFor(() => expect(screen.getByLabelText('Model')).toHaveValue('model-a'))

    await user.type(screen.getByPlaceholderText('Message the local LLM...'), 'Hello')
    await user.click(screen.getByRole('button', { name: 'Send' }))

    act(() => deliver({ reasoning_content: 'Thinking...' }))
    act(() => deliver({ content: 'Answer' }))

    const summary = screen.getByText('Thinking')
    await user.click(summary)

    const details = summary.closest('details')
    await waitFor(() => expect(details).toHaveAttribute('open'))
  })
})

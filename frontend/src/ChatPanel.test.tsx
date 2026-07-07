import { describe, expect, it, vi, beforeEach } from 'vitest'
import { act, render, screen, waitFor } from '@testing-library/react'
import userEvent from '@testing-library/user-event'
import { ChatPanel } from './ChatPanel'
import * as api from './api'
import type { ChatStreamEvent } from './types'

vi.mock('./api')

beforeEach(() => {
  vi.mocked(api.listAgentExecutors).mockResolvedValue({ executors: ['local'] })
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

  it('hides the model select when a non-"local" executor is selected', async () => {
    vi.mocked(api.listAgentExecutors).mockResolvedValue({ executors: ['claude-code'] })
    vi.mocked(api.listModels).mockResolvedValue({ models: ['model-a'] })
    render(<ChatPanel />)

    await waitFor(() => expect(screen.getByLabelText('Executor')).toHaveValue('claude-code'))
    expect(screen.queryByLabelText('Model')).not.toBeInTheDocument()
  })

  it('shows an inline error when listModels rejects, only while "local" is selected', async () => {
    vi.mocked(api.listModels).mockRejectedValue(new Error('boom'))
    render(<ChatPanel />)

    expect(await screen.findByText('Could not load models: boom')).toBeInTheDocument()
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

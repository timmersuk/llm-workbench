import { describe, expect, it, vi } from 'vitest'
import { act, render, screen, waitFor } from '@testing-library/react'
import userEvent from '@testing-library/user-event'
import { ChatPanel } from './ChatPanel'
import * as api from './api'
import type { ChatStreamEvent } from './types'

vi.mock('./api')

describe('ChatPanel — model list', () => {
  it('populates the model select and auto-selects the first model', async () => {
    vi.mocked(api.listModels).mockResolvedValue({ models: ['model-a', 'model-b'] })
    render(<ChatPanel />)

    await waitFor(() => expect(screen.getByLabelText('Model')).toHaveValue('model-a'))
    expect(screen.getByRole('option', { name: 'model-b' })).toBeInTheDocument()
  })

  it('shows an inline error when listModels rejects', async () => {
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
    vi.mocked(api.streamChatCompletion).mockImplementation((_messages, _model, onEvent) => {
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

  it('streams reasoning_content into an open "Thinking" panel that collapses once content arrives', async () => {
    const user = userEvent.setup()
    vi.mocked(api.listModels).mockResolvedValue({ models: ['model-a'] })

    let deliver!: (event: ChatStreamEvent) => void
    vi.mocked(api.streamChatCompletion).mockImplementation((_messages, _model, onEvent) => {
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
    vi.mocked(api.streamChatCompletion).mockImplementation((_messages, _model, onEvent) => {
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

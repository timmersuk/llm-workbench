import { describe, expect, it, vi } from 'vitest'
import { render, screen, waitFor } from '@testing-library/react'
import userEvent from '@testing-library/user-event'
import App from './App'
import * as api from './api'

vi.mock('./api')
vi.mock('./ProjectsPanel', () => ({
  ProjectsPanel: () => <div data-testid="projects-panel">projects panel</div>,
}))
vi.mock('./ChatPanel', () => ({
  ChatPanel: () => <div data-testid="chat-panel">chat panel</div>,
}))

describe('App', () => {
  it('defaults to the Projects tab', async () => {
    vi.mocked(api.getHealthStatus).mockResolvedValue({ status: 'ok', build_id: 'abc123' })
    render(<App />)

    expect(screen.getByTestId('projects-panel')).toBeInTheDocument()
    expect(screen.queryByTestId('chat-panel')).not.toBeInTheDocument()
  })

  it('switches to the Chat tab when clicked', async () => {
    const user = userEvent.setup()
    vi.mocked(api.getHealthStatus).mockResolvedValue({ status: 'ok', build_id: 'abc123' })
    render(<App />)

    await user.click(screen.getByRole('button', { name: 'Chat' }))

    expect(screen.getByTestId('chat-panel')).toBeInTheDocument()
    expect(screen.queryByTestId('projects-panel')).not.toBeInTheDocument()
  })

  it('shows "build dev" before the healthcheck resolves, then the real build_id', async () => {
    vi.mocked(api.getHealthStatus).mockResolvedValue({ status: 'ok', build_id: 'abc123' })
    render(<App />)

    expect(screen.getByText('build dev')).toBeInTheDocument()
    await waitFor(() => expect(screen.getByText('build abc123')).toBeInTheDocument())
  })

  it('falls back to "build dev" when the healthcheck rejects', async () => {
    vi.mocked(api.getHealthStatus).mockRejectedValue(new Error('down'))
    render(<App />)

    await waitFor(() => expect(api.getHealthStatus).toHaveBeenCalled())
    expect(screen.getByText('build dev')).toBeInTheDocument()
  })
})

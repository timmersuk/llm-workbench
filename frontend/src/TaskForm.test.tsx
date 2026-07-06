import { describe, expect, it, vi } from 'vitest'
import { render, screen } from '@testing-library/react'
import userEvent from '@testing-library/user-event'
import { TaskForm } from './TaskForm'

describe('TaskForm', () => {
  it('disables Save until both id and title are filled', async () => {
    const user = userEvent.setup()
    render(<TaskForm onSubmit={vi.fn()} onCancel={vi.fn()} />)

    expect(screen.getByRole('button', { name: 'Save' })).toBeDisabled()

    await user.type(screen.getByLabelText('ID'), 'my-task')
    expect(screen.getByRole('button', { name: 'Save' })).toBeDisabled()

    await user.type(screen.getByLabelText('Title'), 'My Task')
    expect(screen.getByRole('button', { name: 'Save' })).toBeEnabled()
  })

  it('submits id/title/references built from the referenced-knowledge and repo fields', async () => {
    const user = userEvent.setup()
    const onSubmit = vi.fn().mockResolvedValue(undefined)
    render(<TaskForm onSubmit={onSubmit} onCancel={vi.fn()} />)

    await user.type(screen.getByLabelText('ID'), 'my-task')
    await user.type(screen.getByLabelText('Title'), 'My Task')
    await user.type(screen.getByLabelText('Referenced knowledge (one per line)'), 'coding-standards/logging')
    await user.type(screen.getByLabelText('Referenced repos (one per line)'), 'github.com/org/repo')
    await user.click(screen.getByRole('button', { name: 'Save' }))

    expect(onSubmit).toHaveBeenCalledWith({
      id: 'my-task',
      title: 'My Task',
      references: { knowledge: ['coding-standards/logging'], repo: ['github.com/org/repo'] },
    })
  })

  it('shows an inline error and re-enables Save when onSubmit rejects', async () => {
    const user = userEvent.setup()
    const onSubmit = vi.fn().mockRejectedValue(new Error('task already exists'))
    render(<TaskForm onSubmit={onSubmit} onCancel={vi.fn()} />)

    await user.type(screen.getByLabelText('ID'), 'dup-task')
    await user.type(screen.getByLabelText('Title'), 'Dup')
    await user.click(screen.getByRole('button', { name: 'Save' }))

    expect(await screen.findByText('task already exists')).toBeInTheDocument()
    expect(screen.getByRole('button', { name: 'Save' })).toBeEnabled()
  })

  it('calls onCancel when Cancel is clicked', async () => {
    const user = userEvent.setup()
    const onCancel = vi.fn()
    render(<TaskForm onSubmit={vi.fn()} onCancel={onCancel} />)

    await user.click(screen.getByRole('button', { name: 'Cancel' }))
    expect(onCancel).toHaveBeenCalled()
  })
})

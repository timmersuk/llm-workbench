import { describe, expect, it, vi } from 'vitest'
import { render, screen } from '@testing-library/react'
import userEvent from '@testing-library/user-event'
import { ProjectForm } from './ProjectForm'
import type { Project } from './types'

const project: Project = {
  id: 'demo',
  name: 'Demo Project',
  description: 'A demo project',
  repositories: ['github.com/org/demo'],
  knowledge: ['coding-standards/logging'],
  constraints: ['keep it simple'],
  created_at: '2026-01-01T00:00:00Z',
  updated_at: '2026-01-01T00:00:00Z',
}

describe('ProjectForm', () => {
  it('create mode has no ID field and disables Save until a name is entered', async () => {
    const user = userEvent.setup()
    render(<ProjectForm onSubmit={vi.fn()} onCancel={vi.fn()} />)

    expect(screen.queryByLabelText('ID')).not.toBeInTheDocument()
    expect(screen.getByRole('button', { name: 'Save' })).toBeDisabled()

    await user.type(screen.getByLabelText('Name'), 'New Project')
    expect(screen.getByRole('button', { name: 'Save' })).toBeEnabled()
  })

  it('edit mode shows a disabled ID field and pre-populates every field from initial', () => {
    render(<ProjectForm initial={project} onSubmit={vi.fn()} onCancel={vi.fn()} />)

    expect(screen.getByLabelText('ID')).toBeDisabled()
    expect(screen.getByLabelText('ID')).toHaveValue('demo')
    expect(screen.getByLabelText('Name')).toHaveValue('Demo Project')
    expect(screen.getByLabelText('Repositories (one per line)')).toHaveValue('github.com/org/demo')
    expect(screen.getByLabelText('Knowledge (one per line)')).toHaveValue('coding-standards/logging')
    expect(screen.getByLabelText('Constraints (one per line)')).toHaveValue('keep it simple')
  })

  it('shows an inline error and re-enables Save when onSubmit rejects', async () => {
    const user = userEvent.setup()
    const onSubmit = vi.fn().mockRejectedValue(new Error('project already exists'))
    render(<ProjectForm initial={project} onSubmit={onSubmit} onCancel={vi.fn()} />)

    await user.click(screen.getByRole('button', { name: 'Save' }))

    expect(await screen.findByText('project already exists')).toBeInTheDocument()
    expect(screen.getByRole('button', { name: 'Save' })).toBeEnabled()
  })

  it('calls onCancel when Cancel is clicked', async () => {
    const user = userEvent.setup()
    const onCancel = vi.fn()
    render(<ProjectForm onSubmit={vi.fn()} onCancel={onCancel} />)

    await user.click(screen.getByRole('button', { name: 'Cancel' }))
    expect(onCancel).toHaveBeenCalled()
  })
})

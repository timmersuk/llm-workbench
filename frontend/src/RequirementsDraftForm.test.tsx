import { describe, expect, it, vi } from 'vitest'
import { render, screen } from '@testing-library/react'
import { fireEvent } from '@testing-library/react'
import { RequirementsDraftForm } from './RequirementsDraftForm'
import type { RequirementsDraft } from './types'

const draft: RequirementsDraft = {
  objective: 'Ship the login page',
  constraints: ['No new deps'],
  assumptions: ['Auth service exists'],
  success_criteria: ['User can log in'],
  context: {
    summary: 'Adds a login page',
    background: 'Users currently cannot log in',
    files: ['LoginPage.tsx'],
    detail: 'Some detail',
    verification: ['Manually log in'],
    open_questions: ['What about SSO?'],
  },
}

describe('RequirementsDraftForm', () => {
  it('renders every field of a populated draft', () => {
    render(<RequirementsDraftForm draft={draft} onChange={vi.fn()} />)
    expect(screen.getByLabelText('Objective')).toHaveValue('Ship the login page')
    expect(screen.getByLabelText('Constraints (one per line)')).toHaveValue('No new deps')
    expect(screen.getByLabelText('Summary')).toHaveValue('Adds a login page')
    expect(screen.getByLabelText('Files (one per line)')).toHaveValue('LoginPage.tsx')
  })

  it('editing the objective calls onChange with only that field changed', () => {
    const onChange = vi.fn()
    render(<RequirementsDraftForm draft={draft} onChange={onChange} />)
    fireEvent.change(screen.getByLabelText('Objective'), { target: { value: 'Ship it faster' } })
    expect(onChange).toHaveBeenCalledWith({ ...draft, objective: 'Ship it faster' })
  })

  it('editing a list field round-trips through linesToList', () => {
    const onChange = vi.fn()
    render(<RequirementsDraftForm draft={draft} onChange={onChange} />)
    fireEvent.change(screen.getByLabelText('Constraints (one per line)'), {
      target: { value: 'No new deps\nKeep it simple' },
    })
    expect(onChange).toHaveBeenCalledWith({ ...draft, constraints: ['No new deps', 'Keep it simple'] })
  })

  it('editing a nested context field preserves the rest of the context', () => {
    const onChange = vi.fn()
    render(<RequirementsDraftForm draft={draft} onChange={onChange} />)
    fireEvent.change(screen.getByLabelText('Summary'), { target: { value: 'Updated summary' } })
    expect(onChange).toHaveBeenCalledWith({
      ...draft,
      context: { ...draft.context, summary: 'Updated summary' },
    })
  })
})

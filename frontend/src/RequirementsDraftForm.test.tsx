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
    files: [{ path: 'LoginPage.tsx', role: 'the new page component' }],
    detail: 'Some detail',
    verification: [{ description: 'Manually log in', kind: 'human_judgment' }],
    open_questions: ['What about SSO?'],
  },
}

describe('RequirementsDraftForm', () => {
  it('renders every field of a populated draft', () => {
    render(<RequirementsDraftForm draft={draft} onChange={vi.fn()} />)
    expect(screen.getByLabelText('Objective')).toHaveValue('Ship the login page')
    expect(screen.getByLabelText('Constraints (one per line)')).toHaveValue('No new deps')
    expect(screen.getByLabelText('Summary')).toHaveValue('Adds a login page')
    expect(screen.getByLabelText('File 1 path')).toHaveValue('LoginPage.tsx')
    expect(screen.getByLabelText('File 1 role')).toHaveValue('the new page component')
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

  it('renders each verification step as a description input and kind selector', () => {
    render(<RequirementsDraftForm draft={draft} onChange={vi.fn()} />)
    expect(screen.getByLabelText('Verification step 1 description')).toHaveValue('Manually log in')
    expect(screen.getByLabelText('Verification step 1 kind')).toHaveValue('human_judgment')
  })

  it('editing a verification step description changes only that step', () => {
    const onChange = vi.fn()
    render(<RequirementsDraftForm draft={draft} onChange={onChange} />)
    fireEvent.change(screen.getByLabelText('Verification step 1 description'), {
      target: { value: 'Log in via SSO' },
    })
    expect(onChange).toHaveBeenCalledWith({
      ...draft,
      context: { ...draft.context, verification: [{ description: 'Log in via SSO', kind: 'human_judgment' }] },
    })
  })

  it('editing a verification step kind changes only that step', () => {
    const onChange = vi.fn()
    render(<RequirementsDraftForm draft={draft} onChange={onChange} />)
    fireEvent.change(screen.getByLabelText('Verification step 1 kind'), {
      target: { value: 'agent_executable' },
    })
    expect(onChange).toHaveBeenCalledWith({
      ...draft,
      context: { ...draft.context, verification: [{ description: 'Manually log in', kind: 'agent_executable' }] },
    })
  })

  it('adding a verification step appends an agent_executable entry', () => {
    const onChange = vi.fn()
    render(<RequirementsDraftForm draft={draft} onChange={onChange} />)
    fireEvent.click(screen.getByText('Add verification step'))
    expect(onChange).toHaveBeenCalledWith({
      ...draft,
      context: {
        ...draft.context,
        verification: [
          { description: 'Manually log in', kind: 'human_judgment' },
          { description: '', kind: 'agent_executable' },
        ],
      },
    })
  })

  it('removing a verification step drops it', () => {
    const onChange = vi.fn()
    render(<RequirementsDraftForm draft={draft} onChange={onChange} />)
    fireEvent.click(screen.getByLabelText('Remove verification step 1'))
    expect(onChange).toHaveBeenCalledWith({
      ...draft,
      context: { ...draft.context, verification: [] },
    })
  })

  it('editing a file path changes only that file', () => {
    const onChange = vi.fn()
    render(<RequirementsDraftForm draft={draft} onChange={onChange} />)
    fireEvent.change(screen.getByLabelText('File 1 path'), { target: { value: 'LoginForm.tsx' } })
    expect(onChange).toHaveBeenCalledWith({
      ...draft,
      context: { ...draft.context, files: [{ path: 'LoginForm.tsx', role: 'the new page component' }] },
    })
  })

  it('editing a file role changes only that file', () => {
    const onChange = vi.fn()
    render(<RequirementsDraftForm draft={draft} onChange={onChange} />)
    fireEvent.change(screen.getByLabelText('File 1 role'), { target: { value: 'updated role' } })
    expect(onChange).toHaveBeenCalledWith({
      ...draft,
      context: { ...draft.context, files: [{ path: 'LoginPage.tsx', role: 'updated role' }] },
    })
  })

  it('adding a file appends an empty entry', () => {
    const onChange = vi.fn()
    render(<RequirementsDraftForm draft={draft} onChange={onChange} />)
    fireEvent.click(screen.getByText('Add file'))
    expect(onChange).toHaveBeenCalledWith({
      ...draft,
      context: {
        ...draft.context,
        files: [{ path: 'LoginPage.tsx', role: 'the new page component' }, { path: '', role: '' }],
      },
    })
  })

  it('removing a file drops it', () => {
    const onChange = vi.fn()
    render(<RequirementsDraftForm draft={draft} onChange={onChange} />)
    fireEvent.click(screen.getByLabelText('Remove file 1'))
    expect(onChange).toHaveBeenCalledWith({
      ...draft,
      context: { ...draft.context, files: [] },
    })
  })
})

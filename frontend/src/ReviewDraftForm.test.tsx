import { describe, expect, it, vi } from 'vitest'
import { fireEvent, render, screen } from '@testing-library/react'
import { ReviewDraftForm } from './ReviewDraftForm'
import type { ReviewDraft } from './types'

const draft: ReviewDraft = { decision: 'needs_changes', notes: 'Tighten the error path' }

describe('ReviewDraftForm', () => {
  it('renders the decision and notes of a populated draft', () => {
    render(<ReviewDraftForm draft={draft} onChange={vi.fn()} />)
    expect(screen.getByLabelText('Decision')).toHaveValue('needs_changes')
    expect(screen.getByLabelText('Notes')).toHaveValue('Tighten the error path')
  })

  it('changing the decision select updates only that field', () => {
    const onChange = vi.fn()
    render(<ReviewDraftForm draft={draft} onChange={onChange} />)
    fireEvent.change(screen.getByLabelText('Decision'), { target: { value: 'approved' } })
    expect(onChange).toHaveBeenCalledWith({ ...draft, decision: 'approved' })
  })

  it('editing the notes updates only that field', () => {
    const onChange = vi.fn()
    render(<ReviewDraftForm draft={draft} onChange={onChange} />)
    fireEvent.change(screen.getByLabelText('Notes'), { target: { value: 'Ship it' } })
    expect(onChange).toHaveBeenCalledWith({ ...draft, notes: 'Ship it' })
  })

  it('offers all three verdicts', () => {
    render(<ReviewDraftForm draft={draft} onChange={vi.fn()} />)
    const options = Array.from(screen.getByLabelText('Decision').querySelectorAll('option')).map((o) => o.value)
    expect(options).toEqual(['approved', 'needs_changes', 'rejected'])
  })
})

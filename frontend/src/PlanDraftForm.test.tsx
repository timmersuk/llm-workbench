import { describe, expect, it, vi } from 'vitest'
import { fireEvent, render, screen } from '@testing-library/react'
import { PlanDraftForm } from './PlanDraftForm'
import type { TaskPlan } from './types'

const draft: TaskPlan = {
  approach: 'Incremental rollout',
  steps: ['Step one', 'Step two'],
  risks: ['API might change'],
  estimated_complexity: 'low',
  recommended_executor: 'claude-code',
}

describe('PlanDraftForm', () => {
  it('renders every field of a populated draft', () => {
    render(<PlanDraftForm draft={draft} onChange={vi.fn()} />)
    expect(screen.getByLabelText('Approach')).toHaveValue('Incremental rollout')
    expect(screen.getByLabelText('Steps (one per line)')).toHaveValue('Step one\nStep two')
    expect(screen.getByLabelText('Estimated complexity')).toHaveValue('low')
    expect(screen.getByLabelText('Recommended executor')).toHaveValue('claude-code')
  })

  it('editing the approach calls onChange with only that field changed', () => {
    const onChange = vi.fn()
    render(<PlanDraftForm draft={draft} onChange={onChange} />)
    fireEvent.change(screen.getByLabelText('Approach'), { target: { value: 'Big bang rollout' } })
    expect(onChange).toHaveBeenCalledWith({ ...draft, approach: 'Big bang rollout' })
  })

  it('editing the steps list round-trips through linesToList', () => {
    const onChange = vi.fn()
    render(<PlanDraftForm draft={draft} onChange={onChange} />)
    fireEvent.change(screen.getByLabelText('Steps (one per line)'), {
      target: { value: 'Step one\nStep two\nStep three' },
    })
    expect(onChange).toHaveBeenCalledWith({ ...draft, steps: ['Step one', 'Step two', 'Step three'] })
  })

  it('changing the complexity select updates estimated_complexity', () => {
    const onChange = vi.fn()
    render(<PlanDraftForm draft={draft} onChange={onChange} />)
    fireEvent.change(screen.getByLabelText('Estimated complexity'), { target: { value: 'high' } })
    expect(onChange).toHaveBeenCalledWith({ ...draft, estimated_complexity: 'high' })
  })
})

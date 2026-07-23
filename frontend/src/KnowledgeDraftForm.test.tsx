import { describe, expect, it, vi } from 'vitest'
import { fireEvent, render, screen } from '@testing-library/react'
import { KnowledgeDraftForm } from './KnowledgeDraftForm'
import type { KnowledgeConceptDraft } from './types'

const draft: KnowledgeConceptDraft = {
  concept_id: 'coding-standards/logging',
  type: 'Coding Standard',
  frontmatter: { title: 'Logging', description: 'How we log.', tags: ['backend', 'observability'] },
  body: 'Use structured logging.\n',
}

describe('KnowledgeDraftForm', () => {
  it('renders every field of a populated draft', () => {
    render(<KnowledgeDraftForm draft={draft} onChange={vi.fn()} />)
    expect(screen.getByLabelText('Concept ID')).toHaveValue('coding-standards/logging')
    expect(screen.getByLabelText('Type')).toHaveValue('Coding Standard')
    expect(screen.getByLabelText('Title')).toHaveValue('Logging')
    expect(screen.getByLabelText('Description')).toHaveValue('How we log.')
    expect(screen.getByLabelText('Tags (comma-separated)')).toHaveValue('backend, observability')
    expect(screen.getByLabelText('Body (markdown)')).toHaveValue('Use structured logging.\n')
  })

  it('tolerates a draft with no frontmatter at all', () => {
    render(<KnowledgeDraftForm draft={{ concept_id: 'x', type: 'Reference', body: 'y' }} onChange={vi.fn()} />)
    expect(screen.getByLabelText('Title')).toHaveValue('')
    expect(screen.getByLabelText('Tags (comma-separated)')).toHaveValue('')
  })

  it('editing concept_id updates only that field', () => {
    const onChange = vi.fn()
    render(<KnowledgeDraftForm draft={draft} onChange={onChange} />)
    fireEvent.change(screen.getByLabelText('Concept ID'), { target: { value: 'coding-standards/errors' } })
    expect(onChange).toHaveBeenCalledWith({ ...draft, concept_id: 'coding-standards/errors' })
  })

  it('editing type updates only that field', () => {
    const onChange = vi.fn()
    render(<KnowledgeDraftForm draft={draft} onChange={onChange} />)
    fireEvent.change(screen.getByLabelText('Type'), { target: { value: 'Domain Note' } })
    expect(onChange).toHaveBeenCalledWith({ ...draft, type: 'Domain Note' })
  })

  it('editing title merges into frontmatter without touching other keys', () => {
    const onChange = vi.fn()
    render(<KnowledgeDraftForm draft={draft} onChange={onChange} />)
    fireEvent.change(screen.getByLabelText('Title'), { target: { value: 'New Title' } })
    expect(onChange).toHaveBeenCalledWith({ ...draft, frontmatter: { ...draft.frontmatter, title: 'New Title' } })
  })

  it('editing description merges into frontmatter', () => {
    const onChange = vi.fn()
    render(<KnowledgeDraftForm draft={draft} onChange={onChange} />)
    fireEvent.change(screen.getByLabelText('Description'), { target: { value: 'New description.' } })
    expect(onChange).toHaveBeenCalledWith({ ...draft, frontmatter: { ...draft.frontmatter, description: 'New description.' } })
  })

  it('editing tags splits on commas and trims whitespace', () => {
    const onChange = vi.fn()
    render(<KnowledgeDraftForm draft={draft} onChange={onChange} />)
    fireEvent.change(screen.getByLabelText('Tags (comma-separated)'), { target: { value: 'a,  b ,c' } })
    expect(onChange).toHaveBeenCalledWith({ ...draft, frontmatter: { ...draft.frontmatter, tags: ['a', 'b', 'c'] } })
  })

  it('clearing tags writes an empty array, not an array with an empty string', () => {
    const onChange = vi.fn()
    render(<KnowledgeDraftForm draft={draft} onChange={onChange} />)
    fireEvent.change(screen.getByLabelText('Tags (comma-separated)'), { target: { value: '' } })
    expect(onChange).toHaveBeenCalledWith({ ...draft, frontmatter: { ...draft.frontmatter, tags: [] } })
  })

  it('editing the body updates only that field', () => {
    const onChange = vi.fn()
    render(<KnowledgeDraftForm draft={draft} onChange={onChange} />)
    fireEvent.change(screen.getByLabelText('Body (markdown)'), { target: { value: 'New body.\n' } })
    expect(onChange).toHaveBeenCalledWith({ ...draft, body: 'New body.\n' })
  })

  it('preserves unknown frontmatter keys not exposed as their own field', () => {
    const withResource: KnowledgeConceptDraft = { ...draft, frontmatter: { ...draft.frontmatter, resource: 'docs/foo.md' } }
    const onChange = vi.fn()
    render(<KnowledgeDraftForm draft={withResource} onChange={onChange} />)
    fireEvent.change(screen.getByLabelText('Title'), { target: { value: 'Renamed' } })
    expect(onChange).toHaveBeenCalledWith({ ...withResource, frontmatter: { ...withResource.frontmatter, title: 'Renamed' } })
  })
})

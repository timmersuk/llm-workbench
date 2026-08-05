import { describe, expect, it } from 'vitest'
import { render, screen } from '@testing-library/react'
import { KnowledgeActivityList } from './KnowledgeActivityList'
import type { KnowledgeActivityEntry } from './types'

describe('KnowledgeActivityList', () => {
  it('renders one row per entry, labeled by action, with the concept type shown when present', () => {
    const entries: KnowledgeActivityEntry[] = [
      { concept_id: 'process/replan-when-requirements-invalidate-spec', type: 'Operational Practice', action: 'created', created_at: '2026-01-01T00:00:00Z' },
      { concept_id: 'coding-standards/logging', type: 'Coding Standard', action: 'updated', created_at: '2026-01-02T00:00:00Z' },
      { concept_id: 'x', action: 'rejected', created_at: '2026-01-03T00:00:00Z' },
    ]

    render(<KnowledgeActivityList entries={entries} />)

    const items = document.querySelectorAll('.knowledge-activity-list > li')
    expect(items).toHaveLength(3)
    expect(items[0].textContent).toContain('knowledge concept created: process/replan-when-requirements-invalidate-spec')
    expect(items[0].textContent).toContain('Operational Practice')
    expect(items[1].textContent).toContain('knowledge concept updated: coding-standards/logging')
    expect(items[2].textContent).toContain('knowledge concept proposal rejected: x')
    // No type on the rejected entry, so no parenthetical is shown for it.
    expect(items[2].querySelector('.knowledge-activity-type')).toBeNull()
  })

  it('renders nothing (an empty list) when there are no entries', () => {
    render(<KnowledgeActivityList entries={[]} />)
    expect(screen.queryByRole('listitem')).not.toBeInTheDocument()
  })
})

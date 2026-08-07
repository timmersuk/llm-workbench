import { describe, expect, it } from 'vitest'
import { render, screen } from '@testing-library/react'
import userEvent from '@testing-library/user-event'
import { DiffView } from './DiffView'

const SMALL_PATCH = `diff --git a/feature.go b/feature.go
index 1234567..89abcde 100644
--- a/feature.go
+++ b/feature.go
@@ -1,3 +1,4 @@
 package main

+// new line
 func main() {}
`

describe('DiffView', () => {
  it('renders nothing for a null or empty patch', () => {
    const { container: nullContainer } = render(<DiffView patch={null} />)
    expect(nullContainer).toBeEmptyDOMElement()

    const { container: emptyContainer } = render(<DiffView patch="" />)
    expect(emptyContainer).toBeEmptyDOMElement()
  })

  it('defaults to unified view, broken out per changed file', () => {
    render(<DiffView patch={SMALL_PATCH} />)

    expect(screen.getByText('feature.go')).toBeInTheDocument()
    expect(screen.getByRole('button', { name: 'Unified' })).toHaveAttribute('aria-pressed', 'true')
    expect(screen.getByRole('button', { name: 'Split' })).toHaveAttribute('aria-pressed', 'false')
    expect(document.querySelector('.diff-unified')).toBeInTheDocument()
    expect(document.querySelector('.diff-split')).not.toBeInTheDocument()
  })

  it('the Split toggle switches every file to a side-by-side view', async () => {
    const user = userEvent.setup()
    render(<DiffView patch={SMALL_PATCH} />)

    await user.click(screen.getByRole('button', { name: 'Split' }))

    expect(screen.getByRole('button', { name: 'Split' })).toHaveAttribute('aria-pressed', 'true')
    expect(document.querySelector('.diff-split')).toBeInTheDocument()
    expect(document.querySelector('.diff-unified')).not.toBeInTheDocument()
  })

  it('renders every file collapsed by default', () => {
    render(<DiffView patch={SMALL_PATCH} />)

    const details = screen.getByText('feature.go').closest('details')
    expect(details).not.toHaveAttribute('open')
  })

  it('applies refractor syntax highlighting to a changed Go line', () => {
    const { container } = render(<DiffView patch={SMALL_PATCH} />)

    const keywordToken = Array.from(container.querySelectorAll('.token.keyword')).find(
      (el) => el.textContent === 'func',
    )
    expect(keywordToken).toBeDefined()
  })
})

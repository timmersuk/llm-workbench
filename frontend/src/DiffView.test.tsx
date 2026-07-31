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

// A second, unrelated file whose hunk carries 301 added lines — over the
// 300-changed-line collapse threshold.
function bigPatch(): string {
  const added = Array.from({ length: 301 }, (_, i) => `+line ${i}`).join('\n')
  return `diff --git a/big.go b/big.go
new file mode 100644
index 0000000..1111111
--- /dev/null
+++ b/big.go
@@ -0,0 +1,301 @@
${added}
`
}

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

  it('a file under the 300-changed-line threshold is expanded by default; one over it starts collapsed', () => {
    render(<DiffView patch={SMALL_PATCH + bigPatch()} />)

    const smallDetails = screen.getByText('feature.go').closest('details')
    const bigDetails = screen.getByText('big.go').closest('details')

    expect(smallDetails).toHaveAttribute('open')
    expect(bigDetails).not.toHaveAttribute('open')
  })

  it('applies refractor syntax highlighting to a changed Go line', () => {
    const { container } = render(<DiffView patch={SMALL_PATCH} />)

    const keywordToken = Array.from(container.querySelectorAll('.token.keyword')).find(
      (el) => el.textContent === 'func',
    )
    expect(keywordToken).toBeDefined()
  })
})

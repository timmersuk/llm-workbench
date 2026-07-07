import { describe, expect, it } from 'vitest'
import { render, screen } from '@testing-library/react'
import { MarkdownMessage } from './MarkdownMessage'

describe('MarkdownMessage', () => {
  it('renders bold text as <strong>', () => {
    render(<MarkdownMessage content="this is **bold** text" />)
    expect(screen.getByText('bold').tagName).toBe('STRONG')
  })

  it('renders a fenced code block with a highlight class', () => {
    render(<MarkdownMessage content={'```go\nfunc main() {}\n```'} />)
    const code = document.querySelector('pre code')
    expect(code).not.toBeNull()
    expect(code!.className).toContain('hljs')
  })

  it('renders a GFM table as a real <table>', () => {
    const table = '| a | b |\n| --- | --- |\n| 1 | 2 |'
    render(<MarkdownMessage content={table} />)
    expect(document.querySelector('table')).not.toBeNull()
    expect(screen.getByText('1')).toBeInTheDocument()
  })

  it('renders a GFM task list checkbox', () => {
    render(<MarkdownMessage content="- [ ] todo item" />)
    const checkbox = document.querySelector('input[type="checkbox"]')
    expect(checkbox).not.toBeNull()
  })

  it('does not render literal HTML in content as real DOM (no rehype-raw)', () => {
    render(<MarkdownMessage content='<img src=x onerror="window.__pwned = true">' />)
    expect(document.querySelector('img')).toBeNull()
    expect((window as unknown as { __pwned?: boolean }).__pwned).toBeUndefined()
  })
})

import { describe, expect, it } from 'vitest'
import { render, screen } from '@testing-library/react'
import { ToolActivitySequence } from './ToolActivity'
import type { ToolActivityEntry } from './ToolActivity'

function makeActivity(overrides: Partial<ToolActivityEntry> = {}): ToolActivityEntry {
  return { name: 'Read', arguments: '{"path":"a.go"}', result: 'file contents', ...overrides }
}

describe('ToolActivitySequence — argument preview encoding', () => {
  it('un-escapes Go json.Marshal\'s HTML-escaped characters in the preview', () => {
    // Go's encoding/json HTML-escapes <, >, & by default, so a real Bash
    // arguments string arrives as `2>&1` — the preview must show
    // the readable `2>&1`, not the raw escape sequences.
    const activity = makeActivity({
      name: 'Bash',
      arguments: '{"command":"npx oxlint 2\\u003e\\u00261 | tail -60"}',
    })
    render(<ToolActivitySequence activities={[activity]} live />)
    expect(screen.getByText('{"command":"npx oxlint 2>&1 | tail -60"}')).toBeInTheDocument()
    expect(screen.queryByText(/\\u00/)).not.toBeInTheDocument()
  })
})

describe('ToolActivitySequence — not live (at rest)', () => {
  it('collapses to a plain count summary with no error flag', () => {
    render(<ToolActivitySequence activities={[makeActivity(), makeActivity({ name: 'Bash' })]} live={false} />)
    expect(screen.getByText('Used 2 tools')).toBeInTheDocument()
    expect(document.querySelector('.tool-status-error')).toBeNull()
  })

  it('flags the summary when any call in the group failed', () => {
    render(
      <ToolActivitySequence
        activities={[makeActivity(), makeActivity({ name: 'Bash', isError: true, result: 'exit 1' })]}
        live={false}
      />,
    )
    const summary = screen.getByText(/Used 2 tools/)
    expect(summary.querySelector('.tool-status-error')).not.toBeNull()
  })

  it('renders one row per call with its name and full result available', () => {
    render(<ToolActivitySequence activities={[makeActivity()]} live={false} />)
    expect(screen.getByText('Read')).toBeInTheDocument()
    expect(screen.getByText('file contents')).toBeInTheDocument()
  })

  it('renders nothing for an empty sequence', () => {
    const { container } = render(<ToolActivitySequence activities={[]} live={false} />)
    expect(container).toBeEmptyDOMElement()
  })
})

describe('ToolActivitySequence — live', () => {
  it('renders every call individually when at or under the live-visible threshold', () => {
    const activities = [makeActivity({ name: 'Read' }), makeActivity({ name: 'Grep' })]
    render(<ToolActivitySequence activities={activities} live />)
    expect(screen.getByText('Read')).toBeInTheDocument()
    expect(screen.getByText('Grep')).toBeInTheDocument()
    expect(screen.queryByText(/earlier tool call/)).not.toBeInTheDocument()
  })

  it('collapses older calls into an "earlier" summary once past the threshold, keeping the tail individually visible', () => {
    const activities = [
      makeActivity({ name: 'Read' }),
      makeActivity({ name: 'Grep' }),
      makeActivity({ name: 'Glob' }),
      makeActivity({ name: 'Bash' }),
      makeActivity({ name: 'Write', result: undefined }), // still in flight
    ]
    render(<ToolActivitySequence activities={activities} live />)

    // 5 calls, threshold 3: first 2 collapse, last 3 stay individually visible.
    expect(screen.getByText('2 earlier tool calls')).toBeInTheDocument()
    expect(screen.getByText('Glob')).toBeInTheDocument()
    expect(screen.getByText('Bash')).toBeInTheDocument()
    expect(screen.getByText('Write')).toBeInTheDocument()
    expect(screen.getByText('running…')).toBeInTheDocument()
  })

  it('flags the "earlier" summary when a folded-away call failed', () => {
    const activities = [
      makeActivity({ name: 'Read', isError: true, result: 'not found' }),
      makeActivity({ name: 'Grep' }),
      makeActivity({ name: 'Glob' }),
      makeActivity({ name: 'Bash' }),
    ]
    render(<ToolActivitySequence activities={activities} live />)
    const earlier = screen.getByText(/earlier tool call/)
    expect(earlier.querySelector('.tool-status-error')).not.toBeNull()
  })
})

import { describe, expect, it } from 'vitest'
import { appendTextBlock, appendToolCallBlock, appendToolResultBlock } from './toolActivityBlocks'
import type { ToolActivityBlock } from './toolActivityBlocks'

describe('appendToolResultBlock', () => {
  it('attaches a result to the call with the matching id, not the trailing entry, when results arrive out of order', () => {
    let blocks: ToolActivityBlock[] = []
    blocks = appendToolCallBlock(blocks, { id: 'call-A', name: 'Read', arguments: '{"path":"a.go"}' })
    blocks = appendToolCallBlock(blocks, { id: 'call-B', name: 'Grep', arguments: '{"pattern":"TODO"}' })
    // Result for the SECOND call arrives first — the exact shape a batching
    // provider produces.
    blocks = appendToolResultBlock(blocks, 'call-B', 'no matches', false)
    blocks = appendToolResultBlock(blocks, 'call-A', 'package main', false)

    expect(blocks).toHaveLength(1)
    const tools = blocks[0]
    if (tools.kind !== 'tools') {
      throw new Error('expected a tools block')
    }
    expect(tools.activities).toEqual([
      { id: 'call-A', name: 'Read', arguments: '{"path":"a.go"}', result: 'package main', isError: false },
      { id: 'call-B', name: 'Grep', arguments: '{"pattern":"TODO"}', result: 'no matches', isError: false },
    ])
  })

  it('searches every tools block, not just the trailing one, when narration separates two sequences', () => {
    let blocks: ToolActivityBlock[] = []
    blocks = appendToolCallBlock(blocks, { id: 'call-A', name: 'Bash', arguments: '{"command":"go build"}' })
    blocks = appendTextBlock(blocks, 'build passes, now testing')
    blocks = appendToolCallBlock(blocks, { id: 'call-B', name: 'Bash', arguments: '{"command":"go test"}' })

    // The result for the FIRST (now non-trailing) sequence arrives after
    // the second sequence has already opened.
    blocks = appendToolResultBlock(blocks, 'call-A', 'ok', false)
    blocks = appendToolResultBlock(blocks, 'call-B', 'ok', false)

    expect(blocks).toHaveLength(3)
    expect(blocks[0]).toEqual({ kind: 'tools', activities: [{ id: 'call-A', name: 'Bash', arguments: '{"command":"go build"}', result: 'ok', isError: false }] })
    expect(blocks[1]).toEqual({ kind: 'text', text: 'build passes, now testing' })
    expect(blocks[2]).toEqual({ kind: 'tools', activities: [{ id: 'call-B', name: 'Bash', arguments: '{"command":"go test"}', result: 'ok', isError: false }] })
  })

  it('falls back to the trailing pending activity when id is falsy (legacy, pre-id data)', () => {
    let blocks: ToolActivityBlock[] = []
    blocks = appendToolCallBlock(blocks, { name: 'Read', arguments: '{"path":"a.go"}' })
    blocks = appendToolResultBlock(blocks, '', 'package main', false)

    expect(blocks).toEqual([{ kind: 'tools', activities: [{ name: 'Read', arguments: '{"path":"a.go"}', result: 'package main', isError: false }] }])
  })

  it('is a no-op when nothing matches (unknown id, non-fallback case)', () => {
    let blocks: ToolActivityBlock[] = []
    blocks = appendToolCallBlock(blocks, { id: 'call-A', name: 'Read', arguments: '{}' })
    const before = blocks
    blocks = appendToolResultBlock(blocks, 'call-unknown', 'ignored', false)

    expect(blocks).toEqual(before)
  })
})

describe('appendTextBlock / appendToolCallBlock', () => {
  it('coalesces consecutive text deltas into one growing block', () => {
    let blocks: ToolActivityBlock[] = []
    blocks = appendTextBlock(blocks, 'Hel')
    blocks = appendTextBlock(blocks, 'lo')
    expect(blocks).toEqual([{ kind: 'text', text: 'Hello' }])
  })

  it('coalesces consecutive tool calls into one tools block', () => {
    let blocks: ToolActivityBlock[] = []
    blocks = appendToolCallBlock(blocks, { id: 'call-A', name: 'Read', arguments: '{}' })
    blocks = appendToolCallBlock(blocks, { id: 'call-B', name: 'Grep', arguments: '{}' })
    expect(blocks).toHaveLength(1)
    expect(blocks[0].kind).toBe('tools')
  })

  it('starts a new block when narration interrupts a run of tool calls', () => {
    let blocks: ToolActivityBlock[] = []
    blocks = appendToolCallBlock(blocks, { id: 'call-A', name: 'Read', arguments: '{}' })
    blocks = appendTextBlock(blocks, 'narrating')
    blocks = appendToolCallBlock(blocks, { id: 'call-B', name: 'Grep', arguments: '{}' })
    expect(blocks.map((b) => b.kind)).toEqual(['tools', 'text', 'tools'])
  })
})

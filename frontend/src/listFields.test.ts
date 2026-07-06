import { describe, expect, it } from 'vitest'
import { linesToList, listToLines } from './listFields'

describe('linesToList', () => {
  it('splits on newlines, trims, and drops blank lines', () => {
    expect(linesToList('a\n  b  \n\nc\n')).toEqual(['a', 'b', 'c'])
  })

  it('returns an empty array for blank input', () => {
    expect(linesToList('')).toEqual([])
    expect(linesToList('\n\n   \n')).toEqual([])
  })
})

describe('listToLines', () => {
  it('joins items with newlines', () => {
    expect(listToLines(['a', 'b', 'c'])).toBe('a\nb\nc')
  })

  it('returns an empty string for an empty array', () => {
    expect(listToLines([])).toBe('')
  })
})

import { beforeEach, describe, expect, it } from 'vitest'
import { applyTheme, getStoredTheme, setTheme } from './theme'

beforeEach(() => {
  window.localStorage.clear()
  delete document.documentElement.dataset.theme
})

describe('getStoredTheme', () => {
  it('defaults to system when nothing is stored', () => {
    expect(getStoredTheme()).toBe('system')
  })

  it('returns a validly stored theme', () => {
    window.localStorage.setItem('theme', 'dark')
    expect(getStoredTheme()).toBe('dark')
  })

  it('falls back to system for a garbage stored value', () => {
    window.localStorage.setItem('theme', 'nonsense')
    expect(getStoredTheme()).toBe('system')
  })
})

describe('applyTheme', () => {
  it('sets data-theme for light', () => {
    applyTheme('light')
    expect(document.documentElement.dataset.theme).toBe('light')
  })

  it('sets data-theme for dark', () => {
    applyTheme('dark')
    expect(document.documentElement.dataset.theme).toBe('dark')
  })

  it('removes data-theme for system, leaving the prefers-color-scheme fallback in control', () => {
    applyTheme('dark')
    applyTheme('system')
    expect(document.documentElement.dataset.theme).toBeUndefined()
  })
})

describe('setTheme', () => {
  it('persists the choice to localStorage and applies it', () => {
    setTheme('dark')
    expect(window.localStorage.getItem('theme')).toBe('dark')
    expect(document.documentElement.dataset.theme).toBe('dark')
  })

  it('persists system and clears any prior override', () => {
    setTheme('light')
    setTheme('system')
    expect(window.localStorage.getItem('theme')).toBe('system')
    expect(document.documentElement.dataset.theme).toBeUndefined()
  })
})

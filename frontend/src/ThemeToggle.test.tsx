import { beforeEach, describe, expect, it } from 'vitest'
import { render, screen, fireEvent } from '@testing-library/react'
import { ThemeToggle } from './ThemeToggle'

beforeEach(() => {
  window.localStorage.clear()
  delete document.documentElement.dataset.theme
})

describe('ThemeToggle', () => {
  it('defaults to the System button active when nothing is stored', () => {
    render(<ThemeToggle />)
    expect(screen.getByRole('button', { name: 'System' })).toHaveClass('active')
    expect(screen.getByRole('button', { name: 'Light' })).not.toHaveClass('active')
    expect(screen.getByRole('button', { name: 'Dark' })).not.toHaveClass('active')
  })

  it('seeds active state from a previously stored theme', () => {
    window.localStorage.setItem('theme', 'dark')
    render(<ThemeToggle />)
    expect(screen.getByRole('button', { name: 'Dark' })).toHaveClass('active')
  })

  it('clicking Dark updates active state, localStorage, and the document attribute', () => {
    render(<ThemeToggle />)
    fireEvent.click(screen.getByRole('button', { name: 'Dark' }))
    expect(screen.getByRole('button', { name: 'Dark' })).toHaveClass('active')
    expect(screen.getByRole('button', { name: 'System' })).not.toHaveClass('active')
    expect(window.localStorage.getItem('theme')).toBe('dark')
    expect(document.documentElement.dataset.theme).toBe('dark')
  })

  it('clicking Light then System clears the override', () => {
    render(<ThemeToggle />)
    fireEvent.click(screen.getByRole('button', { name: 'Light' }))
    expect(document.documentElement.dataset.theme).toBe('light')
    fireEvent.click(screen.getByRole('button', { name: 'System' }))
    expect(screen.getByRole('button', { name: 'System' })).toHaveClass('active')
    expect(window.localStorage.getItem('theme')).toBe('system')
    expect(document.documentElement.dataset.theme).toBeUndefined()
  })

  it('every option exposes an accessible name', () => {
    render(<ThemeToggle />)
    expect(screen.getByRole('button', { name: 'Light' })).toBeInTheDocument()
    expect(screen.getByRole('button', { name: 'Dark' })).toBeInTheDocument()
    expect(screen.getByRole('button', { name: 'System' })).toBeInTheDocument()
  })
})

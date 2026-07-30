import { useState } from 'react'
import type { ReactElement } from 'react'
import { getStoredTheme, setTheme } from './theme'
import type { Theme } from './theme'

// Plain inline SVG rather than a new icon-library dependency (docs/engineering
// conventions.md: no new runtime dependency without a stated reason; same
// pattern as ActionIcons.tsx) — three fixed glyphs don't need one. All use
// `currentColor` so they pick up the shared nav-button/`.active` color rules
// in index.css with no icon-specific CSS.
function iconProps() {
  return {
    width: 16,
    height: 16,
    viewBox: '0 0 24 24',
    fill: 'none',
    stroke: 'currentColor',
    strokeWidth: 2,
    strokeLinecap: 'round' as const,
    strokeLinejoin: 'round' as const,
    'aria-hidden': true,
  }
}

function SunIcon() {
  return (
    <svg {...iconProps()}>
      <circle cx="12" cy="12" r="4" />
      <path d="M12 2v2" />
      <path d="M12 20v2" />
      <path d="M4.93 4.93l1.41 1.41" />
      <path d="M17.66 17.66l1.41 1.41" />
      <path d="M2 12h2" />
      <path d="M20 12h2" />
      <path d="M4.93 19.07l1.41-1.41" />
      <path d="M17.66 6.34l1.41-1.41" />
    </svg>
  )
}

function MoonIcon() {
  return (
    <svg {...iconProps()}>
      <path d="M21 12.79A9 9 0 1 1 11.21 3 7 7 0 0 0 21 12.79Z" />
    </svg>
  )
}

function MonitorIcon() {
  return (
    <svg {...iconProps()}>
      <rect x="2" y="4" width="20" height="13" rx="2" />
      <path d="M8 21h8" />
      <path d="M12 17v4" />
    </svg>
  )
}

const OPTIONS: { id: Theme; label: string; Icon: () => ReactElement }[] = [
  { id: 'light', label: 'Light', Icon: SunIcon },
  { id: 'dark', label: 'Dark', Icon: MoonIcon },
  { id: 'system', label: 'System', Icon: MonitorIcon },
]

// ThemeToggle: a row of nav-tab-like icon buttons next to the build badge
// (App.tsx's .header-controls) for overriding the OS-driven
// prefers-color-scheme dark palette (index.css). Selection is persisted via
// theme.ts's localStorage-backed setTheme/getStoredTheme.
export function ThemeToggle() {
  const [theme, setThemeState] = useState<Theme>(() => getStoredTheme())

  function handleSelect(next: Theme) {
    setTheme(next)
    setThemeState(next)
  }

  return (
    <div className="theme-toggle" role="group" aria-label="Theme">
      {OPTIONS.map(({ id, label, Icon }) => (
        <button
          key={id}
          type="button"
          className={theme === id ? 'active' : ''}
          aria-label={label}
          title={label}
          onClick={() => handleSelect(id)}
        >
          <Icon />
        </button>
      ))}
    </div>
  )
}

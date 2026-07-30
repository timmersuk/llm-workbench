// Manual Light/Dark/System theme override, layered on top of index.css's
// existing prefers-color-scheme dark palette via a `data-theme` attribute
// on <html> (see index.css's `:root[data-theme='dark']`/`='light'` blocks).
// 'system' removes the attribute entirely, so the prefers-color-scheme
// media query is the only thing that applies — today's automatic behavior,
// unchanged.
export type Theme = 'light' | 'dark' | 'system'

const STORAGE_KEY = 'theme'

function isTheme(value: string | null): value is Theme {
  return value === 'light' || value === 'dark' || value === 'system'
}

export function getStoredTheme(): Theme {
  let stored: string | null = null
  try {
    stored = window.localStorage.getItem(STORAGE_KEY)
  } catch {
    // localStorage can throw (e.g. disabled/private browsing) — fall back
    // to the default below rather than letting theme resolution fail.
  }
  return isTheme(stored) ? stored : 'system'
}

export function applyTheme(theme: Theme): void {
  if (theme === 'system') {
    delete document.documentElement.dataset.theme
  } else {
    document.documentElement.dataset.theme = theme
  }
}

export function setTheme(theme: Theme): void {
  try {
    window.localStorage.setItem(STORAGE_KEY, theme)
  } catch {
    // Best-effort persistence — an in-session override still applies via
    // applyTheme even if it can't be saved.
  }
  applyTheme(theme)
}

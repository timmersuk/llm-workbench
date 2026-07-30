import { StrictMode } from 'react'
import { createRoot } from 'react-dom/client'
import './index.css'
import App from './App.tsx'
import { applyTheme, getStoredTheme } from './theme.ts'

// Applied synchronously, before the first render, so a stored light/dark
// override takes effect immediately — no flash of the OS-default theme.
applyTheme(getStoredTheme())

createRoot(document.getElementById('root')!).render(
  <StrictMode>
    <App />
  </StrictMode>,
)

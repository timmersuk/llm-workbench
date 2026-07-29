// ActionIcons: small inline outline icons for the per-message action row
// (StageConversationPanel.tsx) — Copy/Edit/Regenerate/Delete used to be
// text-labeled buttons, which read as a wall of words next to every
// message; icon-only buttons (title attribute carries the accessible name,
// matching how Claude Desktop's own message actions are styled) read as a
// single compact row instead. Plain inline SVG rather than a new icon-
// library dependency (docs/engineering conventions.md: no new runtime
// dependency without a stated reason) — four fixed 14x14 outline glyphs
// don't need one. All use `currentColor` so they inherit .action-btn's
// existing color/hover styling with no icon-specific CSS.
function iconProps(size = 14) {
  return {
    width: size,
    height: size,
    viewBox: '0 0 24 24',
    fill: 'none',
    stroke: 'currentColor',
    strokeWidth: 2,
    strokeLinecap: 'round' as const,
    strokeLinejoin: 'round' as const,
    'aria-hidden': true,
  }
}

export function CopyIcon() {
  return (
    <svg {...iconProps()}>
      <rect x="9" y="9" width="12" height="12" rx="2" />
      <path d="M5 15H4a2 2 0 0 1-2-2V4a2 2 0 0 1 2-2h9a2 2 0 0 1 2 2v1" />
    </svg>
  )
}

export function EditIcon() {
  return (
    <svg {...iconProps()}>
      <path d="M17 3a2.83 2.83 0 1 1 4 4L7.5 20.5 2 22l1.5-5.5Z" />
    </svg>
  )
}

export function RegenerateIcon() {
  return (
    <svg {...iconProps()}>
      <path d="M21 12a9 9 0 0 1-15.3 6.4L3 16" />
      <path d="M3 12a9 9 0 0 1 15.3-6.4L21 8" />
      <path d="M3 21v-5h5" />
      <path d="M21 3v5h-5" />
    </svg>
  )
}

export function DeleteIcon() {
  return (
    <svg {...iconProps()}>
      <path d="M3 6h18" />
      <path d="M8 6V4a1 1 0 0 1 1-1h6a1 1 0 0 1 1 1v2" />
      <path d="M19 6l-1 14a1 1 0 0 1-1 1H7a1 1 0 0 1-1-1L5 6" />
      <path d="M10 11v6" />
      <path d="M14 11v6" />
    </svg>
  )
}

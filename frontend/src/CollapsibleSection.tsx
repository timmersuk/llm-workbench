import type { ReactNode } from 'react'

interface CollapsibleSectionProps {
  title: string
  defaultOpen: boolean
  // current marks the one section actually relevant to task.stage right
  // now — a distinct background/border plus a "Current" badge, so it reads
  // as "this is what matters here" at a glance, not just "this happens to
  // be expanded."
  current?: boolean
  children: ReactNode
}

// CollapsibleSection is a labeled, collapsible <details> block for the task
// page's reference material (Requirements/Context/Plan, Timeline, Execution
// history) — collapsed by default so a task with several stages' worth of
// history doesn't bury the active stage panel below a wall of read-only
// content; defaultOpen only backs the initial `open` attribute (native
// <details> is uncontrolled), so a human who opens/closes one manually
// isn't fighting a re-render that would reset it.
export function CollapsibleSection({ title, defaultOpen, current = false, children }: CollapsibleSectionProps) {
  return (
    <details className={current ? 'task-section task-section-current' : 'task-section'} open={defaultOpen}>
      <summary>
        {title}
        {current && <span className="task-section-current-badge">Current</span>}
      </summary>
      <div className="task-section-body">{children}</div>
    </details>
  )
}

import { useState } from 'react'
import { runTaskCleanup } from './api'
import type { Task } from './types'

interface CleanupPanelProps {
  projectId: string
  taskId: string
  task: Task
  onUpdated: (task: Task) => void
}

// CleanupPanel is the cleanup-stage screen: shown only when the automatic
// worktree-removal pass "Mark as merged" ran inline
// (internal/api/pr.go's handleMarkPRMerged) left at least one of a task's
// execution worktrees skipped or failed — a task that came back all-clean
// skips this screen entirely, landing straight on stage 'merged'. Renders
// the persisted per-worktree report (task.cleanup_status) and offers Retry
// (re-run the same safety-checked routine — useful once a flagged worktree
// has been committed/pushed by hand) and Force-remove (override the dirty/
// unpushed safety checks) actions, both backed by the same POST
// .../cleanup endpoint (runTaskCleanup).
export function CleanupPanel({ projectId, taskId, task, onUpdated }: CleanupPanelProps) {
  const [running, setRunning] = useState(false)
  const [error, setError] = useState<string | null>(null)

  const status = task.cleanup_status ?? []
  const problems = status.filter((s) => s.outcome === 'skipped' || s.outcome === 'failed')

  async function run(force: boolean) {
    if (running) {
      return
    }
    setRunning(true)
    setError(null)
    try {
      onUpdated(await runTaskCleanup(projectId, taskId, force))
    } catch (err) {
      setError(err instanceof Error ? err.message : String(err))
    } finally {
      setRunning(false)
    }
  }

  return (
    <div className="cleanup-panel">
      <h4>Cleaning up execution worktrees</h4>
      <p>
        The pull request was merged. This task is removing its execution worktrees before it can finish — a worktree with
        uncommitted changes, or commits not reachable from the pushed branch, is left in place rather than force-discarded.
      </p>

      {status.length > 0 ? (
        <ul className="cleanup-status-list">
          {status.map((s) => (
            <li key={s.execution_id} className={`cleanup-outcome-${s.outcome}`}>
              <strong>{s.execution_id}</strong> &middot; {s.outcome}
              {s.reason && <span className="cleanup-reason"> — {s.reason}</span>}
            </li>
          ))}
        </ul>
      ) : (
        <p>No cleanup report yet.</p>
      )}

      <div className="cleanup-actions">
        <button type="button" disabled={running} onClick={() => run(false)}>
          {running ? 'Retrying…' : 'Retry'}
        </button>
        <button type="button" disabled={running || problems.length === 0} onClick={() => run(true)}>
          {running ? 'Forcing…' : 'Force-remove'}
        </button>
      </div>
      {error && <p className="error">{error}</p>}
    </div>
  )
}

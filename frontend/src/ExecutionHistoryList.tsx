import { useState } from 'react'
import { getExecutionLog } from './api'
import { MarkdownMessage } from './MarkdownMessage'
import { ToolActivitySequence } from './ToolActivity'
import { appendTextBlock, appendToolCallBlock, appendToolResultBlock } from './toolActivityBlocks'
import type { ToolActivityBlock } from './toolActivityBlocks'
import type { Execution, ExecutionLogEvent } from './types'

// PastExecutionLogState tracks one past execution's on-demand-fetched log
// (getExecutionLog), keyed by execution_id — absent until a human expands
// that attempt's <details>, since a long unattended run's log can be large
// and most past attempts are never inspected.
type PastExecutionLogState =
  | { status: 'loading' }
  | { status: 'loaded'; trace: ToolActivityBlock[]; backfilled: boolean }
  | { status: 'error'; message: string }

// logEventsToTraceBlocks folds a fetched ExecutionLog's flat event stream
// into ToolActivityBlock[] through the same shared builders the live
// ExecutePanel/StageConversationPanel streams use, so a persisted log
// replays with the same real order (and the same id-matched result
// pairing) a live run rendered with — a persisted log's events aren't
// pre-paired server-side, they're flat and in arrival order, same shape as
// the live stream, so this needed the same fix.
function logEventsToTraceBlocks(events: ExecutionLogEvent[]): ToolActivityBlock[] {
  let blocks: ToolActivityBlock[] = []
  for (const ev of events) {
    if (ev.kind === 'text') {
      blocks = appendTextBlock(blocks, ev.text ?? '')
    } else if (ev.kind === 'tool_call') {
      blocks = appendToolCallBlock(blocks, { id: ev.id, name: ev.tool_name ?? '', arguments: ev.tool_input })
    } else if (ev.kind === 'tool_result') {
      blocks = appendToolResultBlock(blocks, ev.id ?? '', ev.tool_result ?? '', ev.is_error)
    }
  }
  return blocks
}

interface ExecutionHistoryListProps {
  projectId: string
  taskId: string
  executions: Execution[]
}

// ExecutionHistoryList renders every recorded execution attempt for a task
// as an expandable list — each item's summary line is the same
// status/branch/commits/failure line ExecutePanel always showed; expanding
// it now lazily fetches and shows that attempt's full recorded event log
// (handleGetExecutionLog), including a notice when the log was reconstructed
// from an archived session transcript rather than captured live
// (task.ExecutionLog.Backfilled). Shared by ExecutePanel (which also needs
// to append a just-finished run's own Execution live, so it fetches the
// list itself and passes it in) and TaskDetailPanel (which shows this
// regardless of the task's current stage — a human most often wants to
// check what a past run did after the task has already moved on to review
// or merged, not only while still sitting in Implementation).
export function ExecutionHistoryList({ projectId, taskId, executions }: ExecutionHistoryListProps) {
  const [logs, setLogs] = useState<Record<string, PastExecutionLogState>>({})

  // handleToggle fetches executionId's log the first time its <details> is
  // expanded, and does nothing on every subsequent toggle (open or closed)
  // — logs is read from this render's closure, so a second expand after the
  // first fetch resolved sees the cached 'loaded' entry and skips
  // re-fetching.
  function handleToggle(executionId: string, open: boolean) {
    if (!open || logs[executionId]) {
      return
    }
    setLogs((prev) => ({ ...prev, [executionId]: { status: 'loading' } }))
    getExecutionLog(projectId, taskId, executionId)
      .then((log) => {
        setLogs((prev) => ({
          ...prev,
          [executionId]: { status: 'loaded', trace: logEventsToTraceBlocks(log.events), backfilled: log.backfilled ?? false },
        }))
      })
      .catch((err) => {
        setLogs((prev) => ({
          ...prev,
          [executionId]: { status: 'error', message: err instanceof Error ? err.message : String(err) },
        }))
      })
  }

  if (executions.length === 0) {
    return null
  }

  return (
    <ul className="execution-history">
      {executions.map((e) => {
        const logState = logs[e.execution_id]
        return (
          <li key={e.execution_id} className={`execution-status execution-status-${e.status}`}>
            <details onToggle={(ev) => handleToggle(e.execution_id, ev.currentTarget.open)}>
              <summary>
                {e.execution_id}: {e.status}
                {e.output.git_branch && <> &middot; {e.output.git_branch}</>}
                {(e.output.commits?.length ?? 0) > 0 && <> &middot; {e.output.commits.length} commit(s)</>}
                {e.failure && <> &middot; {e.failure.message}</>}
                {e.output.workspace_dirty && <> &middot; workspace still has uncommitted changes</>}
              </summary>
              {logState?.status === 'loading' && <p className="execution-log-status">Loading log…</p>}
              {logState?.status === 'error' && <p className="error">Could not load log: {logState.message}</p>}
              {logState?.status === 'loaded' && (
                <div className="execution-log">
                  {logState.backfilled && (
                    <p className="execution-log-status">
                      Reconstructed from an archived session transcript — this execution ran before live log capture
                      existed.
                    </p>
                  )}
                  {logState.trace.length === 0 && <p className="execution-log-status">No events recorded.</p>}
                  {logState.trace.map((block, index) => (
                    <div key={index} className="chat-message">
                      {block.kind === 'text' && <MarkdownMessage content={block.text} />}
                      {block.kind === 'tools' && <ToolActivitySequence activities={block.activities} live={false} />}
                    </div>
                  ))}
                </div>
              )}
            </details>
          </li>
        )
      })}
    </ul>
  )
}

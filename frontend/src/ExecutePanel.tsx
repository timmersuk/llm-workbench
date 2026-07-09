import { useEffect, useRef, useState } from 'react'
import { isAbortError, listAgentExecutors, listExecutions, startExecution } from './api'
import { MarkdownMessage } from './MarkdownMessage'
import type { Execution, ExecuteStreamEvent } from './types'

// TraceEntry is one rendered piece of a running execution's live activity —
// unlike StageConversationPanel's DisplayMessage (one tool call per turn,
// rendered as a static chip), an execution can produce many tool calls and
// results per run, so this is an ordered list, not a single-message field.
interface TraceEntry {
  kind: 'text' | 'tool_call' | 'tool_result'
  content: string
  toolName?: string
  toolInput?: string
  isError?: boolean
}

interface ExecutePanelProps {
  projectId: string
  taskId: string
  // onExecuted fires once a run completes (success or failure) with its
  // recorded Execution — TaskDetailPanel uses this to reload the task,
  // since a successful run auto-advances stage to "review" server-side.
  onExecuted: (execution: Execution) => void
}

// executorLabels maps an agent executor key to its display label — "local"
// is never offered here since ChatClientRunner.Execute returns "not
// supported" until chatclient-tool-loop lands (same exclusion
// StageConversationPanel applies for stage conversations).
const executorLabels: Record<string, string> = { 'claude-code': 'Claude Code', codex: 'Codex CLI' }

// ExecutePanel is the Implementation stage's autonomous run: a "Run
// Execution" action that streams live tool activity (files written,
// commands run) rather than a back-and-forth conversation — deliberately
// not a StageConversationPanel instantiation, since there's no Draft, no
// human turns, and tool results need their own rendering
// (StageConversationPanel only ever shows one tool-call chip per turn).
export function ExecutePanel({ projectId, taskId, onExecuted }: ExecutePanelProps) {
  const [pastExecutions, setPastExecutions] = useState<Execution[]>([])
  const [trace, setTrace] = useState<TraceEntry[]>([])
  const [running, setRunning] = useState(false)
  const [runError, setRunError] = useState<string | null>(null)
  const [executor, setExecutor] = useState('')
  const [executorOptions, setExecutorOptions] = useState<string[]>([])
  // abortControllerRef tracks the in-flight run's controller so Stop can
  // cancel it — same pattern as StageConversationPanel's.
  const abortControllerRef = useRef<AbortController | null>(null)

  useEffect(() => {
    let cancelled = false
    listExecutions(projectId, taskId)
      .then((result) => {
        if (!cancelled) {
          setPastExecutions(result.executions ?? [])
        }
      })
      .catch(() => undefined) // no prior attempts, or the list failed to load — either way, nothing to show yet

    // "local" is excluded — ChatClientRunner.Execute has no real
    // implementation yet (see chatclient-tool-loop), so offering it here
    // would just 400 on Run.
    listAgentExecutors()
      .then((result) => {
        if (cancelled) {
          return
        }
        const executors = result.executors.filter((key) => key !== 'local')
        setExecutorOptions(executors)
        setExecutor((current) => current || executors[0] || '')
      })
      .catch(() => undefined) // no executors healthy — Run Execution stays disabled below

    return () => {
      cancelled = true
    }
  }, [projectId, taskId])

  function appendTrace(entry: TraceEntry) {
    setTrace((prev) => [...prev, entry])
  }

  // appendText coalesces consecutive text deltas into a single growing
  // trace entry rather than one entry per delta, matching how
  // StageConversationPanel grows one message's content in place.
  function appendText(chunk: string) {
    setTrace((prev) => {
      const last = prev[prev.length - 1]
      if (last && last.kind === 'text') {
        return [...prev.slice(0, -1), { ...last, content: last.content + chunk }]
      }
      return [...prev, { kind: 'text', content: chunk }]
    })
  }

  function handleStreamEvent(event: ExecuteStreamEvent) {
    switch (event.type) {
      case 'text':
        if (event.content) {
          appendText(event.content)
        }
        return
      case 'tool_call':
        appendTrace({ kind: 'tool_call', content: '', toolName: event.tool_name, toolInput: event.tool_input })
        return
      case 'tool_result':
        appendTrace({ kind: 'tool_result', content: event.tool_result ?? '', isError: event.is_error })
        return
      case 'error':
        // A user-initiated Stop cancels the request context, which the
        // backend surfaces as a normal SSE error event rather than a
        // thrown exception — same handling StageConversationPanel needs
        // for the identical reason.
        if (abortControllerRef.current?.signal.aborted) {
          return
        }
        setRunError(event.error ?? 'execution failed')
        return
      case 'done':
        if (event.execution) {
          setPastExecutions((prev) => [...prev, event.execution!])
          onExecuted(event.execution)
        }
    }
  }

  async function handleRun() {
    if (running || !executor) {
      return
    }
    setTrace([])
    setRunError(null)
    setRunning(true)
    const controller = new AbortController()
    abortControllerRef.current = controller

    try {
      await startExecution(projectId, taskId, executor, handleStreamEvent, controller.signal)
    } catch (err) {
      if (!isAbortError(err)) {
        setRunError(err instanceof Error ? err.message : String(err))
      }
    } finally {
      abortControllerRef.current = null
      setRunning(false)
    }
  }

  // handleStop aborts the in-flight run — Go's net/http cancels the
  // handler's request context when the client disconnects, so this
  // actually interrupts the backend agent, not just the frontend's
  // rendering of it (same as StageConversationPanel's Stop).
  function handleStop() {
    abortControllerRef.current?.abort()
  }

  return (
    <div className="stage-conversation">
      <div className="stage-conversation-header">
        <h4>Execute</h4>
        <p className="stage-conversation-intro">
          Runs an agent autonomously, on an isolated git branch, to implement the approved plan — no approval
          checkpoints mid-run; review the result afterward.
        </p>
      </div>

      <div className="chat-model-row">
        <label htmlFor="execute-executor">Executor</label>
        <select
          id="execute-executor"
          value={executor}
          onChange={(e) => setExecutor(e.target.value)}
          disabled={running || executorOptions.length === 0}
        >
          {executorOptions.length === 0 && <option value="">No executor available</option>}
          {executorOptions.map((key) => (
            <option key={key} value={key}>
              {executorLabels[key] ?? key}
            </option>
          ))}
        </select>
      </div>

      {pastExecutions.length > 0 && (
        <ul className="execution-history">
          {pastExecutions.map((e) => (
            <li key={e.execution_id} className={`execution-status execution-status-${e.status}`}>
              {e.execution_id}: {e.status}
              {e.output.git_branch && <> &middot; {e.output.git_branch}</>}
              {e.output.commits.length > 0 && <> &middot; {e.output.commits.length} commit(s)</>}
              {e.failure && <> &middot; {e.failure.message}</>}
            </li>
          ))}
        </ul>
      )}

      {trace.length > 0 && (
        <div className="chat-history">
          {trace.map((entry, index) => (
            <div key={index} className="chat-message">
              {entry.kind === 'text' && <MarkdownMessage content={entry.content} />}
              {entry.kind === 'tool_call' && (
                <details className="thinking-panel">
                  <summary>
                    <span className="tool-call-chip">{entry.toolName}</span>
                  </summary>
                  <div className="thinking-content">{entry.toolInput}</div>
                </details>
              )}
              {entry.kind === 'tool_result' && (
                <details className="thinking-panel">
                  <summary>{entry.isError ? 'Tool error' : 'Tool result'}</summary>
                  <div className={entry.isError ? 'thinking-content error' : 'thinking-content'}>{entry.content}</div>
                </details>
              )}
            </div>
          ))}
        </div>
      )}

      {runError && <p className="error">{runError}</p>}

      <div className="chat-input">
        {running ? (
          <button type="button" className="action-btn-stop" onClick={handleStop}>
            Stop
          </button>
        ) : (
          <button type="button" onClick={handleRun} disabled={!executor}>
            Run Execution
          </button>
        )}
      </div>
    </div>
  )
}

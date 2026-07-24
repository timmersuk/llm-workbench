import { useEffect, useRef, useState } from 'react'
import { getContinuableExecution, isAbortError, listAgentExecutors, listExecutions, startExecution } from './api'
import { MarkdownMessage } from './MarkdownMessage'
import type { ToolActivityEntry } from './ToolActivity'
import { ToolActivitySequence } from './ToolActivity'
import type { Execution, ExecuteStreamEvent } from './types'
import { useStickyAutoScroll } from './useStickyAutoScroll'

// TraceBlock is one rendered piece of a running execution's live activity —
// unlike StageConversationPanel's DisplayMessage (one tool-activity sequence
// per turn, bundled on a single message), an execution's trace is flat and
// can interleave narration between tool calls, so a 'tools' block holds one
// sequence (docs/adr/0019: a maximal run of calls uninterrupted by text) and
// any interleaved text starts a new 'text' block, ending that sequence.
type TraceBlock = { kind: 'text'; content: string } | { kind: 'tools'; activities: ToolActivityEntry[] }

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
// not a StageConversationPanel instantiation, since there's no Draft and no
// human turns. Tool activity rendering itself is shared (see ToolActivity.tsx,
// docs/adr/0019); only the trace shape differs, since a single run can
// interleave several tool-call sequences with narration in between, unlike
// a Conversation turn's one bundled sequence.
export function ExecutePanel({ projectId, taskId, onExecuted }: ExecutePanelProps) {
  const [pastExecutions, setPastExecutions] = useState<Execution[]>([])
  const [trace, setTrace] = useState<TraceBlock[]>([])
  const [running, setRunning] = useState(false)
  const [runError, setRunError] = useState<string | null>(null)
  const [executor, setExecutor] = useState('')
  const [executorOptions, setExecutorOptions] = useState<string[]>([])
  // continuableExecutionId is the execution_id handleGetContinuableExecution
  // offers (empty when there's nothing eligible) — resolveFailureContinuation
  // already excludes this whenever a needs_changes retry is auto-continuing,
  // so the two mechanisms never compete for the same choice. continueChoice
  // defaults to 'continue' once something's offered, since preserving prior
  // work is usually the more valuable outcome after a failure (e.g. a
  // turn-cap exhaustion where the run was likely far along).
  const [continuableExecutionId, setContinuableExecutionId] = useState('')
  const [continueChoice, setContinueChoice] = useState<'continue' | 'fresh'>('continue')
  // abortControllerRef tracks the in-flight run's controller so Stop can
  // cancel it — same pattern as StageConversationPanel's.
  const abortControllerRef = useRef<AbortController | null>(null)
  const historyRef = useStickyAutoScroll(trace)

  useEffect(() => {
    let cancelled = false
    listExecutions(projectId, taskId)
      .then((result) => {
        if (!cancelled) {
          setPastExecutions(result.executions ?? [])
        }
      })
      .catch(() => undefined) // no prior attempts, or the list failed to load — either way, nothing to show yet

    getContinuableExecution(projectId, taskId)
      .then((result) => {
        if (!cancelled) {
          setContinuableExecutionId(result.execution_id)
          setContinueChoice('continue')
        }
      })
      .catch(() => undefined) // nothing to offer, or the lookup failed — either way, no toggle shown

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

  // appendText coalesces consecutive text deltas into a single growing
  // trace block rather than one block per delta, matching how
  // StageConversationPanel grows one message's content in place. Any
  // trailing 'tools' block is left as-is (already closed — its sequence
  // ended the moment this text started).
  function appendText(chunk: string) {
    setTrace((prev) => {
      const last = prev[prev.length - 1]
      if (last && last.kind === 'text') {
        return [...prev.slice(0, -1), { ...last, content: last.content + chunk }]
      }
      return [...prev, { kind: 'text', content: chunk }]
    })
  }

  // appendToolCall starts a new pending activity — continuing the trailing
  // 'tools' block (same sequence) if there is one, or opening a new one.
  function appendToolCall(name: string | undefined, args: string | undefined) {
    setTrace((prev) => {
      const activity: ToolActivityEntry = { name: name ?? '', arguments: args }
      const last = prev[prev.length - 1]
      if (last && last.kind === 'tools') {
        return [...prev.slice(0, -1), { ...last, activities: [...last.activities, activity] }]
      }
      return [...prev, { kind: 'tools', activities: [activity] }]
    })
  }

  // appendToolResult fills in the most recently opened sequence's last
  // pending call — tool_call/tool_result events arrive strictly paired and
  // in order (internal/api/execution.go), so the trailing 'tools' block's
  // last activity is always the one this result belongs to.
  function appendToolResult(result: string, isError: boolean | undefined) {
    setTrace((prev) => {
      const last = prev[prev.length - 1]
      if (!last || last.kind !== 'tools' || last.activities.length === 0) {
        return prev
      }
      const activities = [...last.activities]
      activities[activities.length - 1] = { ...activities[activities.length - 1], result, isError }
      return [...prev.slice(0, -1), { ...last, activities }]
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
        appendToolCall(event.tool_name, event.tool_input)
        return
      case 'tool_result':
        appendToolResult(event.tool_result ?? '', event.is_error)
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
          // This run's own outcome can change what's eligible to continue
          // from next (e.g. it just became the new most-recent
          // failure/partial) — re-fetch rather than guess at
          // resolveFailureContinuation's rule client-side.
          getContinuableExecution(projectId, taskId)
            .then((result) => {
              setContinuableExecutionId(result.execution_id)
              setContinueChoice('continue')
            })
            .catch(() => undefined)
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
      const continueFrom = continuableExecutionId && continueChoice === 'continue' ? continuableExecutionId : undefined
      await startExecution(projectId, taskId, executor, handleStreamEvent, controller.signal, continueFrom)
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

      {continuableExecutionId && (
        <fieldset className="continue-choice" disabled={running}>
          <legend>Prior attempt {continuableExecutionId} didn&apos;t finish</legend>
          <label>
            <input
              type="radio"
              name="continue-choice"
              value="continue"
              checked={continueChoice === 'continue'}
              onChange={() => setContinueChoice('continue')}
            />
            Continue from {continuableExecutionId}
          </label>
          <label>
            <input
              type="radio"
              name="continue-choice"
              value="fresh"
              checked={continueChoice === 'fresh'}
              onChange={() => setContinueChoice('fresh')}
            />
            Start fresh
          </label>
        </fieldset>
      )}

      {pastExecutions.length > 0 && (
        <ul className="execution-history">
          {pastExecutions.map((e) => (
            <li key={e.execution_id} className={`execution-status execution-status-${e.status}`}>
              {e.execution_id}: {e.status}
              {e.output.git_branch && <> &middot; {e.output.git_branch}</>}
              {(e.output.commits?.length ?? 0) > 0 && <> &middot; {e.output.commits.length} commit(s)</>}
              {e.failure && <> &middot; {e.failure.message}</>}
              {e.output.workspace_dirty && <> &middot; workspace still has uncommitted changes</>}
            </li>
          ))}
        </ul>
      )}

      {trace.length > 0 && (
        <div className="chat-history" ref={historyRef}>
          {trace.map((block, index) => (
            <div key={index} className="chat-message">
              {block.kind === 'text' && <MarkdownMessage content={block.content} />}
              {block.kind === 'tools' && (
                <ToolActivitySequence activities={block.activities} live={running && index === trace.length - 1} />
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

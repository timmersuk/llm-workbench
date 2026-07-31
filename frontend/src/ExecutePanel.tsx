import { useEffect, useRef, useState } from 'react'
import { getContinuableExecution, isAbortError, listAgentExecutors, listExecutions, listReviews, startExecution } from './api'
import { ExecutionHistoryList } from './ExecutionHistoryList'
import { MarkdownMessage } from './MarkdownMessage'
import { ToolActivitySequence } from './ToolActivity'
import { appendTextBlock, appendToolCallBlock, appendToolResultBlock } from './toolActivityBlocks'
import type { ToolActivityBlock } from './toolActivityBlocks'
import type { Execution, ExecuteStreamEvent, Review } from './types'
import { useStickyAutoScroll } from './useStickyAutoScroll'

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
  const [trace, setTrace] = useState<ToolActivityBlock[]>([])
  const [running, setRunning] = useState(false)
  const [runError, setRunError] = useState<string | null>(null)
  const [executor, setExecutor] = useState('')
  const [executorOptions, setExecutorOptions] = useState<string[]>([])
  // executorsError is set when listAgentExecutors itself fails (server
  // unreachable, 500, etc.) — distinct from a successful response reporting
  // zero healthy executors, which is a legitimate state and leaves this
  // null. Without this, "No executor available" read the same whether the
  // server was unreachable or genuinely had nothing healthy.
  const [executorsError, setExecutorsError] = useState<string | null>(null)
  // continuableExecutionId is the execution_id handleGetContinuableExecution
  // offers (empty when there's nothing eligible) — resolveFailureContinuation
  // already excludes this whenever a needs_changes retry is auto-continuing,
  // so the two mechanisms never compete for the same choice. continueChoice
  // defaults to 'continue' once something's offered, since preserving prior
  // work is usually the more valuable outcome after a failure (e.g. a
  // turn-cap exhaustion where the run was likely far along).
  const [continuableExecutionId, setContinuableExecutionId] = useState('')
  const [continueChoice, setContinueChoice] = useState<'continue' | 'fresh'>('continue')
  // latestReview backs the "sent back from review" banner below — when its
  // decision is needs_changes, this run will silently continue from that
  // review's execution branch with its notes folded into the prompt
  // (resolveReviewContinuation, internal/api/execution.go); this banner is
  // the only place that's visible before the run actually happens.
  const [latestReview, setLatestReview] = useState<Review | null>(null)
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

    listReviews(projectId, taskId)
      .then((result) => {
        if (!cancelled) {
          const reviews = result.reviews ?? []
          setLatestReview(reviews[reviews.length - 1] ?? null)
        }
      })
      .catch(() => undefined) // no reviews yet, or the lookup failed — either way, no banner shown

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
      .catch((err) => {
        if (!cancelled) {
          setExecutorsError(err instanceof Error ? err.message : String(err))
        }
      }) // Run Execution stays disabled below; the error is surfaced near the picker

    return () => {
      cancelled = true
    }
  }, [projectId, taskId])

  function handleStreamEvent(event: ExecuteStreamEvent) {
    switch (event.type) {
      case 'text':
        if (event.content) {
          setTrace((prev) => appendTextBlock(prev, event.content!))
        }
        return
      case 'tool_call':
        setTrace((prev) => appendToolCallBlock(prev, { id: event.id, name: event.tool_name ?? '', arguments: event.tool_input }))
        return
      case 'tool_result':
        setTrace((prev) => appendToolResultBlock(prev, event.id ?? '', event.tool_result ?? '', event.is_error))
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

  // lastExecution backs the "Last run ..." status line shown in place of
  // an empty trace — pastExecutions is sorted ascending by execution id
  // (internal/task ListExecutions), so the last entry is the most recent
  // attempt, whether from this session's history fetch or a run that just
  // completed (handleStreamEvent's 'done' case appends to the same state).
  const lastExecution = pastExecutions[pastExecutions.length - 1]
  const isReviewContinuation = latestReview?.decision === 'needs_changes'

  return (
    <div className="stage-conversation">
      <div className="stage-conversation-header">
        <h4>Execute</h4>
        <p className="stage-conversation-intro">
          Runs an agent autonomously, on an isolated git branch, to implement the approved plan — no approval
          checkpoints mid-run; review the result afterward.
        </p>
      </div>

      {isReviewContinuation && latestReview && (
        <div className="workspace-status-banner">
          <p>This task was sent back from review with requested changes. Running execution will continue from that reviewed attempt&apos;s branch, with the reviewer&apos;s notes included.</p>
          {latestReview.notes && <MarkdownMessage content={latestReview.notes} />}
        </div>
      )}

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

      {executorsError && <p className="error">Could not reach the server for agent executors: {executorsError}</p>}

      <ExecutionHistoryList projectId={projectId} taskId={taskId} executions={pastExecutions} />

      {trace.length > 0 ? (
        <div className="chat-history" ref={historyRef}>
          {trace.map((block, index) => (
            <div key={index} className="chat-message">
              {block.kind === 'text' && <MarkdownMessage content={block.text} />}
              {block.kind === 'tools' && (
                <ToolActivitySequence activities={block.activities} live={running && index === trace.length - 1} />
              )}
            </div>
          ))}
        </div>
      ) : (
        !running &&
        lastExecution && (
          <p className={`last-run-status execution-status-${lastExecution.status}`}>
            Last run {lastExecution.execution_id}: {lastExecution.status}
            {lastExecution.failure && <> — {lastExecution.failure.message}</>}
          </p>
        )
      )}

      {runError && <p className="error">{runError}</p>}

      {continuableExecutionId && !running && (
        <fieldset className="continue-choice">
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

      <div className="chat-input">
        {running ? (
          <button type="button" className="action-btn-stop" onClick={handleStop}>
            Stop
          </button>
        ) : (
          <button type="button" onClick={handleRun} disabled={!executor}>
            {isReviewContinuation ? 'Continue from Review Feedback' : 'Run Execution'}
          </button>
        )}
      </div>
    </div>
  )
}

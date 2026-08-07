import { useEffect, useRef, useState } from 'react'
import { CopyIcon, DeleteIcon, EditIcon, RegenerateIcon } from './ActionIcons'
import { closeChatSession, isAbortError, listAgentExecutors, listModels, postChatPermissionDecision, streamChatCompletion } from './api'
import { ChatInputArea } from './ChatInputArea'
import { formatMessageTimestamp } from './formatTimestamp'
import { MarkdownMessage } from './MarkdownMessage'
import { PermissionRequestPanel } from './PermissionRequestPanel'
import { ALL_REASONING_EFFORTS, resolveEffort } from './reasoningEffort'
import { ToolActivitySequence } from './ToolActivity'
import { appendTextBlock, appendToolCallBlock, appendToolResultBlock } from './toolActivityBlocks'
import type { ToolActivityBlock } from './toolActivityBlocks'
import type { ChatHistoryEntry, ChatStreamEvent, ReasoningEffort } from './types'
import { useLiveTurnStatus } from './useLiveTurnStatus'
import { usePermissionEscalation } from './usePermissionEscalation'

interface DisplayMessage {
  role: string
  // content is a derived concatenation of every text segment's text, kept
  // alongside segments purely for handleCopyMessage/handleEditMessage/resend
  // (all three need the plain reply text, not its segment structure) — see
  // StageConversationPanel's identical DisplayMessage.content doc comment.
  content: string
  reasoningContent: string
  segments: ToolActivityBlock[]
  error: string | null
  thinkingCollapsed: boolean
  created_at: string
}

// executorLabels maps an agent executor key (internal/agentrunner) to its
// display label, for whichever keys listAgentExecutors currently reports
// healthy. Unlike StageConversationPanel, every key here is a legitimate
// free-chat choice — nothing is filtered out.
const executorLabels: Record<string, string> = { local: 'Local LLM chat', 'claude-code': 'Claude Code' }

export function ChatPanel() {
  const [messages, setMessages] = useState<DisplayMessage[]>([])
  const [draft, setDraft] = useState('')
  const [sending, setSending] = useState(false)
  const [models, setModels] = useState<string[]>([])
  const [selectedModel, setSelectedModel] = useState('')
  const [modelsError, setModelsError] = useState<string | null>(null)
  const [effort, setEffort] = useState<ReasoningEffort>('medium')
  // efforts mirrors models above: the currently-selected executor's
  // advertised effort choices, re-derived on every executor change instead
  // of offering a static low/medium/high list regardless of what that
  // executor actually supports — see reasoningEffort.ts.
  const [efforts, setEfforts] = useState<ReasoningEffort[]>(ALL_REASONING_EFFORTS)
  const [executor, setExecutor] = useState('')
  const [executorOptions, setExecutorOptions] = useState<Array<{ value: string; label: string }>>([])
  // executorsError mirrors modelsError above, for listAgentExecutors — set
  // only when the request itself fails (server unreachable, 500, etc.), not
  // when it succeeds with zero healthy executors.
  const [executorsError, setExecutorsError] = useState<string | null>(null)
  const [sessionKey, setSessionKey] = useState(() => crypto.randomUUID())
  // editingIndex is the position of the user message currently being
  // edited (draft holds its in-progress edited text), or null when not
  // editing.
  const [editingIndex, setEditingIndex] = useState<number | null>(null)
  // abortControllerRef tracks the in-flight stream's controller so the
  // Stop button can cancel it — a ref rather than state since aborting
  // never needs to trigger a re-render itself (setSending's finally
  // already does that).
  const abortControllerRef = useRef<AbortController | null>(null)
  // streamedChars/finalTokens feed the live token-estimate indicator
  // (docs/adr/0018) — see StageConversationPanel's identical fields for the
  // full rationale.
  const [streamedChars, setStreamedChars] = useState(0)
  const [finalTokens, setFinalTokens] = useState<number | undefined>(undefined)
  const liveTurnStatus = useLiveTurnStatus(sending, streamedChars, finalTokens)
  // permission manages an in-flight tool escalation (docs/adr/0024): the turn
  // is blocked server-side until Approve/Deny is clicked. Shared with
  // StageConversationPanel via usePermissionEscalation — see its doc comment.
  const permission = usePermissionEscalation((id, allow) => postChatPermissionDecision(sessionKey, id, allow))

  useEffect(() => {
    listAgentExecutors()
      .then((result) => {
        const options = result.executors.map((key) => ({ value: key, label: executorLabels[key] ?? key }))
        setExecutorOptions(options)
        setExecutor((current) => current || options[0]?.value || '')
      })
      .catch((err) => setExecutorsError(err instanceof Error ? err.message : String(err)))
  }, [])

  // Re-derives models/efforts whenever the selected executor changes,
  // including the very first time it's resolved (either from the initial
  // fetch above or a user's own pick) — a fetch keyed on the actual executor
  // rather than a fixed default, so "local"'s models never briefly show
  // under a different executor before a user-driven change corrects it (see
  // ExecutePanel's identical pattern).
  useEffect(() => {
    let cancelled = false
    setModelsError(null)
    if (!executor) {
      return () => {
        cancelled = true
      }
    }
    listModels(executor)
      .then((result) => {
        if (cancelled) {
          return
        }
        setModels(result.models)
        setSelectedModel((current) => (result.models.includes(current) ? current : (result.models[0] ?? '')))
        const availableEfforts = result.efforts ?? ALL_REASONING_EFFORTS
        setEfforts(availableEfforts)
        setEffort((current) => resolveEffort(current, availableEfforts, result.default_effort))
      })
      .catch((err) => {
        if (!cancelled) {
          setModelsError(err instanceof Error ? err.message : String(err))
        }
      })
    return () => {
      cancelled = true
    }
  }, [executor])

  function updateLastMessage(update: (msg: DisplayMessage) => DisplayMessage) {
    setMessages((prev) => {
      if (prev.length === 0) {
        return prev
      }
      const next = prev.slice()
      next[next.length - 1] = update(next[next.length - 1])
      return next
    })
  }

  // updateMessageAt is updateLastMessage's general form, for surfacing a
  // delete failure inline on the specific message that failed to delete
  // (not necessarily the last one).
  function updateMessageAt(index: number, update: (msg: DisplayMessage) => DisplayMessage) {
    setMessages((prev) => {
      if (index < 0 || index >= prev.length) {
        return prev
      }
      const next = prev.slice()
      next[index] = update(next[index])
      return next
    })
  }

  function handleStreamEvent(event: ChatStreamEvent) {
    if (event.error) {
      // A user-initiated Stop cancels the request context, which the
      // backend surfaces as a normal SSE error event (e.g. "context
      // canceled") rather than a thrown exception — isAbortError's catch-
      // block check never sees it. Checking the controller's own signal
      // here is what actually distinguishes "stopped on purpose" from a
      // real failure, so a deliberate stop doesn't read as an error.
      if (abortControllerRef.current?.signal.aborted) {
        return
      }
      updateLastMessage((msg) => ({ ...msg, error: event.error! }))
      return
    }
    if (event.permission_request) {
      // The turn is blocked server-side awaiting this decision; surface the
      // Approve/Deny control (a new prompt replaces any stale one).
      permission.receive(event.permission_request)
      return
    }
    if (event.tool_activity) {
      // Surfaces a Claude Code turn's intermediate Read/Grep/Glob/Bash/etc.
      // calls live, the same way StageConversationPanel already does — see
      // that panel's identical branch and toolActivityBlocks.ts's doc
      // comment for why this is the one shared implementation, not a copy.
      const ta = event.tool_activity
      if (ta.phase === 'call') {
        updateLastMessage((msg) => ({ ...msg, segments: appendToolCallBlock(msg.segments, { id: ta.id, name: ta.name, arguments: ta.arguments }) }))
      } else {
        updateLastMessage((msg) => ({ ...msg, segments: appendToolResultBlock(msg.segments, ta.id, ta.result ?? '', ta.is_error) }))
      }
      return
    }
    if (event.usage) {
      setFinalTokens(event.usage.total_tokens)
    }
    if (event.reasoning_content) {
      updateLastMessage((msg) => ({ ...msg, reasoningContent: msg.reasoningContent + event.reasoning_content }))
      setStreamedChars((n) => n + event.reasoning_content!.length)
    }
    if (event.content) {
      updateLastMessage((msg) => ({
        ...msg,
        segments: appendTextBlock(msg.segments, event.content!),
        content: msg.content + event.content,
        thinkingCollapsed: true,
      }))
      setStreamedChars((n) => n + event.content!.length)
    }
  }

  // sendText appends a fresh [user, assistant] pair and streams the reply.
  // The server holds this session's history itself in the normal case, but
  // the current local list is sent as history alongside every turn anyway
  // (cheap, already in memory) — it's a no-op whenever the session is
  // still live, and it's exactly what makes a prior delete/edit/regenerate
  // (which evicts the session) actually stick once the human's next
  // message reconnects it.
  async function sendText(text: string) {
    const trimmedText = text.trim()
    if (!trimmedText || sending || !executor) {
      return
    }

    const historyForResend: ChatHistoryEntry[] = messages.map((m) => ({ role: m.role, content: m.content }))

    setMessages((prev) => [
      ...prev,
      { role: 'user', content: trimmedText, reasoningContent: '', segments: [{ kind: 'text', text: trimmedText }], error: null, thinkingCollapsed: true, created_at: new Date().toISOString() },
      { role: 'assistant', content: '', reasoningContent: '', segments: [], error: null, thinkingCollapsed: false, created_at: new Date().toISOString() },
    ])
    setSending(true)
    setStreamedChars(0)
    setFinalTokens(undefined)
    const controller = new AbortController()
    abortControllerRef.current = controller

    try {
      await streamChatCompletion(trimmedText, selectedModel, executor, sessionKey, handleStreamEvent, historyForResend, controller.signal, effort)
    } catch (err) {
      if (!isAbortError(err)) {
        updateLastMessage((msg) => ({ ...msg, error: err instanceof Error ? err.message : String(err) }))
      }
    } finally {
      abortControllerRef.current = null
      setSending(false)
    }
  }

  // handleStop aborts the in-flight stream (Stop button) — Go's net/http
  // cancels the handler's request context when the client aborts the
  // connection, so this actually interrupts the backend run, not just the
  // frontend's rendering of it.
  function handleStop() {
    abortControllerRef.current?.abort()
  }

  async function handleSend() {
    if (editingIndex !== null) {
      const text = draft.trim()
      if (!text) {
        return
      }
      const index = editingIndex
      setEditingIndex(null)
      setDraft('')
      await truncateAndResend(index, text)
      return
    }
    const text = draft
    setDraft('')
    await sendText(text)
  }

  // truncateAndResend discards everything from index onward locally, evicts
  // the server-held session (closeChatSession — free chat has no durable
  // copy, so eviction plus the truncated history sent alongside the resend
  // is the only way a correction reaches what the model sees), and resends
  // content as a fresh [user, assistant] pair. Shared by Edit (index is the
  // edited message's own position, content is the new text) and Regenerate
  // (index is the preceding user message, content is its existing text
  // unchanged).
  async function truncateAndResend(index: number, content: string) {
    if (sending || !executor) {
      return
    }
    const historyForResend: ChatHistoryEntry[] = messages.slice(0, index).map((m) => ({ role: m.role, content: m.content }))

    setMessages((prev) => [
      ...prev.slice(0, index),
      { role: 'user', content, reasoningContent: '', segments: [{ kind: 'text', text: content }], error: null, thinkingCollapsed: true, created_at: new Date().toISOString() },
      { role: 'assistant', content: '', reasoningContent: '', segments: [], error: null, thinkingCollapsed: false, created_at: new Date().toISOString() },
    ])
    setSending(true)
    setStreamedChars(0)
    setFinalTokens(undefined)
    const controller = new AbortController()
    abortControllerRef.current = controller

    try {
      await closeChatSession(sessionKey)
    } catch {
      // Best-effort, same as handleNewChat — if this fails the resend
      // below still sends the corrected history, it just won't take effect
      // until the (still-live) session eventually gets evicted some other way.
    }

    try {
      await streamChatCompletion(content, selectedModel, executor, sessionKey, handleStreamEvent, historyForResend, controller.signal, effort)
    } catch (err) {
      if (!isAbortError(err)) {
        updateLastMessage((msg) => ({ ...msg, error: err instanceof Error ? err.message : String(err) }))
      }
    } finally {
      abortControllerRef.current = null
      setSending(false)
    }
  }

  function handleCopyMessage(content: string) {
    void navigator.clipboard.writeText(content)
  }

  function handleEditMessage(index: number) {
    setEditingIndex(index)
    setDraft(messages[index].content)
  }

  function handleCancelEdit() {
    setEditingIndex(null)
    setDraft('')
  }

  // handleRegenerateMessage targets assistantIndex's preceding user message
  // (a well-formed conversation always alternates user/assistant, so
  // assistantIndex - 1 is that user turn) and resends its existing content
  // unchanged.
  function handleRegenerateMessage(assistantIndex: number) {
    const userIndex = assistantIndex - 1
    if (userIndex < 0 || sending) {
      return
    }
    void truncateAndResend(userIndex, messages[userIndex].content)
  }

  // handleDeleteMessage removes a message locally and evicts the
  // server-held session immediately (rather than deferring to the next
  // resend) — otherwise a still-live session would keep the deleted
  // message in context regardless of what the frontend displays.
  async function handleDeleteMessage(index: number) {
    if (sending) {
      return
    }
    try {
      await closeChatSession(sessionKey)
      setMessages((prev) => prev.filter((_, i) => i !== index))
      if (editingIndex === index) {
        setEditingIndex(null)
        setDraft('')
      }
    } catch (err) {
      updateMessageAt(index, (msg) => ({ ...msg, error: err instanceof Error ? err.message : String(err) }))
    }
  }

  async function handleNewChat() {
    try {
      await closeChatSession(sessionKey)
    } catch {
      // Best-effort — the old session just lingers server-side if this
      // fails, no different from a page refresh losing track of it today.
    }
    setSessionKey(crypto.randomUUID())
    setMessages([])
    setDraft('')
    setEditingIndex(null)
  }

  return (
    <div className="chat-panel">
      <div className="chat-model-row">
        <label htmlFor="chat-executor-select">Executor</label>
        <select
          id="chat-executor-select"
          value={executor}
          onChange={(e) => setExecutor(e.target.value)}
          disabled={executorOptions.length === 0}
        >
          {executorOptions.length === 0 && <option value="">No executors available</option>}
          {executorOptions.map((opt) => (
            <option key={opt.value} value={opt.value}>
              {opt.label}
            </option>
          ))}
        </select>

        {models.length > 0 && (
          <>
            <label htmlFor="chat-model-select">Model</label>
            <select
              id="chat-model-select"
              value={selectedModel}
              onChange={(e) => setSelectedModel(e.target.value)}
              disabled={models.length === 0}
            >
              {models.length === 0 && <option value="">No models available</option>}
              {models.map((model) => (
                <option key={model} value={model}>
                  {model}
                </option>
              ))}
            </select>
          </>
        )}

        <label htmlFor="chat-effort-select">Effort</label>
        <select
          id="chat-effort-select"
          value={effort}
          onChange={(e) => setEffort(e.target.value as ReasoningEffort)}
          disabled={efforts.length === 0}
        >
          {!efforts.includes(effort) && <option value={effort}>{effort}</option>}
          {efforts.map((option) => (
            <option key={option} value={option}>
              {option}
            </option>
          ))}
        </select>

        <button type="button" onClick={handleNewChat} disabled={sending}>
          New chat
        </button>
      </div>
      {executorsError && <p className="error">Could not reach the server for agent executors: {executorsError}</p>}
      {modelsError && <p className="error">Could not load models: {modelsError}</p>}
      <div className="chat-history">
        {messages.map((message, index) => (
          <div key={index} className={`chat-message chat-message-${message.role}`}>
            {message.reasoningContent && (
              <details
                className="thinking-panel"
                open={!message.thinkingCollapsed}
                onToggle={(e) => {
                  const isOpen = (e.target as HTMLDetailsElement).open
                  setMessages((prev) =>
                    prev.map((m, i) => (i === index ? { ...m, thinkingCollapsed: !isOpen } : m)),
                  )
                }}
              >
                <summary>Thinking</summary>
                <div className="thinking-content">{message.reasoningContent}</div>
              </details>
            )}
            <strong>{message.role}:</strong> <span className="message-timestamp">{formatMessageTimestamp(message.created_at)}</span>{' '}
            {message.segments.map((segment, segIndex) =>
              segment.kind === 'text' ? (
                <div key={segIndex}>
                  <MarkdownMessage content={segment.text} />
                </div>
              ) : (
                <ToolActivitySequence
                  key={segIndex}
                  activities={segment.activities}
                  live={sending && index === messages.length - 1 && segIndex === message.segments.length - 1}
                />
              ),
            )}
            {message.error && <p className="error">{message.error}</p>}
            <div className="message-actions">
              <button type="button" className="action-btn" title="Copy" aria-label="Copy" onClick={() => handleCopyMessage(message.content)}>
                <CopyIcon />
              </button>
              {message.role === 'user' && (
                <button
                  type="button"
                  className="action-btn"
                  title="Edit and continue from here"
                  aria-label="Edit"
                  onClick={() => handleEditMessage(index)}
                  disabled={sending}
                >
                  <EditIcon />
                </button>
              )}
              {message.role === 'assistant' && index > 0 && (
                <button
                  type="button"
                  className="action-btn"
                  title="Regenerate"
                  aria-label="Regenerate"
                  onClick={() => handleRegenerateMessage(index)}
                  disabled={sending}
                >
                  <RegenerateIcon />
                </button>
              )}
              <button
                type="button"
                className="action-btn"
                title="Delete"
                aria-label="Delete"
                onClick={() => handleDeleteMessage(index)}
                disabled={sending}
              >
                <DeleteIcon />
              </button>
            </div>
          </div>
        ))}
      </div>
      {permission.pending && (
        <PermissionRequestPanel
          pending={permission.pending}
          deciding={permission.deciding}
          error={permission.error}
          onDecide={permission.decide}
        />
      )}
      <ChatInputArea
        draft={draft}
        onDraftChange={setDraft}
        onSend={handleSend}
        onStop={handleStop}
        onCancelEdit={handleCancelEdit}
        sending={sending}
        editingIndex={editingIndex}
        canSend={!!executor}
        placeholder="Message the local LLM..."
        liveTurnStatus={liveTurnStatus}
      />
    </div>
  )
}

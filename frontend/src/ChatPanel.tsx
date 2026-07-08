import { useEffect, useRef, useState } from 'react'
import { closeChatSession, isAbortError, listAgentExecutors, listModels, streamChatCompletion } from './api'
import { MarkdownMessage } from './MarkdownMessage'
import type { ChatHistoryEntry, ChatStreamEvent } from './types'

interface DisplayMessage {
  role: string
  content: string
  reasoningContent: string
  error: string | null
  thinkingCollapsed: boolean
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
  const [executor, setExecutor] = useState('')
  const [executorOptions, setExecutorOptions] = useState<Array<{ value: string; label: string }>>([])
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

  useEffect(() => {
    listModels()
      .then((result) => {
        setModels(result.models)
        setSelectedModel((current) => current || result.models[0] || '')
      })
      .catch((err) => setModelsError(err instanceof Error ? err.message : String(err)))
    listAgentExecutors()
      .then((result) => {
        const options = result.executors.map((key) => ({ value: key, label: executorLabels[key] ?? key }))
        setExecutorOptions(options)
        setExecutor((current) => current || options[0]?.value || '')
      })
      .catch(() => undefined)
  }, [])

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
    if (event.reasoning_content) {
      updateLastMessage((msg) => ({ ...msg, reasoningContent: msg.reasoningContent + event.reasoning_content }))
    }
    if (event.content) {
      updateLastMessage((msg) => ({ ...msg, content: msg.content + event.content, thinkingCollapsed: true }))
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
      { role: 'user', content: trimmedText, reasoningContent: '', error: null, thinkingCollapsed: true },
      { role: 'assistant', content: '', reasoningContent: '', error: null, thinkingCollapsed: false },
    ])
    setSending(true)
    const controller = new AbortController()
    abortControllerRef.current = controller

    try {
      await streamChatCompletion(trimmedText, selectedModel, executor, sessionKey, handleStreamEvent, historyForResend, controller.signal)
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
      { role: 'user', content, reasoningContent: '', error: null, thinkingCollapsed: true },
      { role: 'assistant', content: '', reasoningContent: '', error: null, thinkingCollapsed: false },
    ])
    setSending(true)
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
      await streamChatCompletion(content, selectedModel, executor, sessionKey, handleStreamEvent, historyForResend, controller.signal)
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

        {executor === 'local' && (
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

        <button type="button" onClick={handleNewChat} disabled={sending}>
          New chat
        </button>
      </div>
      {modelsError && executor === 'local' && <p className="error">Could not load models: {modelsError}</p>}
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
            <strong>{message.role}:</strong> <MarkdownMessage content={message.content} />
            {message.error && <p className="error">{message.error}</p>}
            <div className="message-actions">
              <button type="button" className="action-btn" onClick={() => handleCopyMessage(message.content)}>
                Copy
              </button>
              {message.role === 'user' && (
                <button type="button" className="action-btn" onClick={() => handleEditMessage(index)} disabled={sending}>
                  Edit
                </button>
              )}
              {message.role === 'assistant' && index > 0 && (
                <button type="button" className="action-btn" onClick={() => handleRegenerateMessage(index)} disabled={sending}>
                  Regenerate
                </button>
              )}
              <button type="button" className="action-btn" onClick={() => handleDeleteMessage(index)} disabled={sending}>
                Delete
              </button>
            </div>
          </div>
        ))}
      </div>
      <div className="chat-input">
        <textarea
          value={draft}
          onChange={(e) => setDraft(e.target.value)}
          placeholder={editingIndex !== null ? 'Editing message...' : 'Message the local LLM...'}
          rows={3}
          disabled={sending}
        />
        {editingIndex !== null ? (
          <div className="chat-input-edit-controls">
            <button type="button" className="action-btn-cancel" onClick={handleCancelEdit} disabled={sending}>
              Cancel
            </button>
            <button type="button" onClick={handleSend} disabled={sending || !draft.trim() || !executor}>
              {sending ? 'Saving...' : 'Save'}
            </button>
          </div>
        ) : sending ? (
          <button type="button" className="action-btn-stop" onClick={handleStop}>
            Stop
          </button>
        ) : (
          <button onClick={handleSend} disabled={!draft.trim() || !executor}>
            Send
          </button>
        )}
      </div>
    </div>
  )
}

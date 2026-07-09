import { useEffect, useRef, useState } from 'react'
import type { KeyboardEvent, ReactNode } from 'react'
import {
  deleteStageMessage,
  getStageConversation,
  isAbortError,
  listAgentExecutors,
  listModels,
  postStageMessage,
  regenerateStageMessage,
  startStageConversation,
} from './api'
import { MarkdownMessage } from './MarkdownMessage'
import type { ChatStreamEvent, ConversationMessage } from './types'

interface DisplayMessage {
  role: string
  content: string
  reasoningContent: string
  toolCallName: string | null
  error: string | null
  thinkingCollapsed: boolean
}

// mergeDraftDefaults fills in emptyDraft's fields the model's tool call
// omitted, one level deep — the model frequently proposes a partial object
// (e.g. context.summary but no context.files), and a plain shallow spread
// would replace an entire nested object with an incomplete one, leaving
// e.g. draft.context.files undefined instead of []. Generic over D since
// this component doesn't know Draft's shape (RequirementsDraft nests a
// context object, TaskPlan is flat) — this only needs to recurse one level
// to cover both.
function mergeDraftDefaults<D>(emptyDraft: D, parsed: Partial<D>): D {
  const merged: D = { ...emptyDraft, ...parsed }
  for (const key of Object.keys(emptyDraft as object) as (keyof D)[]) {
    const defaultValue = emptyDraft[key]
    const parsedValue = parsed[key]
    if (isPlainObject(defaultValue) && isPlainObject(parsedValue)) {
      merged[key] = { ...defaultValue, ...parsedValue }
    }
  }
  return merged
}

function isPlainObject(value: unknown): value is Record<string, unknown> {
  return typeof value === 'object' && value !== null && !Array.isArray(value)
}

function toDisplayMessage(m: ConversationMessage): DisplayMessage {
  return {
    role: m.role,
    content: m.content,
    reasoningContent: '',
    toolCallName: m.tool_call?.name ?? null,
    error: m.error ?? null,
    thinkingCollapsed: true,
  }
}

interface StageConversationPanelProps<D> {
  projectId: string
  taskId: string
  stage: string
  // title/description label this interview so it reads as a distinct,
  // interactive zone rather than more of the read-only summary above it
  // (TaskDetailPanel wraps the read-only fields and this panel in
  // separately styled containers for the same reason).
  title: string
  description: string
  emptyDraft: D
  renderDraft: (draft: D, onChange: (draft: D) => void) => ReactNode
  onFinalize: (draft: D) => Promise<void>
}

// localChatOption is always available — the local-LLM chat path
// (internal/chat) needs no server opt-in, unlike agent executors below.
const localChatOption = { value: '', label: 'Local LLM chat' }

// executorLabels maps an agent executor key (internal/agentrunner) to its
// display label, for whichever keys listAgentExecutors currently reports
// healthy — an executor that isn't live right now is never offered, so
// selecting one can't 400.
const executorLabels: Record<string, string> = { 'claude-code': 'Claude Code', codex: 'Codex CLI' }

// StageConversationPanel is the mechanism shared by GrillMe and Planning
// Mode (CONTEXT.md): a persisted Conversation transcript, a message input
// that streams the assistant's reply, and — when the model calls its
// registered tool — a Draft, shown via renderDraft for the human to edit
// before Finalize or discard (which just clears local state; no API call).
export function StageConversationPanel<D>({
  projectId,
  taskId,
  stage,
  title,
  description,
  emptyDraft,
  renderDraft,
  onFinalize,
}: StageConversationPanelProps<D>) {
  const [messages, setMessages] = useState<DisplayMessage[]>([])
  const [loadError, setLoadError] = useState<string | null>(null)
  const [draft, setDraft] = useState('')
  const [sending, setSending] = useState(false)
  const [models, setModels] = useState<string[]>([])
  const [selectedModel, setSelectedModel] = useState('')
  const [executor, setExecutor] = useState('')
  const [executorOptions, setExecutorOptions] = useState([localChatOption])
  const [pendingDraft, setPendingDraft] = useState<D | null>(null)
  const [finalizing, setFinalizing] = useState(false)
  const [finalizeError, setFinalizeError] = useState<string | null>(null)
  const [requestChangesComment, setRequestChangesComment] = useState('')
  // editingIndex is the position of the user message currently being
  // edited (draft holds its in-progress edited text), or null when not
  // editing — mutually exclusive with normal sending.
  const [editingIndex, setEditingIndex] = useState<number | null>(null)
  // abortControllerRef tracks the in-flight stream's controller so the
  // Stop button can cancel it — a ref rather than state since aborting
  // never needs to trigger a re-render itself (setSending's finally
  // already does that).
  const abortControllerRef = useRef<AbortController | null>(null)

  useEffect(() => {
    let cancelled = false

    async function init() {
      let loadedEmpty = false

      try {
        const conv = await getStageConversation(projectId, taskId, stage)
        const loaded = conv.messages ?? []
        if (cancelled) {
          return
        }
        setMessages(loaded.map(toDisplayMessage))
        loadedEmpty = loaded.length === 0
        // Rehydrate the pending draft from the most recent proposed tool
        // call — a reload or server restart must not lose a proposal the
        // human hasn't finalized or discarded yet, since it's already sat
        // for review in a prior session.
        for (let i = loaded.length - 1; i >= 0; i--) {
          const toolCall = loaded[i].tool_call
          if (toolCall) {
            try {
              setPendingDraft(mergeDraftDefaults(emptyDraft, JSON.parse(toolCall.arguments)))
            } catch {
              // Malformed arguments JSON from a past turn — nothing to
              // rehydrate, the human can just keep chatting.
            }
            break
          }
        }
      } catch (err) {
        // Can't confirm the conversation is actually empty, so don't risk
        // auto-starting on top of unknown state — the human can still send
        // a message manually once the load error is visible.
        if (!cancelled) {
          setLoadError(err instanceof Error ? err.message : String(err))
        }
        return
      }

      let resolvedModel = ''
      try {
        const result = await listModels()
        if (!cancelled) {
          setModels(result.models)
          resolvedModel = result.models[0] ?? ''
          setSelectedModel((current) => current || resolvedModel)
        }
      } catch {
        // No models available — local-chat model selection just stays empty.
      }

      let resolvedExecutor = ''
      try {
        const result = await listAgentExecutors()
        if (!cancelled) {
          // "local" is registered for the free-floating Chat tab, not for
          // stage conversations — it has no notion of this stage's
          // Draft-proposing tool, so selecting it here would silently never
          // produce a Draft. Filtered out rather than offered as a dead end.
          const executors = result.executors.filter((key) => key !== 'local')
          setExecutorOptions([localChatOption, ...executors.map((key) => ({ value: key, label: executorLabels[key] ?? key }))])
          // claude-code can ground its questions in the actual repository
          // (Read/Grep/Glob), unlike the local chat path, so it's preferred
          // whenever it's healthy — the picker still lets the human switch
          // back to local chat.
          if (executors.includes('claude-code')) {
            resolvedExecutor = 'claude-code'
            setExecutor((current) => current || 'claude-code')
          }
        }
      } catch {
        // No agent executors available — Local LLM chat stays the only option.
      }

      // A brand-new conversation has nothing for the human to reply to —
      // GrillMe/Planning Mode asks the opening question itself instead of
      // waiting on a message that doesn't exist yet.
      if (!cancelled && loadedEmpty) {
        await startConversation(resolvedModel, resolvedExecutor)
      }
    }

    init()
    return () => {
      cancelled = true
    }
    // eslint-disable-next-line react-hooks/exhaustive-deps
  }, [projectId, taskId, stage])

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

  // handleStreamEvent applies one streamed ChatStreamEvent to the
  // in-progress last message — shared by sendMessage's postStageMessage
  // call and startConversation's startStageConversation call, since both
  // stream the identical event shape onto an assistant bubble that's
  // already been appended by their respective callers.
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
    if (event.tool_call) {
      updateLastMessage((msg) => ({ ...msg, toolCallName: event.tool_call!.name }))
      try {
        setPendingDraft(mergeDraftDefaults(emptyDraft, JSON.parse(event.tool_call.arguments)))
      } catch {
        // Malformed arguments JSON is surfaced via the chip only; the
        // human can keep chatting and ask the model to try again.
      }
      return
    }
    if (event.reasoning_content) {
      updateLastMessage((msg) => ({ ...msg, reasoningContent: msg.reasoningContent + event.reasoning_content }))
    }
    if (event.content) {
      updateLastMessage((msg) => ({ ...msg, content: msg.content + event.content, thinkingCollapsed: true }))
    }
  }

  async function sendMessage(text: string) {
    if (!text || sending) {
      return
    }

    setMessages((prev) => [
      ...prev,
      { role: 'user', content: text, reasoningContent: '', toolCallName: null, error: null, thinkingCollapsed: true },
      { role: 'assistant', content: '', reasoningContent: '', toolCallName: null, error: null, thinkingCollapsed: false },
    ])
    setSending(true)
    const controller = new AbortController()
    abortControllerRef.current = controller

    try {
      await postStageMessage(projectId, taskId, stage, text, selectedModel, executor, handleStreamEvent, controller.signal)
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

  // startConversation kicks off a brand-new, empty stage Conversation on
  // load (see the init effect above) — there's no human message to pair
  // with the opening question, so only an assistant bubble is appended.
  // model/executorKey are passed explicitly rather than read from
  // selectedModel/executor state, since this runs as part of the same
  // effect that resolves them — reading state here could race against
  // React not having flushed those updates yet.
  async function startConversation(model: string, executorKey: string) {
    setMessages((prev) => [
      ...prev,
      { role: 'assistant', content: '', reasoningContent: '', toolCallName: null, error: null, thinkingCollapsed: false },
    ])
    setSending(true)
    const controller = new AbortController()
    abortControllerRef.current = controller

    try {
      await startStageConversation(projectId, taskId, stage, model, executorKey, handleStreamEvent, controller.signal)
    } catch (err) {
      if (!isAbortError(err)) {
        updateLastMessage((msg) => ({ ...msg, error: err instanceof Error ? err.message : String(err) }))
      }
    } finally {
      abortControllerRef.current = null
      setSending(false)
    }
  }

  async function handleSend() {
    const text = draft.trim()
    if (!text || sending) {
      return
    }
    if (editingIndex !== null) {
      const index = editingIndex
      setEditingIndex(null)
      setDraft('')
      await truncateAndResend(index, text)
      return
    }
    setDraft('')
    await sendMessage(text)
  }

  // truncateAndResend discards everything from index onward (locally and,
  // via regenerateStageMessage, server-side) and resends content as a
  // fresh [user, assistant] pair — the shared core of both Edit (index is
  // the edited message's own position, content is the new text) and
  // Regenerate (index is the preceding user message, content is its
  // existing text unchanged). Any pending Draft is cleared: whatever tool
  // call produced it may no longer be part of history after truncation.
  async function truncateAndResend(index: number, content: string) {
    if (sending) {
      return
    }
    setMessages((prev) => [
      ...prev.slice(0, index),
      { role: 'user', content, reasoningContent: '', toolCallName: null, error: null, thinkingCollapsed: true },
      { role: 'assistant', content: '', reasoningContent: '', toolCallName: null, error: null, thinkingCollapsed: false },
    ])
    setPendingDraft(null)
    setSending(true)
    const controller = new AbortController()
    abortControllerRef.current = controller

    try {
      await regenerateStageMessage(projectId, taskId, stage, index, content, selectedModel, executor, handleStreamEvent, controller.signal)
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

  // handleRegenerateMessage targets assistantIndex's preceding user
  // message (a well-formed conversation always alternates user/assistant,
  // so assistantIndex - 1 is that user turn) and resends its existing
  // content unchanged.
  function handleRegenerateMessage(assistantIndex: number) {
    const userIndex = assistantIndex - 1
    if (userIndex < 0 || sending) {
      return
    }
    void truncateAndResend(userIndex, messages[userIndex].content)
  }

  async function handleDeleteMessage(index: number) {
    if (sending) {
      return
    }
    try {
      await deleteStageMessage(projectId, taskId, stage, index)
      setMessages((prev) => prev.filter((_, i) => i !== index))
      if (editingIndex === index) {
        setEditingIndex(null)
        setDraft('')
      }
    } catch (err) {
      updateMessageAt(index, (msg) => ({ ...msg, error: err instanceof Error ? err.message : String(err) }))
    }
  }

  // Enter sends the reply, matching most chat UIs; Alt+Enter inserts a
  // newline instead, for the rare multi-line reply. The newline is spliced
  // in at the cursor explicitly (rather than left to the textarea's
  // default handling) so the cursor lands in the right place regardless of
  // modifier keys, since a plain, unmodified Enter is the one case a
  // browser textarea inserts a newline for by default — Alt+Enter isn't.
  // The textarea is disabled while sending, so this never fires mid-stream.
  function handleDraftKeyDown(e: KeyboardEvent<HTMLTextAreaElement>) {
    if (e.key !== 'Enter') {
      return
    }
    if (e.altKey) {
      e.preventDefault()
      const el = e.currentTarget
      const start = el.selectionStart ?? draft.length
      const end = el.selectionEnd ?? draft.length
      const next = draft.slice(0, start) + '\n' + draft.slice(end)
      el.value = next
      el.selectionStart = el.selectionEnd = start + 1
      setDraft(next)
      return
    }
    e.preventDefault()
    void handleSend()
  }

  // handleRequestChanges sends the human's edited draft back to the model
  // as a single message — the comment plus the current (edited) draft as a
  // fenced JSON block — through the same postStageMessage path a normal
  // reply uses, so the transcript shows exactly what the model saw (no
  // hidden state). The system prompt (buildStagePrompt) tells the model to
  // treat such a block as the authoritative starting point for its
  // revision. pendingDraft is cleared since the model is expected to
  // re-propose once it has revised.
  async function handleRequestChanges() {
    if (!pendingDraft || sending) {
      return
    }
    const comment = requestChangesComment.trim()
    const text = `${comment || 'Please revise the draft below.'}\n\n\`\`\`json\n${JSON.stringify(pendingDraft, null, 2)}\n\`\`\``
    setRequestChangesComment('')
    setPendingDraft(null)
    await sendMessage(text)
  }

  async function handleFinalize() {
    if (!pendingDraft || finalizing) {
      return
    }
    setFinalizing(true)
    setFinalizeError(null)
    try {
      await onFinalize(pendingDraft)
      setPendingDraft(null)
    } catch (err) {
      setFinalizeError(err instanceof Error ? err.message : String(err))
    } finally {
      setFinalizing(false)
    }
  }

  return (
    <div className="stage-conversation">
      <div className="stage-conversation-header">
        <h4>{title}</h4>
        <p className="stage-conversation-intro">{description}</p>
      </div>

      <div className="chat-model-row">
        <label htmlFor={`stage-executor-${stage}`}>Executor</label>
        <select id={`stage-executor-${stage}`} value={executor} onChange={(e) => setExecutor(e.target.value)}>
          {executorOptions.map((opt) => (
            <option key={opt.value} value={opt.value}>
              {opt.label}
            </option>
          ))}
        </select>

        {!executor && (
          <>
            <label htmlFor={`stage-model-${stage}`}>Model</label>
            <select id={`stage-model-${stage}`} value={selectedModel} onChange={(e) => setSelectedModel(e.target.value)} disabled={models.length === 0}>
              {models.length === 0 && <option value="">No models available</option>}
              {models.map((model) => (
                <option key={model} value={model}>
                  {model}
                </option>
              ))}
            </select>
          </>
        )}
      </div>

      {loadError && <p className="error">Could not load conversation: {loadError}</p>}

      <div className="chat-history">
        {messages.map((message, index) => (
          <div key={index} className={`chat-message chat-message-${message.role}`}>
            {message.reasoningContent && (
              <details className="thinking-panel" open={!message.thinkingCollapsed}>
                <summary>Thinking</summary>
                <div className="thinking-content">{message.reasoningContent}</div>
              </details>
            )}
            <strong>{message.role}:</strong> <MarkdownMessage content={message.content} />
            {message.toolCallName && <span className="tool-call-chip">Proposed a draft ({message.toolCallName})</span>}
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
          onKeyDown={handleDraftKeyDown}
          placeholder={editingIndex !== null ? 'Editing message...' : 'Reply...'}
          rows={3}
          disabled={sending}
        />
        {editingIndex !== null ? (
          <div className="chat-input-edit-controls">
            <button type="button" className="action-btn-cancel" onClick={handleCancelEdit} disabled={sending}>
              Cancel
            </button>
            <button type="button" onClick={handleSend} disabled={sending || !draft.trim()}>
              {sending ? 'Saving...' : 'Save'}
            </button>
          </div>
        ) : sending ? (
          <button type="button" className="action-btn-stop" onClick={handleStop}>
            Stop
          </button>
        ) : (
          <button type="button" onClick={handleSend} disabled={!draft.trim()}>
            Send
          </button>
        )}
      </div>
      <p className="chat-input-hint">Enter to send &middot; Alt+Enter for a new line</p>

      {pendingDraft && (
        <div className="draft-review">
          <h4>Proposed draft</h4>
          {renderDraft(pendingDraft, setPendingDraft)}
          {finalizeError && <p className="error">{finalizeError}</p>}
          <textarea
            value={requestChangesComment}
            onChange={(e) => setRequestChangesComment(e.target.value)}
            placeholder="What should change? (optional — edit the draft above first)"
            rows={2}
          />
          <div className="stage-actions">
            <button type="button" onClick={handleFinalize} disabled={finalizing}>
              {finalizing ? 'Finalizing...' : 'Finalize'}
            </button>
            <button type="button" onClick={handleRequestChanges} disabled={finalizing || sending}>
              Request changes
            </button>
            <button type="button" onClick={() => setPendingDraft(null)} disabled={finalizing}>
              Discard
            </button>
          </div>
        </div>
      )}
    </div>
  )
}

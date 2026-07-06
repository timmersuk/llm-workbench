import { useEffect, useState } from 'react'
import type { ReactNode } from 'react'
import { getStageConversation, listModels, postStageMessage } from './api'
import type { ConversationMessage } from './types'

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
    error: null,
    thinkingCollapsed: true,
  }
}

interface StageConversationPanelProps<D> {
  projectId: string
  taskId: string
  stage: string
  emptyDraft: D
  renderDraft: (draft: D, onChange: (draft: D) => void) => ReactNode
  onFinalize: (draft: D) => Promise<void>
}

// StageConversationPanel is the mechanism shared by GrillMe and Planning
// Mode (CONTEXT.md): a persisted Conversation transcript, a message input
// that streams the assistant's reply, and — when the model calls its
// registered tool — a Draft, shown via renderDraft for the human to edit
// before Finalize or discard (which just clears local state; no API call).
export function StageConversationPanel<D>({
  projectId,
  taskId,
  stage,
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
  const [pendingDraft, setPendingDraft] = useState<D | null>(null)
  const [finalizing, setFinalizing] = useState(false)
  const [finalizeError, setFinalizeError] = useState<string | null>(null)

  useEffect(() => {
    getStageConversation(projectId, taskId, stage)
      .then((conv) => setMessages((conv.messages ?? []).map(toDisplayMessage)))
      .catch((err) => setLoadError(err instanceof Error ? err.message : String(err)))
    listModels()
      .then((result) => {
        setModels(result.models)
        setSelectedModel((current) => current || result.models[0] || '')
      })
      .catch(() => undefined)
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

  async function handleSend() {
    const text = draft.trim()
    if (!text || sending) {
      return
    }

    setMessages((prev) => [
      ...prev,
      { role: 'user', content: text, reasoningContent: '', toolCallName: null, error: null, thinkingCollapsed: true },
      { role: 'assistant', content: '', reasoningContent: '', toolCallName: null, error: null, thinkingCollapsed: false },
    ])
    setDraft('')
    setSending(true)

    try {
      await postStageMessage(projectId, taskId, stage, text, selectedModel, (event) => {
        if (event.error) {
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
      })
    } catch (err) {
      updateLastMessage((msg) => ({ ...msg, error: err instanceof Error ? err.message : String(err) }))
    } finally {
      setSending(false)
    }
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
      <div className="chat-model-row">
        <label htmlFor={`stage-model-${stage}`}>Model</label>
        <select id={`stage-model-${stage}`} value={selectedModel} onChange={(e) => setSelectedModel(e.target.value)} disabled={models.length === 0}>
          {models.length === 0 && <option value="">No models available</option>}
          {models.map((model) => (
            <option key={model} value={model}>
              {model}
            </option>
          ))}
        </select>
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
            <strong>{message.role}:</strong> {message.content}
            {message.toolCallName && <span className="tool-call-chip">Proposed a draft ({message.toolCallName})</span>}
            {message.error && <p className="error">{message.error}</p>}
          </div>
        ))}
      </div>

      <div className="chat-input">
        <textarea
          value={draft}
          onChange={(e) => setDraft(e.target.value)}
          placeholder="Reply..."
          rows={3}
        />
        <button type="button" onClick={handleSend} disabled={sending || !draft.trim()}>
          {sending ? 'Sending...' : 'Send'}
        </button>
      </div>

      {pendingDraft && (
        <div className="draft-review">
          <h4>Proposed draft</h4>
          {renderDraft(pendingDraft, setPendingDraft)}
          {finalizeError && <p className="error">{finalizeError}</p>}
          <div className="stage-actions">
            <button type="button" onClick={handleFinalize} disabled={finalizing}>
              {finalizing ? 'Finalizing...' : 'Finalize'}
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

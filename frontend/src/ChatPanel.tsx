import { useEffect, useState } from 'react'
import { listModels, streamChatCompletion } from './api'
import type { ChatMessage } from './types'

interface DisplayMessage {
  role: string
  content: string
  reasoningContent: string
  error: string | null
  thinkingCollapsed: boolean
}

export function ChatPanel() {
  const [messages, setMessages] = useState<DisplayMessage[]>([])
  const [draft, setDraft] = useState('')
  const [sending, setSending] = useState(false)
  const [models, setModels] = useState<string[]>([])
  const [selectedModel, setSelectedModel] = useState('')
  const [modelsError, setModelsError] = useState<string | null>(null)

  useEffect(() => {
    listModels()
      .then((result) => {
        setModels(result.models)
        setSelectedModel((current) => current || result.models[0] || '')
      })
      .catch((err) => setModelsError(err instanceof Error ? err.message : String(err)))
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

  async function handleSend() {
    const text = draft.trim()
    if (!text || sending) {
      return
    }

    const history: ChatMessage[] = messages.map((m) => ({ role: m.role, content: m.content }))
    history.push({ role: 'user', content: text })

    setMessages((prev) => [
      ...prev,
      { role: 'user', content: text, reasoningContent: '', error: null, thinkingCollapsed: true },
      { role: 'assistant', content: '', reasoningContent: '', error: null, thinkingCollapsed: false },
    ])
    setDraft('')
    setSending(true)

    try {
      await streamChatCompletion(history, selectedModel, (event) => {
        if (event.error) {
          updateLastMessage((msg) => ({ ...msg, error: event.error! }))
          return
        }
        if (event.reasoning_content) {
          updateLastMessage((msg) => ({ ...msg, reasoningContent: msg.reasoningContent + event.reasoning_content }))
        }
        if (event.content) {
          updateLastMessage((msg) => ({
            ...msg,
            content: msg.content + event.content,
            thinkingCollapsed: true,
          }))
        }
      })
    } catch (err) {
      updateLastMessage((msg) => ({ ...msg, error: err instanceof Error ? err.message : String(err) }))
    } finally {
      setSending(false)
    }
  }

  return (
    <div className="chat-panel">
      <div className="chat-model-row">
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
      </div>
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
            <strong>{message.role}:</strong> {message.content}
            {message.error && <p className="error">{message.error}</p>}
          </div>
        ))}
      </div>
      <div className="chat-input">
        <textarea
          value={draft}
          onChange={(e) => setDraft(e.target.value)}
          placeholder="Message the local LLM..."
          rows={3}
        />
        <button onClick={handleSend} disabled={sending || !draft.trim()}>
          {sending ? 'Sending...' : 'Send'}
        </button>
      </div>
    </div>
  )
}

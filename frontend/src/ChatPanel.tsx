import { useState } from 'react'
import { sendChatMessage } from './api'
import type { ChatMessage } from './types'

export function ChatPanel() {
  const [messages, setMessages] = useState<ChatMessage[]>([])
  const [draft, setDraft] = useState('')
  const [sending, setSending] = useState(false)
  const [error, setError] = useState<string | null>(null)

  async function handleSend() {
    const text = draft.trim()
    if (!text || sending) {
      return
    }

    const nextMessages = [...messages, { role: 'user', content: text }]
    setMessages(nextMessages)
    setDraft('')
    setSending(true)
    setError(null)

    try {
      const resp = await sendChatMessage(nextMessages)
      const reply = resp.choices[0]?.message
      if (reply) {
        setMessages((prev) => [...prev, reply])
      }
    } catch (err) {
      setError(err instanceof Error ? err.message : String(err))
    } finally {
      setSending(false)
    }
  }

  return (
    <div className="chat-panel">
      <div className="chat-history">
        {messages.map((message, index) => (
          <div key={index} className={`chat-message chat-message-${message.role}`}>
            <strong>{message.role}:</strong> {message.content}
          </div>
        ))}
      </div>
      {error && <p className="error">Chat failed: {error}</p>}
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

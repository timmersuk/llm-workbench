import type { KeyboardEvent } from 'react'
import type { LiveTurnStatus } from './useLiveTurnStatus'

interface ChatInputAreaProps {
  draft: string
  onDraftChange: (value: string) => void
  onSend: () => void
  onStop: () => void
  onCancelEdit: () => void
  sending: boolean
  // editingIndex is the position of the user message currently being
  // edited (non-null), or null for a normal new-message reply — callers
  // own the editing state itself; this only decides which control row and
  // placeholder to show.
  editingIndex: number | null
  // canSend is whatever a caller needs true beyond "there's non-empty,
  // non-sending draft text" before Send/Save may be clicked — e.g.
  // ChatPanel requires a healthy executor to be selected; StageConversationPanel
  // has no such extra precondition and always passes true.
  canSend: boolean
  placeholder: string
  liveTurnStatus: LiveTurnStatus
}

// ChatInputArea is the reply box shared by every turn-based conversation
// surface (ChatPanel's free chat, StageConversationPanel's task stages):
// the draft textarea with Enter-to-send/Alt+Enter-for-newline handling, the
// Send/Save/Cancel/Stop control row, and the live turn-status/hint line
// beneath it. Callers own draft/sending/editingIndex state and the actual
// send/stop/cancel actions; this component owns only the input mechanics
// and the two callers' now-identical layout.
export function ChatInputArea({ draft, onDraftChange, onSend, onStop, onCancelEdit, sending, editingIndex, canSend, placeholder, liveTurnStatus }: ChatInputAreaProps) {
  // Enter sends the reply, matching most chat UIs; Alt+Enter inserts a
  // newline instead, for the rare multi-line reply. The newline is spliced
  // in at the cursor explicitly (rather than left to the textarea's default
  // handling) so the cursor lands in the right place regardless of modifier
  // keys, since a plain, unmodified Enter is the one case a browser textarea
  // inserts a newline for by default — Alt+Enter isn't. The textarea is
  // disabled while sending, so this never fires mid-stream.
  function handleKeyDown(e: KeyboardEvent<HTMLTextAreaElement>) {
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
      onDraftChange(next)
      return
    }
    e.preventDefault()
    onSend()
  }

  const sendDisabled = sending || !draft.trim() || !canSend

  return (
    <>
      <div className="chat-input">
        <textarea
          value={draft}
          onChange={(e) => onDraftChange(e.target.value)}
          onKeyDown={handleKeyDown}
          placeholder={editingIndex !== null ? 'Editing message...' : placeholder}
          rows={3}
          disabled={sending}
        />
        {editingIndex !== null ? (
          <div className="chat-input-edit-controls">
            <button type="button" className="action-btn-cancel" onClick={onCancelEdit} disabled={sending}>
              Cancel
            </button>
            <button type="button" onClick={onSend} disabled={sendDisabled}>
              {sending ? 'Saving...' : 'Save'}
            </button>
          </div>
        ) : sending ? (
          <button type="button" className="action-btn-stop" onClick={onStop}>
            Stop
          </button>
        ) : (
          <button type="button" onClick={onSend} disabled={sendDisabled}>
            Send
          </button>
        )}
      </div>
      {sending ? (
        <p className="turn-status" aria-live="polite">
          <span className="turn-status-spinner" aria-hidden="true" />
          {liveTurnStatus.elapsedSeconds}s
          {liveTurnStatus.tokens > 0 && (
            <>
              {' '}&middot; {liveTurnStatus.isEstimate ? '~' : ''}
              {liveTurnStatus.tokens} tokens
            </>
          )}
        </p>
      ) : (
        <p className="chat-input-hint">Enter to send &middot; Alt+Enter for a new line</p>
      )}
    </>
  )
}

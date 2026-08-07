import { useState } from 'react'

// PendingPermission mirrors ChatStreamEvent.permission_request (types.ts) —
// kept as its own small type here rather than importing that one, so this
// hook doesn't couple to the wire event shape, only to the three fields it
// actually needs.
export interface PendingPermission {
  id: string
  name: string
  arguments?: string
}

// usePermissionEscalation manages one in-flight tool-escalation prompt
// (docs/adr/0024): receive records a permission_request event, decide posts
// the human's choice via submit and clears the prompt on success (or surfaces
// an error so the human can retry). Shared by ChatPanel and
// StageConversationPanel, which previously each held an independent,
// byte-identical copy of this exact state/decide logic.
//
// submit is optional — the task-drafts pre-creation chat (StageConversationOps.
// submitPermissionDecision) offers no decision endpoint at all, since its
// backend never wires OnPermissionRequest and so never emits a
// permission_request event in the first place; decide simply no-ops in that
// case, mirroring the guard both original copies had.
export function usePermissionEscalation(submit?: (id: string, allow: boolean) => Promise<void>) {
  const [pending, setPending] = useState<PendingPermission | null>(null)
  const [deciding, setDeciding] = useState(false)
  const [error, setError] = useState<string | null>(null)

  // receive surfaces a new prompt, replacing any stale one — called from a
  // stream handler when a permission_request event arrives.
  function receive(permission: PendingPermission) {
    setError(null)
    setPending(permission)
  }

  async function decide(allow: boolean) {
    if (!pending || deciding || !submit) {
      return
    }
    setDeciding(true)
    setError(null)
    try {
      await submit(pending.id, allow)
      setPending(null)
    } catch (err) {
      setError(err instanceof Error ? err.message : String(err))
    } finally {
      setDeciding(false)
    }
  }

  return { pending, deciding, error, receive, decide }
}

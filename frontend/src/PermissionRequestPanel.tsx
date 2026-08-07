import type { PendingPermission } from './usePermissionEscalation'

interface PermissionRequestPanelProps {
  pending: PendingPermission
  deciding: boolean
  error: string | null
  onDecide: (allow: boolean) => void
}

// PermissionRequestPanel renders a pending tool escalation's Approve/Deny
// control (docs/adr/0024) — shared by ChatPanel and StageConversationPanel,
// which previously each held an independent, byte-identical copy of this
// exact markup. Rendered outside the composer by both callers because it can
// fire mid-turn (sending is still true); the model can see and adapt to a
// denial.
export function PermissionRequestPanel({ pending, deciding, error, onDecide }: PermissionRequestPanelProps) {
  return (
    <div className="permission-request" role="group" aria-label="Tool permission request">
      <p className="permission-request-prompt">
        The agent wants to run <strong>{pending.name}</strong> and needs your approval.
      </p>
      {pending.arguments && <pre className="permission-request-args">{pending.arguments}</pre>}
      <div className="permission-request-actions">
        <button type="button" className="permission-request-approve" onClick={() => onDecide(true)} disabled={deciding}>
          Approve
        </button>
        <button type="button" className="permission-request-deny" onClick={() => onDecide(false)} disabled={deciding}>
          Deny
        </button>
      </div>
      {error && <p className="error">{error}</p>}
    </div>
  )
}

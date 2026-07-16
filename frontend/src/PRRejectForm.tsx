import type { ReviewDecision, ReviewDraft } from './types'

// needs_changes/rejected only — approved is invalid from pr_review (the
// backend guard rejects it; the "Push & Open PR"/"Mark as merged" actions
// already cover the approve-shaped outcomes for this stage).
const DECISIONS: { value: ReviewDecision; label: string }[] = [
  { value: 'needs_changes', label: 'Needs changes' },
  { value: 'rejected', label: 'Rejected' },
]

interface PRRejectFormProps {
  draft: ReviewDraft
  onChange: (draft: ReviewDraft) => void
  onSubmit: () => void
  disabled: boolean
}

// PRRejectForm is pr_review's reject action (docs/milestones/milestone7.md
// PR 3): a human recording that a pushed (or not-yet-pushed) PR needs
// changes or should be rejected outright, posted through the same
// task.FinalizeReview reject path Review itself uses. Deliberately not
// ReviewDraftForm: simpler shape (no propose_review agent draft to
// receive — pr_review has no agent conversation at all) and it owns its
// own submit button, since there's no StageConversationPanel Finalize step
// wrapping it here.
export function PRRejectForm({ draft, onChange, onSubmit, disabled }: PRRejectFormProps) {
  return (
    <div className="draft-form">
      <div className="form-row">
        <label htmlFor="pr-reject-decision">Decision</label>
        <select
          id="pr-reject-decision"
          value={draft.decision}
          onChange={(e) => onChange({ ...draft, decision: e.target.value as ReviewDecision })}
        >
          {DECISIONS.map((d) => (
            <option key={d.value} value={d.value}>
              {d.label}
            </option>
          ))}
        </select>
      </div>
      <div className="form-row">
        <label htmlFor="pr-reject-notes">Notes</label>
        <textarea
          id="pr-reject-notes"
          value={draft.notes}
          onChange={(e) => onChange({ ...draft, notes: e.target.value })}
          rows={4}
        />
      </div>
      <button type="button" disabled={disabled} onClick={onSubmit}>
        Record decision
      </button>
    </div>
  )
}

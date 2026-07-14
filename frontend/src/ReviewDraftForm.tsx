import type { ReviewDecision, ReviewDraft } from './types'

// The three verdicts, in the order a reviewer most often reaches for them:
// approve, ask for changes, or reject outright (which reopens requirements).
const DECISIONS: { value: ReviewDecision; label: string }[] = [
  { value: 'approved', label: 'Approved' },
  { value: 'needs_changes', label: 'Needs changes' },
  { value: 'rejected', label: 'Rejected' },
]

interface ReviewDraftFormProps {
  draft: ReviewDraft
  onChange: (draft: ReviewDraft) => void
}

// Editable form for a Review Draft — the agent proposes a verdict + notes via
// propose_review; the human can change either here before Finalize. Mirrors
// PlanDraftForm/RequirementsDraftForm's shape (a select plus a textarea).
export function ReviewDraftForm({ draft, onChange }: ReviewDraftFormProps) {
  return (
    <div className="draft-form">
      <div className="form-row">
        <label htmlFor="review-decision">Decision</label>
        <select
          id="review-decision"
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
        <label htmlFor="review-notes">Notes</label>
        <textarea
          id="review-notes"
          value={draft.notes}
          onChange={(e) => onChange({ ...draft, notes: e.target.value })}
          rows={5}
        />
      </div>
    </div>
  )
}

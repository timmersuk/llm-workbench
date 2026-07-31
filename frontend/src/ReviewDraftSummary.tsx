import { MarkdownMessage } from './MarkdownMessage'
import type { ReviewDecision, ReviewDraft } from './types'

const DECISION_LABELS: Record<ReviewDecision, string> = {
  approved: 'Approved',
  needs_changes: 'Needs changes',
  rejected: 'Rejected',
}

// ReviewDraftSummary is the read-only counterpart a plain editable form
// would be here: the agent's proposed verdict isn't something the human is
// meant to hand-edit before Finalize — disagreeing means Request Changes
// (which reopens the conversation instead), not silently overriding the
// decision or notes text. StageConversationPanel renders this with
// draftIsEditable={false}.
export function ReviewDraftSummary({ draft }: { draft: ReviewDraft }) {
  return (
    <div className="draft-form">
      <p className="review-draft-decision">
        <strong>Decision:</strong> {DECISION_LABELS[draft.decision]}
      </p>
      {draft.notes && <MarkdownMessage content={draft.notes} />}
    </div>
  )
}

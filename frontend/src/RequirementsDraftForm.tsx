import type { RequirementsDraft } from './types'
import { linesToList, listToLines } from './listFields'

interface RequirementsDraftFormProps {
  draft: RequirementsDraft
  onChange: (draft: RequirementsDraft) => void
}

// Editable form for a GrillMe Draft (CONTEXT.md) — the human can tweak any
// field here before Finalize; nothing here is read-only.
export function RequirementsDraftForm({ draft, onChange }: RequirementsDraftFormProps) {
  return (
    <div className="draft-form">
      <div className="form-row">
        <label htmlFor="draft-objective">Objective</label>
        <textarea
          id="draft-objective"
          value={draft.objective}
          onChange={(e) => onChange({ ...draft, objective: e.target.value })}
          rows={2}
        />
      </div>
      <div className="form-row">
        <label htmlFor="draft-constraints">Constraints (one per line)</label>
        <textarea
          id="draft-constraints"
          value={listToLines(draft.constraints)}
          onChange={(e) => onChange({ ...draft, constraints: linesToList(e.target.value) })}
          rows={3}
        />
      </div>
      <div className="form-row">
        <label htmlFor="draft-assumptions">Assumptions (one per line)</label>
        <textarea
          id="draft-assumptions"
          value={listToLines(draft.assumptions)}
          onChange={(e) => onChange({ ...draft, assumptions: linesToList(e.target.value) })}
          rows={3}
        />
      </div>
      <div className="form-row">
        <label htmlFor="draft-success-criteria">Success criteria (one per line)</label>
        <textarea
          id="draft-success-criteria"
          value={listToLines(draft.success_criteria)}
          onChange={(e) => onChange({ ...draft, success_criteria: linesToList(e.target.value) })}
          rows={3}
        />
      </div>

      <h4>Context</h4>
      <div className="form-row">
        <label htmlFor="draft-context-summary">Summary</label>
        <textarea
          id="draft-context-summary"
          value={draft.context.summary}
          onChange={(e) => onChange({ ...draft, context: { ...draft.context, summary: e.target.value } })}
          rows={2}
        />
      </div>
      <div className="form-row">
        <label htmlFor="draft-context-background">Background</label>
        <textarea
          id="draft-context-background"
          value={draft.context.background}
          onChange={(e) => onChange({ ...draft, context: { ...draft.context, background: e.target.value } })}
          rows={3}
        />
      </div>
      <div className="form-row">
        <label htmlFor="draft-context-files">Files (one per line)</label>
        <textarea
          id="draft-context-files"
          value={listToLines(draft.context.files)}
          onChange={(e) => onChange({ ...draft, context: { ...draft.context, files: linesToList(e.target.value) } })}
          rows={2}
        />
      </div>
      <div className="form-row">
        <label htmlFor="draft-context-detail">Detail</label>
        <textarea
          id="draft-context-detail"
          value={draft.context.detail}
          onChange={(e) => onChange({ ...draft, context: { ...draft.context, detail: e.target.value } })}
          rows={4}
        />
      </div>
      <div className="form-row">
        <label htmlFor="draft-context-verification">Verification (one per line)</label>
        <textarea
          id="draft-context-verification"
          value={listToLines(draft.context.verification)}
          onChange={(e) =>
            onChange({ ...draft, context: { ...draft.context, verification: linesToList(e.target.value) } })
          }
          rows={2}
        />
      </div>
      <div className="form-row">
        <label htmlFor="draft-context-open-questions">Open questions (one per line)</label>
        <textarea
          id="draft-context-open-questions"
          value={listToLines(draft.context.open_questions)}
          onChange={(e) =>
            onChange({ ...draft, context: { ...draft.context, open_questions: linesToList(e.target.value) } })
          }
          rows={2}
        />
      </div>
    </div>
  )
}

import type { TaskPlan } from './types'
import { linesToList, listToLines } from './listFields'

interface PlanDraftFormProps {
  draft: TaskPlan
  onChange: (draft: TaskPlan) => void
}

// Editable form for a Planning Mode Draft (CONTEXT.md) — the human can
// tweak any field here before Finalize.
export function PlanDraftForm({ draft, onChange }: PlanDraftFormProps) {
  return (
    <div className="draft-form">
      <div className="form-row">
        <label htmlFor="plan-approach">Approach</label>
        <textarea
          id="plan-approach"
          value={draft.approach}
          onChange={(e) => onChange({ ...draft, approach: e.target.value })}
          rows={3}
        />
      </div>
      <div className="form-row">
        <label htmlFor="plan-steps">Steps (one per line)</label>
        <textarea
          id="plan-steps"
          value={listToLines(draft.steps)}
          onChange={(e) => onChange({ ...draft, steps: linesToList(e.target.value) })}
          rows={5}
        />
      </div>
      <div className="form-row">
        <label htmlFor="plan-risks">Risks (one per line)</label>
        <textarea
          id="plan-risks"
          value={listToLines(draft.risks)}
          onChange={(e) => onChange({ ...draft, risks: linesToList(e.target.value) })}
          rows={3}
        />
      </div>
      <div className="form-row">
        <label htmlFor="plan-complexity">Estimated complexity</label>
        <select
          id="plan-complexity"
          value={draft.estimated_complexity}
          onChange={(e) => onChange({ ...draft, estimated_complexity: e.target.value as TaskPlan['estimated_complexity'] })}
        >
          <option value="">(unset)</option>
          <option value="low">Low</option>
          <option value="medium">Medium</option>
          <option value="high">High</option>
        </select>
      </div>
      <div className="form-row">
        <label htmlFor="plan-executor">Recommended executor</label>
        <input
          id="plan-executor"
          type="text"
          value={draft.recommended_executor}
          onChange={(e) => onChange({ ...draft, recommended_executor: e.target.value })}
        />
      </div>
    </div>
  )
}

import type { TaskDraft } from './types'
import { linesToList, listToLines } from './listFields'

interface TaskDraftFormProps {
  draft: TaskDraft
  onChange: (draft: TaskDraft) => void
}

// Editable form for a propose_task Draft (internal/drafttool/drafttool.go) —
// the human can tweak any field, including id/title, before confirming and
// having createProjectTask actually called (NewTaskPanel.tsx). Unlike every
// other Draft form in this codebase (RequirementsDraftForm/PlanDraftForm/
// ReviewDraftForm/KnowledgeDraftForm), this one edits id/title too: those
// fields belong to task.yaml itself, but every other Draft is proposed for
// an already-created task, whereas this one runs before the task exists at
// all — so id/title are exactly the kind of thing a human is expected to
// review or override here, not fields set some other way beforehand.
export function TaskDraftForm({ draft, onChange }: TaskDraftFormProps) {
  return (
    <div className="draft-form">
      <div className="form-row">
        <label htmlFor="task-draft-id">ID</label>
        <input
          id="task-draft-id"
          type="text"
          value={draft.id}
          onChange={(e) => onChange({ ...draft, id: e.target.value })}
        />
      </div>
      <div className="form-row">
        <label htmlFor="task-draft-title">Title</label>
        <input
          id="task-draft-title"
          type="text"
          value={draft.title}
          onChange={(e) => onChange({ ...draft, title: e.target.value })}
        />
      </div>
      <div className="form-row">
        <label htmlFor="task-draft-objective">Objective</label>
        <textarea
          id="task-draft-objective"
          value={draft.objective}
          onChange={(e) => onChange({ ...draft, objective: e.target.value })}
          rows={2}
        />
      </div>
      <div className="form-row">
        <label htmlFor="task-draft-constraints">Constraints (one per line)</label>
        <textarea
          id="task-draft-constraints"
          value={listToLines(draft.constraints)}
          onChange={(e) => onChange({ ...draft, constraints: linesToList(e.target.value) })}
          rows={3}
        />
      </div>
      <div className="form-row">
        <label htmlFor="task-draft-assumptions">Assumptions (one per line)</label>
        <textarea
          id="task-draft-assumptions"
          value={listToLines(draft.assumptions)}
          onChange={(e) => onChange({ ...draft, assumptions: linesToList(e.target.value) })}
          rows={3}
        />
      </div>
      <div className="form-row">
        <label htmlFor="task-draft-success-criteria">Success criteria (one per line)</label>
        <textarea
          id="task-draft-success-criteria"
          value={listToLines(draft.success_criteria)}
          onChange={(e) => onChange({ ...draft, success_criteria: linesToList(e.target.value) })}
          rows={3}
        />
      </div>
      <div className="form-row">
        <label htmlFor="task-draft-ref-knowledge">Referenced knowledge (one per line)</label>
        <textarea
          id="task-draft-ref-knowledge"
          value={listToLines(draft.references.knowledge)}
          onChange={(e) => onChange({ ...draft, references: { ...draft.references, knowledge: linesToList(e.target.value) } })}
          rows={2}
        />
      </div>
      <div className="form-row">
        <label htmlFor="task-draft-ref-repo">Referenced repos (one per line)</label>
        <textarea
          id="task-draft-ref-repo"
          value={listToLines(draft.references.repo)}
          onChange={(e) => onChange({ ...draft, references: { ...draft.references, repo: linesToList(e.target.value) } })}
          rows={2}
        />
      </div>
    </div>
  )
}

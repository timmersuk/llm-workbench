import type { ContextFile, RequirementsDraft, VerificationKind, VerificationStep } from './types'
import { linesToList, listToLines } from './listFields'

const VERIFICATION_KINDS: { value: VerificationKind; label: string }[] = [
  { value: 'agent_executable', label: 'Agent-executable' },
  { value: 'human_judgment', label: 'Human judgment' },
]

interface RequirementsDraftFormProps {
  draft: RequirementsDraft
  onChange: (draft: RequirementsDraft) => void
}

// Editable form for a GrillMe Draft (CONTEXT.md) — the human can tweak any
// field here before Finalize; nothing here is read-only.
export function RequirementsDraftForm({ draft, onChange }: RequirementsDraftFormProps) {
  const verification = draft.context.verification

  const setVerification = (steps: VerificationStep[]) =>
    onChange({ ...draft, context: { ...draft.context, verification: steps } })

  const updateStep = (index: number, patch: Partial<VerificationStep>) =>
    setVerification(verification.map((step, i) => (i === index ? { ...step, ...patch } : step)))

  const removeStep = (index: number) =>
    setVerification(verification.filter((_, i) => i !== index))

  const addStep = () =>
    setVerification([...verification, { description: '', kind: 'agent_executable' }])

  const files = draft.context.files

  const setFiles = (files: ContextFile[]) => onChange({ ...draft, context: { ...draft.context, files } })

  const updateFile = (index: number, patch: Partial<ContextFile>) =>
    setFiles(files.map((file, i) => (i === index ? { ...file, ...patch } : file)))

  const removeFile = (index: number) => setFiles(files.filter((_, i) => i !== index))

  const addFile = () => setFiles([...files, { path: '', role: '' }])

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
        <span className="form-label">Files</span>
        <div className="verification-steps">
          {files.length === 0 && <p className="verification-empty">No files yet.</p>}
          {files.map((file, index) => (
            <div className="verification-step" key={index}>
              <input
                type="text"
                aria-label={`File ${index + 1} path`}
                placeholder="path"
                value={file.path}
                onChange={(e) => updateFile(index, { path: e.target.value })}
              />
              <input
                type="text"
                aria-label={`File ${index + 1} role`}
                placeholder="role (optional)"
                value={file.role}
                onChange={(e) => updateFile(index, { role: e.target.value })}
              />
              <button type="button" aria-label={`Remove file ${index + 1}`} onClick={() => removeFile(index)}>
                Remove
              </button>
            </div>
          ))}
          <button type="button" onClick={addFile}>
            Add file
          </button>
        </div>
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
        <span className="form-label">Verification steps</span>
        <div className="verification-steps">
          {verification.length === 0 && <p className="verification-empty">No verification steps yet.</p>}
          {verification.map((step, index) => (
            <div className="verification-step" key={index}>
              <input
                type="text"
                aria-label={`Verification step ${index + 1} description`}
                value={step.description}
                onChange={(e) => updateStep(index, { description: e.target.value })}
              />
              <select
                aria-label={`Verification step ${index + 1} kind`}
                value={step.kind}
                onChange={(e) => updateStep(index, { kind: e.target.value as VerificationKind })}
              >
                {VERIFICATION_KINDS.map((k) => (
                  <option key={k.value} value={k.value}>
                    {k.label}
                  </option>
                ))}
              </select>
              <button
                type="button"
                aria-label={`Remove verification step ${index + 1}`}
                onClick={() => removeStep(index)}
              >
                Remove
              </button>
            </div>
          ))}
          <button type="button" onClick={addStep}>
            Add verification step
          </button>
        </div>
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

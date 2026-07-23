import { finalizeRequirements } from './api'
import { RequirementsDraftForm } from './RequirementsDraftForm'
import { StageConversationPanel } from './StageConversationPanel'
import type { RequirementsDraft, Task, TaskContext } from './types'

const EMPTY_DRAFT: RequirementsDraft = {
  objective: '',
  constraints: [],
  assumptions: [],
  success_criteria: [],
  context: {
    summary: '',
    background: '',
    files: [],
    detail: '',
    verification: [],
    open_questions: [],
  },
}

interface GrillMePanelProps {
  projectId: string
  taskId: string
  onFinalized: (task: Task, context: TaskContext) => void
}

// normalizeRequirementsDraft repairs context.files entries a model
// proposed as {path, role} objects instead of the plain path strings the
// propose_context tool schema (drafttool.go's proposeContextSchema) and
// task.Context.Files ([]string) both require — observed from a Claude Code
// executor turn despite the schema, and otherwise silently corrupts the
// Files field in the form and 400s on Finalize (Go's json.Decode rejects
// the whole body when a string field gets an object instead).
function normalizeRequirementsDraft(draft: RequirementsDraft): RequirementsDraft {
  const files = draft.context.files.map((file) =>
    typeof file === 'string' ? file : String((file as { path?: unknown })?.path ?? file),
  )
  return { ...draft, context: { ...draft.context, files } }
}

// GrillMePanel is the requirements-stage interview (CONTEXT.md's "GrillMe"):
// StageConversationPanel scoped to stage "requirements", proposing/editing
// a RequirementsDraft, Finalizing into both task.yaml's requirements
// fields and context.yaml.
export function GrillMePanel({ projectId, taskId, onFinalized }: GrillMePanelProps) {
  return (
    <StageConversationPanel<RequirementsDraft>
      projectId={projectId}
      taskId={taskId}
      stage="requirements"
      title="GrillMe"
      description="Reply below to answer GrillMe's questions — its proposal will become this task's requirements once you finalize it."
      emptyDraft={EMPTY_DRAFT}
      renderDraft={(draft, onChange) => <RequirementsDraftForm draft={draft} onChange={onChange} />}
      normalizeDraft={normalizeRequirementsDraft}
      autoStart={false}
      startLabel="Start GrillMe"
      onFinalize={async (draft) => {
        const result = await finalizeRequirements(projectId, taskId, draft)
        onFinalized(result.task, result.context)
      }}
    />
  )
}

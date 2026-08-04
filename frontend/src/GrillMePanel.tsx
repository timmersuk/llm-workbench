import { finalizeRequirements } from './api'
import { RequirementsDraftForm } from './RequirementsDraftForm'
import { stageConversationOps } from './stageConversationOps'
import { StageConversationPanel } from './StageConversationPanel'
import type { AgentSelection, ContextFile, RequirementsDraft, Task, TaskContext } from './types'

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
  defaultSelection?: AgentSelection
}

// normalizeRequirementsDraft upgrades context.files entries a model
// proposed as bare path strings into the {path, role} objects the
// propose_context tool schema (drafttool.go's proposeContextSchema) and
// task.Context.Files ([]ContextFile) both require as of docs/adr/0021 —
// some executors still emit a plain string despite the schema, which
// otherwise renders wrong in the form and 400s on Finalize (Go's
// json.Decode rejects the whole body when an object field gets a string
// instead).
function normalizeRequirementsDraft(draft: RequirementsDraft): RequirementsDraft {
  const files = draft.context.files.map((file) =>
    typeof file === 'string' ? { path: file, role: '' } : (file as ContextFile),
  )
  return { ...draft, context: { ...draft.context, files } }
}

// GrillMePanel is the requirements-stage interview (CONTEXT.md's "GrillMe"):
// StageConversationPanel scoped to stage "requirements", proposing/editing
// a RequirementsDraft, Finalizing into both task.yaml's requirements
// fields and context.yaml.
export function GrillMePanel({ projectId, taskId, onFinalized, defaultSelection }: GrillMePanelProps) {
  return (
    <StageConversationPanel<RequirementsDraft>
      conversationKey={`${projectId}:${taskId}:requirements`}
      ops={stageConversationOps(projectId, taskId, 'requirements')}
      title="GrillMe"
      description="Reply below to answer GrillMe's questions — its proposal will become this task's requirements once you finalize it."
      emptyDraft={EMPTY_DRAFT}
      renderDraft={(draft, onChange) => <RequirementsDraftForm draft={draft} onChange={onChange} />}
      normalizeDraft={normalizeRequirementsDraft}
      autoStart={false}
      startLabel="Start GrillMe"
      defaultSelection={defaultSelection}
      onFinalize={async (draft) => {
        const result = await finalizeRequirements(projectId, taskId, draft)
        onFinalized(result.task, result.context)
      }}
    />
  )
}

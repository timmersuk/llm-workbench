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
      emptyDraft={EMPTY_DRAFT}
      renderDraft={(draft, onChange) => <RequirementsDraftForm draft={draft} onChange={onChange} />}
      onFinalize={async (draft) => {
        const result = await finalizeRequirements(projectId, taskId, draft)
        onFinalized(result.task, result.context)
      }}
    />
  )
}

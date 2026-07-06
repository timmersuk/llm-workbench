import { finalizePlan } from './api'
import { PlanDraftForm } from './PlanDraftForm'
import { StageConversationPanel } from './StageConversationPanel'
import type { Task, TaskPlan } from './types'

const EMPTY_DRAFT: TaskPlan = {
  approach: '',
  steps: [],
  risks: [],
  estimated_complexity: '',
  recommended_executor: '',
}

interface PlanningModePanelProps {
  projectId: string
  taskId: string
  onFinalized: (task: Task, plan: TaskPlan) => void
}

// PlanningModePanel is the planning-stage interview (CONTEXT.md's
// "Planning Mode"): StageConversationPanel scoped to stage "planning",
// proposing/editing a TaskPlan, Finalizing into plan.yaml.
export function PlanningModePanel({ projectId, taskId, onFinalized }: PlanningModePanelProps) {
  return (
    <StageConversationPanel<TaskPlan>
      projectId={projectId}
      taskId={taskId}
      stage="planning"
      emptyDraft={EMPTY_DRAFT}
      renderDraft={(draft, onChange) => <PlanDraftForm draft={draft} onChange={onChange} />}
      onFinalize={async (draft) => {
        const result = await finalizePlan(projectId, taskId, draft)
        onFinalized(result.task, result.plan)
      }}
    />
  )
}

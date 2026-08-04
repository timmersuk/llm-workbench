import { finalizePlan } from './api'
import { PlanDraftForm } from './PlanDraftForm'
import { stageConversationOps } from './stageConversationOps'
import { StageConversationPanel } from './StageConversationPanel'
import type { AgentSelection, Task, TaskPlan } from './types'

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
  defaultSelection?: AgentSelection
}

// PlanningModePanel is the planning-stage interview (CONTEXT.md's
// "Planning Mode"): StageConversationPanel scoped to stage "planning",
// proposing/editing a TaskPlan, Finalizing into plan.yaml.
export function PlanningModePanel({ projectId, taskId, onFinalized, defaultSelection }: PlanningModePanelProps) {
  return (
    <StageConversationPanel<TaskPlan>
      conversationKey={`${projectId}:${taskId}:planning`}
      ops={stageConversationOps(projectId, taskId, 'planning')}
      title="Planning Mode"
      description="Reply below to answer Planning Mode's questions — its proposal will become this task's execution plan once you finalize it."
      emptyDraft={EMPTY_DRAFT}
      renderDraft={(draft, onChange) => <PlanDraftForm draft={draft} onChange={onChange} />}
      autoStart={false}
      startLabel="Start Planning"
      defaultSelection={defaultSelection}
      onFinalize={async (draft) => {
        const result = await finalizePlan(projectId, taskId, draft)
        onFinalized(result.task, result.plan)
      }}
    />
  )
}

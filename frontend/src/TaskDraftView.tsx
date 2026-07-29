import { taskDraftConversationOps } from './stageConversationOps'
import { StageConversationPanel } from './StageConversationPanel'
import type { Task } from './types'

interface TaskDraftViewProps {
  projectId: string
  task: Task
  onBack: () => void
}

// TaskDraftView is the read-only /projects/:projectId/tasks/:taskId/draft
// screen: the frozen pre-creation "New Task" conversation that produced
// task (task.draft_session_id), shown after the fact. Read-only because
// this conversation's own life ended the moment the task was created — a
// task's live requirements conversation is GrillMe's own, separate one
// (TaskDetailPanel's GrillMePanel), which folds this transcript in as a
// system-prompt addendum rather than continuing it (buildTaskDraftContext,
// internal/api/stage_conversation.go); reopening this view must not be able
// to mutate a conversation nothing reads live anymore. Renders null when
// the task has no draft_session_id — TaskDetailPanel only ever links here
// when one is set, but this guards against a stale/direct URL visit too.
export function TaskDraftView({ projectId, task, onBack }: TaskDraftViewProps) {
  if (!task.draft_session_id) {
    return null
  }
  const sessionId = task.draft_session_id

  return (
    <div className="task-draft-view">
      <button type="button" className="back-link" onClick={onBack}>
        &larr; Back to task
      </button>
      <StageConversationPanel<Record<string, never>>
        conversationKey={`${projectId}:${sessionId}:readonly`}
        ops={taskDraftConversationOps(projectId, sessionId)}
        readOnly
        title="Pre-creation conversation"
        description="The chat that produced this task's initial draft, before it was created — read-only."
        emptyDraft={{}}
        renderDraft={() => null}
        onFinalize={async () => undefined}
      />
    </div>
  )
}

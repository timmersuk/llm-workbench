import { createProjectTask } from './api'
import { taskDraftConversationOps } from './stageConversationOps'
import { StageConversationPanel } from './StageConversationPanel'
import { TaskDraftForm } from './TaskDraftForm'
import type { Task, TaskDraft } from './types'

const EMPTY_DRAFT: TaskDraft = {
  id: '',
  title: '',
  objective: '',
  constraints: [],
  assumptions: [],
  success_criteria: [],
  references: { knowledge: [], repo: [] },
}

interface NewTaskPanelProps {
  projectId: string
  sessionId: string
  onCreated: (task: Task) => void
}

// NewTaskPanel is the chat-driven "New Task" entry point that replaced the
// plain TaskForm.tsx: StageConversationPanel scoped to a task-drafts
// session, proposing/editing a TaskDraft via propose_task
// (internal/drafttool), confirmed into the existing Create Task API rather
// than a form-submit. sessionId is minted by the caller (ProjectDetailPanel)
// the instant "New Task" is clicked and pushed into the URL immediately —
// before any message is sent — so a reload mid-conversation lands back on
// this same session rather than losing it.
//
// Confirming the draft (StageConversationPanel's Finalize) calls
// createProjectTask carrying draft_session_id, then hands off to the new
// task's own view via onCreated — the task lands at stage: requirements
// (server-defaulted), so the caller renders straight into GrillMe, whose
// first turn folds this conversation's transcript into its system prompt
// as an addendum (buildTaskDraftContext, internal/api/stage_conversation.go)
// rather than re-interviewing from scratch.
export function NewTaskPanel({ projectId, sessionId, onCreated }: NewTaskPanelProps) {
  return (
    <StageConversationPanel<TaskDraft>
      conversationKey={`${projectId}:${sessionId}`}
      ops={taskDraftConversationOps(projectId, sessionId)}
      title="New Task"
      description="Describe what you want done. Once the draft below looks right, confirm it to create the task and move on to GrillMe."
      emptyDraft={EMPTY_DRAFT}
      renderDraft={(draft, onChange) => <TaskDraftForm draft={draft} onChange={onChange} />}
      autoStart={false}
      startLabel="Start"
      onFinalize={async (draft) => {
        const created = await createProjectTask(projectId, {
          id: draft.id,
          title: draft.title,
          references: draft.references,
          objective: draft.objective,
          constraints: draft.constraints,
          assumptions: draft.assumptions,
          success_criteria: draft.success_criteria,
          draft_session_id: sessionId,
        })
        onCreated(created)
      }}
    />
  )
}


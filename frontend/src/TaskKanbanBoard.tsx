import type { Task, TaskStage } from './types'

const STAGES: TaskStage[] = ['requirements', 'planning', 'implementation', 'review', 'pr_review', 'merged']

const STAGE_LABELS: Record<TaskStage, string> = {
  requirements: 'Requirements',
  planning: 'Planning',
  implementation: 'Implementation',
  review: 'Review',
  pr_review: 'PR Review',
  merged: 'Merged',
}

interface TaskKanbanBoardProps {
  tasks: Task[]
  onSelect: (task: Task) => void
}

// A basic kanban view: one column per stage, lightweight navigational
// cards only (no per-card actions) — clicking a card opens TaskDetailPanel,
// where GrillMe/Planning Mode/Finalize/Revise actually live. The only task
// view on ProjectDetailPanel.
export function TaskKanbanBoard({ tasks, onSelect }: TaskKanbanBoardProps) {
  return (
    <div className="kanban-board">
      {STAGES.map((stage) => (
        <div key={stage} className="kanban-column">
          <h4>{STAGE_LABELS[stage]}</h4>
          {tasks
            .filter((task) => task.stage === stage)
            .map((task) => (
              <button key={task.id} type="button" className="kanban-card" onClick={() => onSelect(task)}>
                <strong>{task.title || task.id}</strong>
                <span className="kanban-card-meta">
                  {task.id} &middot; {task.status}
                </span>
              </button>
            ))}
        </div>
      ))}
    </div>
  )
}

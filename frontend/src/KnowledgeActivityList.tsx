import { knowledgeActivityLabel } from './knowledgeActivity'
import type { KnowledgeActivityEntry } from './types'

interface KnowledgeActivityListProps {
  entries: KnowledgeActivityEntry[]
}

// KnowledgeActivityList renders a task's Task.knowledge_activity as its own
// focused list — TaskDetailPanel's dedicated "Knowledge" section, distinct
// from (and in addition to) the same entries' appearance inline in
// TimelinePanel's merged chronological history: the Timeline shows where a
// decision fell relative to the task's stage transitions/review verdicts,
// this shows every knowledge outcome in one place without having to hunt
// through that merged list for it.
export function KnowledgeActivityList({ entries }: KnowledgeActivityListProps) {
  return (
    <ul className="knowledge-activity-list">
      {entries.map((entry, i) => (
        <li key={`${entry.concept_id}-${entry.created_at}-${i}`} className={`knowledge-activity-${entry.action}`}>
          {knowledgeActivityLabel(entry)}
          {entry.type && <span className="knowledge-activity-type"> ({entry.type})</span>}
        </li>
      ))}
    </ul>
  )
}

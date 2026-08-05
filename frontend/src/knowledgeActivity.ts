import type { KnowledgeActivityEntry } from './types'

// knowledgeActivityLabel renders one KnowledgeActivityEntry's action as a
// short, human-readable phrase — shared by TimelinePanel (one line among
// transitions/reviews) and KnowledgeActivityList (the task's dedicated
// Knowledge section), so the same decision reads identically in both
// places.
export function knowledgeActivityLabel(entry: KnowledgeActivityEntry): string {
  switch (entry.action) {
    case 'created':
      return `knowledge concept created: ${entry.concept_id}`
    case 'updated':
      return `knowledge concept updated: ${entry.concept_id}`
    case 'rejected':
      return `knowledge concept proposal rejected: ${entry.concept_id}`
    default:
      return `knowledge concept ${entry.action}: ${entry.concept_id}`
  }
}

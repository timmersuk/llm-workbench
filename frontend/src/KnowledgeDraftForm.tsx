import type { KnowledgeConceptDraft } from './types'

// tagsToText/textToTags round-trip frontmatter.tags (a string[], per OKF)
// through a single comma-separated input — the same "one field per common
// case" tradeoff title/description below make, rather than a nested
// tag-editor widget for what's usually a handful of short words.
function tagsToText(tags: unknown): string {
  if (!Array.isArray(tags)) {
    return ''
  }
  return tags.filter((t): t is string => typeof t === 'string').join(', ')
}

function textToTags(text: string): string[] {
  return text
    .split(',')
    .map((t) => t.trim())
    .filter(Boolean)
}

interface KnowledgeDraftFormProps {
  draft: KnowledgeConceptDraft
  onChange: (draft: KnowledgeConceptDraft) => void
}

// Editable form for a propose_knowledge Draft — the agent proposes a
// concept's full content (concept_id, type, frontmatter, body); the human
// can correct any of it here before accepting. Mirrors ReviewDraftForm's
// shape (a handful of fields plus a body textarea), except frontmatter is
// an open bag per OKF (docs/knowledge schema v0.md §2: "any other
// producer-defined fields") rather than a closed schema — this exposes
// only the three most common fields (title/description/tags) as their own
// inputs and preserves every other frontmatter key untouched rather than
// requiring a raw-JSON textarea for the common case.
export function KnowledgeDraftForm({ draft, onChange }: KnowledgeDraftFormProps) {
  const frontmatter = draft.frontmatter ?? {}
  const title = typeof frontmatter.title === 'string' ? frontmatter.title : ''
  const description = typeof frontmatter.description === 'string' ? frontmatter.description : ''
  const tagsText = tagsToText(frontmatter.tags)

  function updateFrontmatter(patch: Record<string, unknown>) {
    onChange({ ...draft, frontmatter: { ...frontmatter, ...patch } })
  }

  return (
    <div className="draft-form">
      <div className="form-row">
        <label htmlFor="knowledge-concept-id">Concept ID</label>
        <input
          id="knowledge-concept-id"
          type="text"
          value={draft.concept_id}
          onChange={(e) => onChange({ ...draft, concept_id: e.target.value })}
        />
      </div>
      <div className="form-row">
        <label htmlFor="knowledge-type">Type</label>
        <input id="knowledge-type" type="text" value={draft.type} onChange={(e) => onChange({ ...draft, type: e.target.value })} />
      </div>
      <div className="form-row">
        <label htmlFor="knowledge-title">Title</label>
        <input id="knowledge-title" type="text" value={title} onChange={(e) => updateFrontmatter({ title: e.target.value })} />
      </div>
      <div className="form-row">
        <label htmlFor="knowledge-description">Description</label>
        <input
          id="knowledge-description"
          type="text"
          value={description}
          onChange={(e) => updateFrontmatter({ description: e.target.value })}
        />
      </div>
      <div className="form-row">
        <label htmlFor="knowledge-tags">Tags (comma-separated)</label>
        <input id="knowledge-tags" type="text" value={tagsText} onChange={(e) => updateFrontmatter({ tags: textToTags(e.target.value) })} />
      </div>
      <div className="form-row">
        <label htmlFor="knowledge-body">Body (markdown)</label>
        <textarea id="knowledge-body" value={draft.body} onChange={(e) => onChange({ ...draft, body: e.target.value })} rows={10} />
      </div>
    </div>
  )
}

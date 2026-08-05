package task

import "time"

// AppendKnowledgeActivity appends entry (with CreatedAt server-stamped,
// ignoring any caller-supplied value) to id's KnowledgeActivity log and
// persists task.yaml. Unlike every lifecycle.go action, this never touches
// Stage — a knowledge concept's accept/reject decision is independent of
// the Review conversation's own propose_review verdict (handleFinalizeKnowledge's
// doc comment) — so there is no stage guard here either; the API layer
// already restricts the call to a task currently at StageReview.
func (s *FileStore) AppendKnowledgeActivity(projectID, id string, entry KnowledgeActivityEntry) (Task, error) {
	t, err := s.Get(projectID, id)
	if err != nil {
		return Task{}, err
	}

	entry.CreatedAt = time.Now().UTC()
	t.KnowledgeActivity = append(t.KnowledgeActivity, entry)
	t.UpdatedAt = time.Now().UTC()

	if err := s.writeTask(projectID, t); err != nil {
		return Task{}, err
	}
	return t, nil
}

package api

import (
	"net/http"

	"github.com/timmersuk/llm-workbench/internal/task"
)

// handleListStageTransitions returns every recorded stage transition for a
// task, oldest first — a file-for-file mirror of handleListReviews.
// TimelinePanel.tsx merges this with the full reviews list to show the
// task's real path through its lifecycle, including reversals, rather than
// only its current stage.
func (s *Server) handleListStageTransitions() http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		projectId := r.PathValue("projectId")
		store, ok := s.resolveTaskStore(w, projectId)
		if !ok {
			return
		}

		transitions, err := store.ListStageTransitions(projectId, r.PathValue("taskId"))
		if err != nil {
			writeGetError(w, err)
			return
		}
		writeJSON(w, http.StatusOK, map[string][]task.StageTransition{"stage_transitions": transitions})
	}
}

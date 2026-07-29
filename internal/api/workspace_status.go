package api

import (
	"errors"
	"fmt"
	"net/http"

	"github.com/timmersuk/llm-workbench/internal/agentrunner"
)

// workspaceStatusResponse is the wire shape for handleWorkspaceStatus —
// live git-derived advisory status for a project's shared checkout,
// deliberately kept separate from GET /api/v1/projects/{id}
// (project.Project's stored, persisted config): this is ephemeral state
// re-derived on every call, not a field written to project.yaml.
type workspaceStatusResponse struct {
	RepositoryConfigured bool                        `json:"repository_configured"`
	Status               agentrunner.WorkspaceStatus `json:"status"`
}

// handleWorkspaceStatus reports the project's shared checkout's current
// advisory hygiene status (docs/milestones/done/milestone8a.md's "Advisory
// checks") — both signals are advisory only, so this handler is tolerant
// by design: a project with no configured repository, or one whose
// checkout hasn't been cloned yet, is not an error here, it's just
// repository_configured: false (no repo) or both checks Known: false (no
// checkout) — the same tolerant posture resolveStageRun already takes for
// ErrNoRepository (stage_conversation.go). Critically, unlike
// resolveStageRun's ResolveWorkspace call, GetWorkspaceStatus never
// triggers a lazy clone: opening this view is never itself the trigger
// for a first, potentially multi-minute, clone.
func (s *Server) handleWorkspaceStatus() http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		proj, err := s.Projects.Get(r.PathValue("projectId"))
		if err != nil {
			writeGetError(w, err)
			return
		}

		status, err := agentrunner.GetWorkspaceStatus(r.Context(), s.ReposRoot, proj.Repositories)
		if err != nil {
			if errors.Is(err, agentrunner.ErrNoRepository) {
				writeJSON(w, http.StatusOK, workspaceStatusResponse{})
				return
			}
			writeAPIError(w, http.StatusInternalServerError, fmt.Sprintf("resolving workspace status: %v", err))
			return
		}
		writeJSON(w, http.StatusOK, workspaceStatusResponse{RepositoryConfigured: true, Status: status})
	}
}

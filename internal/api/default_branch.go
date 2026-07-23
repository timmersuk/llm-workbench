package api

import (
	"context"
	"fmt"

	"github.com/timmersuk/llm-workbench/internal/agentrunner"
	"github.com/timmersuk/llm-workbench/internal/project"
)

// ensureDefaultBranch returns proj's known default branch, determining and
// persisting it via s.DefaultBranchResolver if it has never been set. This
// is the lazy backfill docs/milestones/done/milestone8a.md calls for: it
// runs regardless of how the shared checkout came to exist (freshly
// auto-cloned, or already checked out before this feature existed), so
// already-checked-out projects like llm-workbench and agent-shell get
// backfilled the same way a brand-new one would, the first time either
// hits this path.
//
// Fails closed: if resolver.Determine fails (gh not installed, not
// authenticated, rate-limited, or a non-GitHub host), the error propagates
// rather than letting a caller proceed with an empty/unknown default
// branch — agentrunner.ResolveExecutionWorkspace/ResolveReviewWorkspace
// would refuse anyway (checkDefaultBranch treats "" as always-block), but
// failing here gives a clearer error than a generic ErrWrongBranch would.
func (s *Server) ensureDefaultBranch(ctx context.Context, proj project.Project) (string, error) {
	if proj.DefaultBranch != "" {
		return proj.DefaultBranch, nil
	}
	if len(proj.Repositories) == 0 {
		return "", agentrunner.ErrNoRepository
	}

	branch, err := s.DefaultBranchResolver.Determine(ctx, proj.Repositories[0])
	if err != nil {
		return "", err
	}

	if _, err := s.Projects.Update(proj.ID, project.UpdateInput{
		Name:          proj.Name,
		Description:   proj.Description,
		Repositories:  proj.Repositories,
		DefaultBranch: branch,
		Knowledge:     proj.Knowledge,
		Constraints:   proj.Constraints,
	}); err != nil {
		return "", fmt.Errorf("persisting default branch for project %s: %w", proj.ID, err)
	}

	return branch, nil
}

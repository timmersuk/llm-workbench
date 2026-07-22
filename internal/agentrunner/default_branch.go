package agentrunner

import (
	"context"
	"errors"
	"fmt"
	"strings"
)

// ErrDefaultBranchUnknown is returned when a repository's default branch
// can't be determined — gh not installed, not authenticated, rate-limited,
// or a non-GitHub host. Per docs/milestones/milestone8a.md, callers fail
// closed on this: Execute/Review must not proceed without a known default
// branch to check the wrong-branch gate against.
var ErrDefaultBranchUnknown = errors.New("default branch could not be determined")

// DefaultBranchResolver determines a repository's default branch —
// mirroring GitHubPRClient's shape (pr.go): a real implementation shelling
// out to `gh` in production, a fake in tests. The determine-and-persist
// orchestration (checking Project.DefaultBranch first, calling this only
// when unset, then writing the result back) lives in internal/api, which
// is where a Project is actually read and written; this interface is only
// the git/gh mechanics.
type DefaultBranchResolver interface {
	// Determine looks up repository's (e.g.
	// "github.com/timmersuk/llm-workbench") default branch.
	Determine(ctx context.Context, repository string) (string, error)
}

// NewDefaultBranchResolver returns the real DefaultBranchResolver, shelling
// out to the `gh` CLI — consistent with, not a new dependency beyond,
// GitHubPRClient's existing `gh` usage for PR operations. Relies entirely
// on ambient `gh auth`, same as GitHubPRClient — never stores, requests, or
// handles a credential itself.
func NewDefaultBranchResolver() DefaultBranchResolver {
	return realDefaultBranchResolver{}
}

type realDefaultBranchResolver struct{}

func (realDefaultBranchResolver) Determine(ctx context.Context, repository string) (string, error) {
	ownerRepo, err := githubOwnerRepo(repository)
	if err != nil {
		return "", fmt.Errorf("%w: %s: %v", ErrDefaultBranchUnknown, repository, err)
	}

	// gh accepts the repository explicitly as an argument, so this never
	// needs to run from inside a git checkout (unlike runGH's other
	// callers in pr.go, which resolve the repository from dir's git remote
	// config) — cmd.Dir is irrelevant here, hence the empty dir.
	out, err := runGH(ctx, "", "repo", "view", ownerRepo, "--json", "defaultBranchRef", "-q", ".defaultBranchRef.name")
	if err != nil {
		return "", fmt.Errorf("%w: %s: %v", ErrDefaultBranchUnknown, repository, err)
	}
	branch := strings.TrimSpace(out)
	if branch == "" {
		return "", fmt.Errorf("%w: %s: gh returned an empty default branch", ErrDefaultBranchUnknown, repository)
	}
	return branch, nil
}

// githubOwnerRepo extracts "owner/repo" from a "host/owner/repo" repository
// identifier (e.g. "github.com/timmersuk/llm-workbench" ->
// "timmersuk/llm-workbench") — the explicit-repository form `gh repo view`
// accepts.
func githubOwnerRepo(repository string) (string, error) {
	parts := strings.Split(repository, "/")
	if len(parts) < 3 {
		return "", fmt.Errorf("expected host/owner/repo, got %q", repository)
	}
	return strings.Join(parts[len(parts)-2:], "/"), nil
}

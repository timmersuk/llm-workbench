package api

import (
	"context"
	"errors"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/mock"
	"github.com/stretchr/testify/require"

	"github.com/timmersuk/llm-workbench/internal/agentrunner"
	"github.com/timmersuk/llm-workbench/internal/project"
)

// fakeDefaultBranchResolver stands in for a real `gh repo view` call in
// tests, mirroring fakeGitHubPRClient's shape (pr_test.go) — no network or
// GitHub auth involved.
type fakeDefaultBranchResolver struct {
	branch string
	err    error
	calls  int
}

func (f *fakeDefaultBranchResolver) Determine(_ context.Context, _ string) (string, error) {
	f.calls++
	if f.err != nil {
		return "", f.err
	}
	return f.branch, nil
}

func TestEnsureDefaultBranch_AlreadySetSkipsResolverAndPersist(t *testing.T) {
	projects := new(mockProjectStore) // no .On("Update", ...) set up — a call would fail the test
	resolver := &fakeDefaultBranchResolver{branch: "should-not-be-used"}

	proj := project.Project{ID: "demo-project", Repositories: []string{"github.com/x/myrepo"}, DefaultBranch: "main"}
	got, err := (&Server{Projects: projects, DefaultBranchResolver: resolver}).ensureDefaultBranch(context.Background(), proj)

	require.NoError(t, err)
	assert.Equal(t, "main", got)
	assert.Equal(t, 0, resolver.calls, "resolver must not be consulted when DefaultBranch is already set")
}

func TestEnsureDefaultBranch_BackfillsAndPersistsWhenUnset(t *testing.T) {
	proj := project.Project{
		ID:           "demo-project",
		Name:         "Demo Project",
		Repositories: []string{"github.com/x/myrepo"},
	}
	projects := new(mockProjectStore)
	projects.On("Update", "demo-project", project.UpdateInput{
		Name:          proj.Name,
		Repositories:  proj.Repositories,
		DefaultBranch: "main",
	}).Return(project.Project{}, nil)
	resolver := &fakeDefaultBranchResolver{branch: "main"}

	got, err := (&Server{Projects: projects, DefaultBranchResolver: resolver}).ensureDefaultBranch(context.Background(), proj)

	require.NoError(t, err)
	assert.Equal(t, "main", got)
	assert.Equal(t, 1, resolver.calls)
	projects.AssertExpectations(t)
}

func TestEnsureDefaultBranch_ResolverFailureFailsClosed(t *testing.T) {
	proj := project.Project{ID: "demo-project", Repositories: []string{"github.com/x/myrepo"}}
	projects := new(mockProjectStore) // no .On("Update", ...) — must not be called on failure
	resolver := &fakeDefaultBranchResolver{err: agentrunner.ErrDefaultBranchUnknown}

	_, err := (&Server{Projects: projects, DefaultBranchResolver: resolver}).ensureDefaultBranch(context.Background(), proj)

	assert.ErrorIs(t, err, agentrunner.ErrDefaultBranchUnknown)
}

func TestEnsureDefaultBranch_NoRepositoriesIsAnError(t *testing.T) {
	proj := project.Project{ID: "demo-project"}
	projects := new(mockProjectStore)
	resolver := &fakeDefaultBranchResolver{branch: "main"}

	_, err := (&Server{Projects: projects, DefaultBranchResolver: resolver}).ensureDefaultBranch(context.Background(), proj)

	assert.ErrorIs(t, err, agentrunner.ErrNoRepository)
	assert.Equal(t, 0, resolver.calls)
}

func TestEnsureDefaultBranch_PersistFailurePropagates(t *testing.T) {
	proj := project.Project{ID: "demo-project", Repositories: []string{"github.com/x/myrepo"}}
	projects := new(mockProjectStore)
	projects.On("Update", "demo-project", mock.Anything).Return(project.Project{}, errors.New("disk full"))
	resolver := &fakeDefaultBranchResolver{branch: "main"}

	_, err := (&Server{Projects: projects, DefaultBranchResolver: resolver}).ensureDefaultBranch(context.Background(), proj)

	assert.Error(t, err)
}

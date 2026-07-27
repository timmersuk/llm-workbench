package gitstore

import (
	"errors"
	"os"
	"path/filepath"
	"testing"

	"github.com/go-git/go-git/v5"
	"github.com/go-git/go-git/v5/plumbing"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/timmersuk/llm-workbench/internal/project"
	"github.com/timmersuk/llm-workbench/internal/task"
)

// newBareRemote creates an empty bare repository under t.TempDir(), the
// same shape a fresh deployment or e2e test harness provisions via `git
// init --bare` (see the design note near cmd/server/main.go) before
// setting DATA_REPO_URL to its path.
func newBareRemote(t *testing.T) string {
	t.Helper()
	dir := filepath.Join(t.TempDir(), "remote.git")
	_, err := git.PlainInit(dir, true)
	require.NoError(t, err)
	return dir
}

func TestOpen_ClonesIntoEmptyWorkspaceRoot(t *testing.T) {
	remote := newBareRemote(t)
	workspaceRoot := filepath.Join(t.TempDir(), "workspace")

	store, err := Open(workspaceRoot, remote)
	require.NoError(t, err)
	require.NotNil(t, store)
	assert.DirExists(t, filepath.Join(workspaceRoot, ".git"))
}

func TestOpen_ClonesIntoAbsentWorkspaceRoot(t *testing.T) {
	remote := newBareRemote(t)
	// Deliberately do not create workspaceRoot at all (not even an empty
	// dir) — a fresh deployment's WORKSPACE_ROOT may not exist yet.
	workspaceRoot := filepath.Join(t.TempDir(), "does-not-exist-yet")

	store, err := Open(workspaceRoot, remote)
	require.NoError(t, err)
	require.NotNil(t, store)
	assert.DirExists(t, filepath.Join(workspaceRoot, ".git"))
}

func TestOpen_ResumesExistingMatchingClone(t *testing.T) {
	remote := newBareRemote(t)
	workspaceRoot := filepath.Join(t.TempDir(), "workspace")

	store1, err := Open(workspaceRoot, remote)
	require.NoError(t, err)

	_, err = store1.Projects.Create(newProjectCreateInput("first"))
	require.NoError(t, err)

	// Re-open the same workspaceRoot: this must resume the existing
	// checkout (not error, not re-clone), and see the commit store1 made.
	store2, err := Open(workspaceRoot, remote)
	require.NoError(t, err)

	result, err := store2.Projects.List()
	require.NoError(t, err)
	assert.Len(t, result.Projects, 1)
}

func TestOpen_AmbiguousWorkspace_NonGitNonEmptyDirectory(t *testing.T) {
	remote := newBareRemote(t)
	workspaceRoot := filepath.Join(t.TempDir(), "workspace")
	require.NoError(t, os.MkdirAll(workspaceRoot, 0o755))
	require.NoError(t, os.WriteFile(filepath.Join(workspaceRoot, "stray-file.txt"), []byte("hi"), 0o644))

	_, err := Open(workspaceRoot, remote)
	require.Error(t, err)
	assert.ErrorIs(t, err, ErrAmbiguousWorkspace)
}

func TestOpen_AmbiguousWorkspace_MismatchedOrigin(t *testing.T) {
	remoteA := newBareRemote(t)
	remoteB := newBareRemote(t)
	workspaceRoot := filepath.Join(t.TempDir(), "workspace")

	_, err := Open(workspaceRoot, remoteA)
	require.NoError(t, err)

	// Re-opening the same workspaceRoot against a *different* dataRepoURL
	// is exactly the ambiguous case Open must refuse to guess through.
	_, err = Open(workspaceRoot, remoteB)
	require.Error(t, err)
	assert.ErrorIs(t, err, ErrAmbiguousWorkspace)
}

func TestOpen_AmbiguousWorkspace_GitCheckoutWithNoOriginRemote(t *testing.T) {
	remote := newBareRemote(t)
	workspaceRoot := filepath.Join(t.TempDir(), "workspace")
	require.NoError(t, os.MkdirAll(workspaceRoot, 0o755))
	_, err := git.PlainInit(workspaceRoot, false)
	require.NoError(t, err)

	_, err = Open(workspaceRoot, remote)
	require.Error(t, err)
	assert.ErrorIs(t, err, ErrAmbiguousWorkspace)
}

func newProjectCreateInput(name string) project.CreateInput {
	return project.CreateInput{Name: name}
}

func newProjectUpdateInput(name string) project.UpdateInput {
	return project.UpdateInput{Name: name}
}

func newTask(id string) task.Task {
	return task.Task{ID: id, Title: "Test task", Objective: "Do the thing"}
}

// TestStore_CreateCommitsLocally exercises the actual create-then-commit
// path end to end via the real project/task packages (not the placeholder
// above), asserting both the on-disk YAML and the git history it produces.
func TestStore_CreateCommitsLocally(t *testing.T) {
	remote := newBareRemote(t)
	workspaceRoot := filepath.Join(t.TempDir(), "workspace")
	store, err := Open(workspaceRoot, remote)
	require.NoError(t, err)

	p, err := store.Projects.Create(newProjectCreateInput("Demo Project"))
	require.NoError(t, err)
	assert.Equal(t, "demo-project", p.ID)

	commits := logMessages(t, store.core.repo)
	require.Len(t, commits, 1)
	assert.Equal(t, `Create project "Demo Project"`, commits[0])

	tsk, err := store.Tasks.Create(p.ID, newTask("first-task"))
	require.NoError(t, err)
	assert.Equal(t, "first-task", tsk.ID)

	commits = logMessages(t, store.core.repo)
	require.Len(t, commits, 2)
	assert.Equal(t, "Create task demo-project/first-task", commits[0])
}

// TestStore_UpdateCommitsLocally exercises Update's commit path (as
// opposed to Create's), including that a failed Update (unknown id) is
// never committed.
func TestStore_UpdateCommitsLocally(t *testing.T) {
	remote := newBareRemote(t)
	workspaceRoot := filepath.Join(t.TempDir(), "workspace")
	store, err := Open(workspaceRoot, remote)
	require.NoError(t, err)

	p, err := store.Projects.Create(newProjectCreateInput("Demo Project"))
	require.NoError(t, err)

	_, err = store.Projects.Update(p.ID, newProjectUpdateInput("Renamed Project"))
	require.NoError(t, err)

	commits := logMessages(t, store.core.repo)
	require.Len(t, commits, 2)
	assert.Equal(t, "Update project demo-project", commits[0])

	_, err = store.Projects.Update("does-not-exist", newProjectUpdateInput("Nope"))
	require.Error(t, err)

	// A failed Update must not produce a commit.
	commits = logMessages(t, store.core.repo)
	assert.Len(t, commits, 2)
}

func logMessages(t *testing.T, repo *git.Repository) []string {
	t.Helper()
	head, err := repo.Head()
	if errors.Is(err, plumbing.ErrReferenceNotFound) {
		return nil
	}
	require.NoError(t, err)

	iter, err := repo.Log(&git.LogOptions{From: head.Hash()})
	require.NoError(t, err)
	defer iter.Close()

	var messages []string
	for {
		c, err := iter.Next()
		if err != nil {
			break
		}
		messages = append(messages, c.Message)
	}
	return messages
}

package gitstore

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

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
	_, err := runGit("", "init", "--bare", dir)
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
	// checkout (not error, not re-clone), and see the write store1 made —
	// FileStore writes land on disk immediately regardless of commit
	// timing (commits are deferred to the push worker's tick, see
	// commit.go), so this doesn't depend on a commit having happened yet.
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
	_, err := runGit(workspaceRoot, "init")
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

// logMessages returns every commit message reachable from HEAD, oldest
// last (matching `git log`'s default order) — nil if nothing has been
// committed yet (an empty checkout has no HEAD).
func logMessages(t *testing.T, root string) []string {
	t.Helper()
	out, err := runGit(root, "log", "--pretty=%s")
	if err != nil {
		// "unknown revision or path not in the working tree" et al: HEAD
		// doesn't exist yet because nothing has been committed.
		return nil
	}
	out = strings.TrimRight(out, "\n")
	if out == "" {
		return nil
	}
	return strings.Split(out, "\n")
}

// TestStore_CreateEnqueuesPendingChange exercises the actual
// create-then-enqueue path end to end via the real project/task packages
// (not the placeholder above): writes land on disk immediately, but
// nothing is committed until commitPending runs (the push worker's job —
// see push_test.go for the commit/push behavior itself).
func TestStore_CreateEnqueuesPendingChange(t *testing.T) {
	remote := newBareRemote(t)
	workspaceRoot := filepath.Join(t.TempDir(), "workspace")
	store, err := Open(workspaceRoot, remote)
	require.NoError(t, err)

	p, err := store.Projects.Create(newProjectCreateInput("Demo Project"))
	require.NoError(t, err)
	assert.Equal(t, "demo-project", p.ID)
	assert.FileExists(t, filepath.Join(workspaceRoot, "projects", "demo-project", "project.yaml"))
	assert.Nil(t, logMessages(t, workspaceRoot), "nothing should be committed before commitPending runs")

	require.NoError(t, store.core.commitPending(time.Hour))
	commits := logMessages(t, workspaceRoot)
	require.Len(t, commits, 1)
	assert.Equal(t, `Create project "Demo Project"`, commits[0])

	tsk, err := store.Tasks.Create(p.ID, newTask("first-task"))
	require.NoError(t, err)
	assert.Equal(t, "first-task", tsk.ID)

	require.NoError(t, store.core.commitPending(time.Hour))
	commits = logMessages(t, workspaceRoot)
	require.Len(t, commits, 2)
	assert.Equal(t, "Create task demo-project/first-task", commits[0])
}

// TestStore_UpdateEnqueuesPendingChange exercises Update's enqueue path (as
// opposed to Create's), including that a failed Update (unknown id) never
// enqueues anything.
func TestStore_UpdateEnqueuesPendingChange(t *testing.T) {
	remote := newBareRemote(t)
	workspaceRoot := filepath.Join(t.TempDir(), "workspace")
	store, err := Open(workspaceRoot, remote)
	require.NoError(t, err)

	p, err := store.Projects.Create(newProjectCreateInput("Demo Project"))
	require.NoError(t, err)
	// Drain before the Update: both touch the same project directory, so
	// queuing them together would squash into one commit (see
	// TestCommitPending_SquashesPendingChangesToTheSameDirectory) — this
	// test wants each in its own commit.
	require.NoError(t, store.core.commitPending(time.Hour))

	_, err = store.Projects.Update(p.ID, newProjectUpdateInput("Renamed Project"))
	require.NoError(t, err)

	require.NoError(t, store.core.commitPending(time.Hour))
	commits := logMessages(t, workspaceRoot)
	require.Len(t, commits, 2)
	assert.Equal(t, "Update project demo-project", commits[0])

	_, err = store.Projects.Update("does-not-exist", newProjectUpdateInput("Nope"))
	require.Error(t, err)

	// A failed Update must not enqueue (and therefore must not commit).
	require.NoError(t, store.core.commitPending(time.Hour))
	commits = logMessages(t, workspaceRoot)
	assert.Len(t, commits, 2)
}

// TestStore_SetSessionID_EnqueuesPendingChangeAndPersists exercises
// SetSessionID's withPending wiring (mirroring AppendConversationMessages'
// own commit-on-push-tick behavior): the write lands on disk immediately,
// nothing is committed until commitPending runs, and the value round-trips
// through GetSessionID afterward — the durability an API process restart
// relies on.
func TestStore_SetSessionID_EnqueuesPendingChangeAndPersists(t *testing.T) {
	remote := newBareRemote(t)
	workspaceRoot := filepath.Join(t.TempDir(), "workspace")
	store, err := Open(workspaceRoot, remote)
	require.NoError(t, err)

	p, err := store.Projects.Create(newProjectCreateInput("Demo Project"))
	require.NoError(t, err)
	_, err = store.Tasks.Create(p.ID, newTask("first-task"))
	require.NoError(t, err)
	require.NoError(t, store.core.commitPending(time.Hour))

	require.NoError(t, store.Tasks.SetSessionID(p.ID, "first-task", task.StageRequirements, "claude-code", "sess-123"))
	assert.FileExists(t, filepath.Join(workspaceRoot, "projects", p.ID, "tasks", "first-task", "conversation-requirements.session.yaml"))
	assert.Len(t, logMessages(t, workspaceRoot), 1, "nothing should be committed before commitPending runs")

	require.NoError(t, store.core.commitPending(time.Hour))
	commits := logMessages(t, workspaceRoot)
	require.Len(t, commits, 2)
	assert.Equal(t, `Record claude-code session id for demo-project/first-task/requirements`, commits[0])

	id, err := store.Tasks.GetSessionID(p.ID, "first-task", task.StageRequirements, "claude-code")
	require.NoError(t, err)
	assert.Equal(t, "sess-123", id)
}

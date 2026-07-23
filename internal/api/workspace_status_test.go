package api

import (
	"context"
	"encoding/json"
	"io/fs"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/timmersuk/llm-workbench/internal/gitutil"
	"github.com/timmersuk/llm-workbench/internal/project"
)

// newWorkspaceStatusRepo creates reposRoot/name as a real clone of a
// throwaway source repo — cloning (rather than a plain `git init` + manual
// `remote add`) is what gives the checkout's branch proper upstream
// tracking, the same way gitutil.Clone's own tests rely on, so
// BehindOrigin's `@{upstream}` resolves instead of reporting Known: false.
func newWorkspaceStatusRepo(t *testing.T, reposRoot, name string) (checkoutDir, sourceDir string) {
	t.Helper()
	sourceDir = filepath.Join(t.TempDir(), "source")
	require.NoError(t, os.MkdirAll(sourceDir, 0o755))
	gitRun(t, sourceDir, "init", "-q")
	gitRun(t, sourceDir, "config", "user.email", "t@example.com")
	gitRun(t, sourceDir, "config", "user.name", "T")
	require.NoError(t, os.WriteFile(filepath.Join(sourceDir, "README.md"), []byte("hi\n"), 0o644))
	gitRun(t, sourceDir, "add", ".")
	gitRun(t, sourceDir, "commit", "-q", "-m", "init")

	checkoutDir = filepath.Join(reposRoot, name)
	require.NoError(t, gitutil.Clone(context.Background(), sourceDir, checkoutDir))
	return checkoutDir, sourceDir
}

func TestHandleWorkspaceStatus_ProjectNotFound(t *testing.T) {
	projects := new(mockProjectStore)
	projects.On("Get", "demo-project").Return(nil, fs.ErrNotExist)

	req := httptest.NewRequest(http.MethodGet, "/api/v1/projects/demo-project/workspace-status", nil)
	req.SetPathValue("projectId", "demo-project")
	w := httptest.NewRecorder()
	handleWorkspaceStatus(projects, "")(w, req)

	assert.Equal(t, http.StatusNotFound, w.Code)
}

func TestHandleWorkspaceStatus_NoRepositoryConfigured(t *testing.T) {
	projects := new(mockProjectStore)
	projects.On("Get", "demo-project").Return(project.Project{ID: "demo-project"}, nil)

	req := httptest.NewRequest(http.MethodGet, "/api/v1/projects/demo-project/workspace-status", nil)
	req.SetPathValue("projectId", "demo-project")
	w := httptest.NewRecorder()
	handleWorkspaceStatus(projects, t.TempDir())(w, req)

	require.Equal(t, http.StatusOK, w.Code)
	var got workspaceStatusResponse
	require.NoError(t, json.Unmarshal(w.Body.Bytes(), &got))
	assert.False(t, got.RepositoryConfigured)
}

func TestHandleWorkspaceStatus_CleanCheckout(t *testing.T) {
	reposRoot := t.TempDir()
	newWorkspaceStatusRepo(t, reposRoot, "myrepo")

	projects := new(mockProjectStore)
	projects.On("Get", "demo-project").Return(project.Project{ID: "demo-project", Repositories: []string{"github.com/x/myrepo"}}, nil)

	req := httptest.NewRequest(http.MethodGet, "/api/v1/projects/demo-project/workspace-status", nil)
	req.SetPathValue("projectId", "demo-project")
	w := httptest.NewRecorder()
	handleWorkspaceStatus(projects, reposRoot)(w, req)

	require.Equal(t, http.StatusOK, w.Code)
	var got workspaceStatusResponse
	require.NoError(t, json.Unmarshal(w.Body.Bytes(), &got))
	assert.True(t, got.RepositoryConfigured)
	assert.True(t, got.Status.Dirty.Known)
	assert.False(t, got.Status.Dirty.Dirty)
	assert.True(t, got.Status.BehindOrigin.Known)
	assert.Equal(t, 0, got.Status.BehindOrigin.Behind)
}

func TestHandleWorkspaceStatus_DirtyCheckout(t *testing.T) {
	reposRoot := t.TempDir()
	checkoutDir, _ := newWorkspaceStatusRepo(t, reposRoot, "myrepo")
	require.NoError(t, os.WriteFile(filepath.Join(checkoutDir, "scratch.txt"), []byte("x\n"), 0o644))

	projects := new(mockProjectStore)
	projects.On("Get", "demo-project").Return(project.Project{ID: "demo-project", Repositories: []string{"github.com/x/myrepo"}}, nil)

	req := httptest.NewRequest(http.MethodGet, "/api/v1/projects/demo-project/workspace-status", nil)
	req.SetPathValue("projectId", "demo-project")
	w := httptest.NewRecorder()
	handleWorkspaceStatus(projects, reposRoot)(w, req)

	require.Equal(t, http.StatusOK, w.Code)
	var got workspaceStatusResponse
	require.NoError(t, json.Unmarshal(w.Body.Bytes(), &got))
	assert.True(t, got.Status.Dirty.Dirty)
}

func TestHandleWorkspaceStatus_BehindOrigin(t *testing.T) {
	reposRoot := t.TempDir()
	_, sourceDir := newWorkspaceStatusRepo(t, reposRoot, "myrepo")

	require.NoError(t, os.WriteFile(filepath.Join(sourceDir, "second.txt"), []byte("x\n"), 0o644))
	gitRun(t, sourceDir, "add", ".")
	gitRun(t, sourceDir, "commit", "-q", "-m", "second commit")

	projects := new(mockProjectStore)
	projects.On("Get", "demo-project").Return(project.Project{ID: "demo-project", Repositories: []string{"github.com/x/myrepo"}}, nil)

	req := httptest.NewRequest(http.MethodGet, "/api/v1/projects/demo-project/workspace-status", nil)
	req.SetPathValue("projectId", "demo-project")
	w := httptest.NewRecorder()
	handleWorkspaceStatus(projects, reposRoot)(w, req)

	require.Equal(t, http.StatusOK, w.Code)
	var got workspaceStatusResponse
	require.NoError(t, json.Unmarshal(w.Body.Bytes(), &got))
	assert.True(t, got.Status.BehindOrigin.Known)
	assert.Equal(t, 1, got.Status.BehindOrigin.Behind)
}

func TestHandleWorkspaceStatus_NeverClonesMissingCheckout(t *testing.T) {
	reposRoot := t.TempDir() // no "myrepo" checkout under here

	projects := new(mockProjectStore)
	projects.On("Get", "demo-project").Return(project.Project{ID: "demo-project", Repositories: []string{"github.com/x/myrepo"}}, nil)

	req := httptest.NewRequest(http.MethodGet, "/api/v1/projects/demo-project/workspace-status", nil)
	req.SetPathValue("projectId", "demo-project")
	w := httptest.NewRecorder()
	handleWorkspaceStatus(projects, reposRoot)(w, req)

	require.Equal(t, http.StatusOK, w.Code)
	var got workspaceStatusResponse
	require.NoError(t, json.Unmarshal(w.Body.Bytes(), &got))
	assert.True(t, got.RepositoryConfigured)
	assert.False(t, got.Status.BehindOrigin.Known)
	assert.False(t, got.Status.Dirty.Known)
	assert.NoDirExists(t, filepath.Join(reposRoot, "myrepo"), "must never clone just to answer a status check")
}

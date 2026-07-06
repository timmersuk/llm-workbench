package api

import (
	"bytes"
	"encoding/json"
	"errors"
	"io/fs"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/timmersuk/llm-workbench/internal/project"
)

func newProjectRequest(t *testing.T, method, path string, body any) *http.Request {
	t.Helper()
	data, err := json.Marshal(body)
	require.NoError(t, err)
	return httptest.NewRequest(method, path, bytes.NewReader(data))
}

func TestHandleListProjects_OK(t *testing.T) {
	lister := new(mockProjectStore)
	lister.On("List").Return(project.ListResult{Projects: []project.Project{{ID: "demo-project"}}}, nil)

	req := httptest.NewRequest(http.MethodGet, "/api/v1/projects", nil)
	w := httptest.NewRecorder()
	handleListProjects(lister)(w, req)

	require.Equal(t, http.StatusOK, w.Code)
	var got project.ListResult
	require.NoError(t, json.Unmarshal(w.Body.Bytes(), &got))
	require.Len(t, got.Projects, 1)
	assert.Equal(t, "demo-project", got.Projects[0].ID)
}

func TestHandleListProjects_Error(t *testing.T) {
	lister := new(mockProjectStore)
	lister.On("List").Return(nil, errors.New("disk on fire"))

	req := httptest.NewRequest(http.MethodGet, "/api/v1/projects", nil)
	w := httptest.NewRecorder()
	handleListProjects(lister)(w, req)

	assert.Equal(t, http.StatusInternalServerError, w.Code)
}

func TestHandleGetProject_OK(t *testing.T) {
	lister := new(mockProjectStore)
	lister.On("Get", "demo-project").Return(project.Project{ID: "demo-project", Name: "Demo"}, nil)

	req := httptest.NewRequest(http.MethodGet, "/api/v1/projects/demo-project", nil)
	req.SetPathValue("id", "demo-project")
	w := httptest.NewRecorder()
	handleGetProject(lister)(w, req)

	require.Equal(t, http.StatusOK, w.Code)
	var got project.Project
	require.NoError(t, json.Unmarshal(w.Body.Bytes(), &got))
	assert.Equal(t, "Demo", got.Name)
}

func TestHandleGetProject_NotFound(t *testing.T) {
	lister := new(mockProjectStore)
	lister.On("Get", "nonexistent").Return(nil, fs.ErrNotExist)

	req := httptest.NewRequest(http.MethodGet, "/api/v1/projects/nonexistent", nil)
	req.SetPathValue("id", "nonexistent")
	w := httptest.NewRecorder()
	handleGetProject(lister)(w, req)

	assert.Equal(t, http.StatusNotFound, w.Code)
}

func TestHandleGetProject_InvalidID(t *testing.T) {
	lister := new(mockProjectStore)
	lister.On("Get", "../etc").Return(nil, project.ErrInvalidID)

	req := httptest.NewRequest(http.MethodGet, "/api/v1/projects/..%2Fetc", nil)
	req.SetPathValue("id", "../etc")
	w := httptest.NewRecorder()
	handleGetProject(lister)(w, req)

	assert.Equal(t, http.StatusBadRequest, w.Code)
}

func TestHandleCreateProject_OK(t *testing.T) {
	store := new(mockProjectStore)
	in := project.CreateInput{Name: "Demo Project"}
	store.On("Create", in).Return(project.Project{ID: "demo-project", Name: "Demo Project"}, nil)

	req := newProjectRequest(t, http.MethodPost, "/api/v1/projects", in)
	w := httptest.NewRecorder()
	handleCreateProject(store)(w, req)

	require.Equal(t, http.StatusCreated, w.Code)
	var got project.Project
	require.NoError(t, json.Unmarshal(w.Body.Bytes(), &got))
	assert.Equal(t, "demo-project", got.ID)
}

func TestHandleCreateProject_InvalidBody(t *testing.T) {
	store := new(mockProjectStore)

	req := httptest.NewRequest(http.MethodPost, "/api/v1/projects", bytes.NewReader([]byte("not json")))
	w := httptest.NewRecorder()
	handleCreateProject(store)(w, req)

	assert.Equal(t, http.StatusBadRequest, w.Code)
}

func TestHandleCreateProject_MissingName(t *testing.T) {
	store := new(mockProjectStore)
	in := project.CreateInput{Name: ""}
	store.On("Create", in).Return(nil, project.ErrMissingName)

	req := newProjectRequest(t, http.MethodPost, "/api/v1/projects", in)
	w := httptest.NewRecorder()
	handleCreateProject(store)(w, req)

	assert.Equal(t, http.StatusBadRequest, w.Code)
}

func TestHandleCreateProject_AlreadyExists(t *testing.T) {
	store := new(mockProjectStore)
	in := project.CreateInput{Name: "Demo Project"}
	store.On("Create", in).Return(nil, project.ErrAlreadyExists)

	req := newProjectRequest(t, http.MethodPost, "/api/v1/projects", in)
	w := httptest.NewRecorder()
	handleCreateProject(store)(w, req)

	assert.Equal(t, http.StatusConflict, w.Code)
}

func TestHandleUpdateProject_OK(t *testing.T) {
	store := new(mockProjectStore)
	in := project.UpdateInput{Name: "Renamed"}
	store.On("Update", "demo-project", in).Return(project.Project{ID: "demo-project", Name: "Renamed"}, nil)

	req := newProjectRequest(t, http.MethodPut, "/api/v1/projects/demo-project", in)
	req.SetPathValue("id", "demo-project")
	w := httptest.NewRecorder()
	handleUpdateProject(store)(w, req)

	require.Equal(t, http.StatusOK, w.Code)
	var got project.Project
	require.NoError(t, json.Unmarshal(w.Body.Bytes(), &got))
	assert.Equal(t, "Renamed", got.Name)
}

func TestHandleUpdateProject_NotFound(t *testing.T) {
	store := new(mockProjectStore)
	in := project.UpdateInput{Name: "Renamed"}
	store.On("Update", "nonexistent", in).Return(nil, fs.ErrNotExist)

	req := newProjectRequest(t, http.MethodPut, "/api/v1/projects/nonexistent", in)
	req.SetPathValue("id", "nonexistent")
	w := httptest.NewRecorder()
	handleUpdateProject(store)(w, req)

	assert.Equal(t, http.StatusNotFound, w.Code)
}

func TestHandleUpdateProject_MissingName(t *testing.T) {
	store := new(mockProjectStore)
	in := project.UpdateInput{Name: ""}
	store.On("Update", "demo-project", in).Return(nil, project.ErrMissingName)

	req := newProjectRequest(t, http.MethodPut, "/api/v1/projects/demo-project", in)
	req.SetPathValue("id", "demo-project")
	w := httptest.NewRecorder()
	handleUpdateProject(store)(w, req)

	assert.Equal(t, http.StatusBadRequest, w.Code)
}

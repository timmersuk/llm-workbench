package api

import (
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

func TestHandleListProjects_OK(t *testing.T) {
	lister := new(mockProjectLister)
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
	lister := new(mockProjectLister)
	lister.On("List").Return(nil, errors.New("disk on fire"))

	req := httptest.NewRequest(http.MethodGet, "/api/v1/projects", nil)
	w := httptest.NewRecorder()
	handleListProjects(lister)(w, req)

	assert.Equal(t, http.StatusInternalServerError, w.Code)
}

func TestHandleGetProject_OK(t *testing.T) {
	lister := new(mockProjectLister)
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
	lister := new(mockProjectLister)
	lister.On("Get", "nonexistent").Return(nil, fs.ErrNotExist)

	req := httptest.NewRequest(http.MethodGet, "/api/v1/projects/nonexistent", nil)
	req.SetPathValue("id", "nonexistent")
	w := httptest.NewRecorder()
	handleGetProject(lister)(w, req)

	assert.Equal(t, http.StatusNotFound, w.Code)
}

func TestHandleGetProject_InvalidID(t *testing.T) {
	lister := new(mockProjectLister)
	lister.On("Get", "../etc").Return(nil, project.ErrInvalidID)

	req := httptest.NewRequest(http.MethodGet, "/api/v1/projects/..%2Fetc", nil)
	req.SetPathValue("id", "../etc")
	w := httptest.NewRecorder()
	handleGetProject(lister)(w, req)

	assert.Equal(t, http.StatusBadRequest, w.Code)
}

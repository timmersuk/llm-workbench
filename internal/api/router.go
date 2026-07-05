// Package api wires the HTTP surface for the workbench server: health/version
// endpoints, read-only task/project listing, chat completions, and the
// embedded frontend.
package api

import (
	"context"
	"io/fs"
	"net/http"

	"github.com/timmersuk/llm-workbench/internal/chat"
	"github.com/timmersuk/llm-workbench/internal/project"
	"github.com/timmersuk/llm-workbench/internal/task"
)

// TaskLister lists and retrieves tasks. Satisfied by *task.FileStore.
type TaskLister interface {
	List() (task.ListResult, error)
	Get(id string) (task.Task, error)
}

// ProjectLister lists and retrieves projects. Satisfied by
// *project.FileStore.
type ProjectLister interface {
	List() (project.ListResult, error)
	Get(id string) (project.Project, error)
}

// ChatCompleter creates chat completions. Satisfied by *chat.Client.
type ChatCompleter interface {
	CreateChatCompletion(ctx context.Context, req chat.CompletionRequest) (chat.CompletionResponse, error)
	CheckHealth(ctx context.Context) error
	ListModels(ctx context.Context) ([]string, error)
}

// NewRouter builds the full HTTP handler: the JSON API plus the embedded
// frontend (serving frontendFS, with an index.html SPA fallback for unknown
// paths).
func NewRouter(tasks TaskLister, projects ProjectLister, chatCompleter ChatCompleter, frontendFS fs.FS, buildId string) http.Handler {
	mux := http.NewServeMux()

	mux.HandleFunc("GET /healthcheck", handleHealthcheck(chatCompleter, buildId))
	mux.HandleFunc("GET /api/v1/version", handleVersion(buildId))

	mux.HandleFunc("GET /api/v1/tasks", handleListTasks(tasks))
	mux.HandleFunc("GET /api/v1/tasks/{id}", handleGetTask(tasks))

	mux.HandleFunc("GET /api/v1/projects", handleListProjects(projects))
	mux.HandleFunc("GET /api/v1/projects/{id}", handleGetProject(projects))

	mux.HandleFunc("POST /api/v1/chat/completions", handleChatCompletions(chatCompleter))
	mux.HandleFunc("GET /api/v1/chat/models", handleListModels(chatCompleter))

	mux.Handle("GET /", newFrontendHandler(frontendFS))

	return mux
}

func handleHealthcheck(chatCompleter ChatCompleter, buildId string) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		if chatCompleter != nil {
			if err := chatCompleter.CheckHealth(r.Context()); err != nil {
				writeJSON(w, http.StatusServiceUnavailable, map[string]string{"status": "error", "build_id": buildId, "error": err.Error()})
				return
			}
		}

		writeJSON(w, http.StatusOK, map[string]string{"status": "ok", "build_id": buildId})
	}
}

func handleVersion(buildId string) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		writeJSON(w, http.StatusOK, map[string]string{"version": buildId})
	}
}

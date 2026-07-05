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
	List() ([]task.Task, error)
	Get(id string) (task.Task, error)
}

// ProjectLister lists and retrieves projects. Satisfied by
// *project.FileStore.
type ProjectLister interface {
	List() ([]project.Project, error)
	Get(id string) (project.Project, error)
}

// ChatCompleter creates chat completions. Satisfied by *chat.Client.
type ChatCompleter interface {
	CreateChatCompletion(ctx context.Context, req chat.CompletionRequest) (chat.CompletionResponse, error)
}

// Version is reported by GET /api/v1/version. It's set from the module's
// build info by main.go.
var Version = "dev"

// NewRouter builds the full HTTP handler: the JSON API plus the embedded
// frontend (serving frontendFS, with an index.html SPA fallback for unknown
// paths).
func NewRouter(tasks TaskLister, projects ProjectLister, chatCompleter ChatCompleter, frontendFS fs.FS) http.Handler {
	mux := http.NewServeMux()

	mux.HandleFunc("GET /healthcheck", handleHealthcheck)
	mux.HandleFunc("GET /api/v1/version", handleVersion)

	mux.HandleFunc("GET /api/v1/tasks", handleListTasks(tasks))
	mux.HandleFunc("GET /api/v1/tasks/{id}", handleGetTask(tasks))

	mux.HandleFunc("GET /api/v1/projects", handleListProjects(projects))
	mux.HandleFunc("GET /api/v1/projects/{id}", handleGetProject(projects))

	mux.HandleFunc("POST /api/v1/chat/completions", handleChatCompletions(chatCompleter))

	mux.Handle("GET /", newFrontendHandler(frontendFS))

	return mux
}

func handleHealthcheck(w http.ResponseWriter, r *http.Request) {
	w.WriteHeader(http.StatusOK)
	w.Write([]byte("ok"))
}

func handleVersion(w http.ResponseWriter, r *http.Request) {
	writeJSON(w, http.StatusOK, map[string]string{"version": Version})
}

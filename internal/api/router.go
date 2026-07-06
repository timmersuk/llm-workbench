// Package api wires the HTTP surface for the workbench server: health/version
// endpoints, read-only task/project listing, chat completions, and the
// embedded frontend.
package api

import (
	"io/fs"
	"net/http"

	"github.com/timmersuk/llm-workbench/internal/chat"
	"github.com/timmersuk/llm-workbench/internal/knowledge"
	"github.com/timmersuk/llm-workbench/internal/project"
	"github.com/timmersuk/llm-workbench/internal/task"
)

// ProjectStore lists, retrieves, creates, and updates projects, and
// resolves a project's task-store root. Satisfied by *project.FileStore.
type ProjectStore interface {
	List() (project.ListResult, error)
	Get(id string) (project.Project, error)
	Create(in project.CreateInput) (project.Project, error)
	Update(id string, in project.UpdateInput) (project.Project, error)
	TasksRoot(id string) (string, error)
}

// TaskStore lists, retrieves, creates, and updates tasks within a single
// project's task root, plus the GrillMe/Planning Mode lifecycle: the
// derived context.yaml/plan.yaml artifacts, each stage's persisted
// Conversation, and the Finalize/Revise transitions (CONTEXT.md). Satisfied
// by *task.FileStore.
type TaskStore interface {
	List() (task.ListResult, error)
	Get(id string) (task.Task, error)
	Create(t task.Task) (task.Task, error)
	Update(id string, t task.Task) (task.Task, error)

	GetContext(id string) (task.Context, error)
	GetPlan(id string) (task.Plan, error)
	GetConversation(id, stage string) (task.Conversation, error)
	AppendConversationMessages(id, stage string, msgs ...task.ConversationMessage) (task.Conversation, error)
	FinalizeRequirements(id string, draft task.RequirementsDraft) (task.Task, error)
	FinalizePlan(id string, plan task.Plan) (task.Task, error)
	ReviseToRequirements(id string) (task.Task, error)
	ReviseToPlanning(id string) (task.Task, error)
}

// KnowledgeReader resolves a knowledge concept id (e.g.
// "coding-standards/logging") to its parsed OKF document. Satisfied by
// *knowledge.FileReader — see docs/knowledge schema v0.md §6.
type KnowledgeReader interface {
	Get(conceptID string) (knowledge.Concept, error)
}

// TaskStoreFactory builds a TaskStore rooted at the given directory. Task
// routes are always scoped to one project, so handlers resolve the root
// per-request via ProjectStore.TasksRoot and construct a store through this
// factory rather than holding a single package-level task store.
type TaskStoreFactory func(root string) TaskStore

// NewRouter builds the full HTTP handler: the JSON API plus the embedded
// frontend (serving frontendFS, with an index.html SPA fallback for unknown
// paths). chatCompleter is chat.ChatClient — declared and documented in
// internal/chat as a deliberate exception to interfaces normally living in
// the consuming package (see docs/adr/0001-opaque-chat-provider-implementation.md).
func NewRouter(projects ProjectStore, taskStores TaskStoreFactory, chatCompleter chat.ChatClient, knowledgeReader KnowledgeReader, frontendFS fs.FS, buildId string) http.Handler {
	mux := http.NewServeMux()

	mux.HandleFunc("GET /healthcheck", handleHealthcheck(chatCompleter, buildId))
	mux.HandleFunc("GET /api/v1/version", handleVersion(buildId))

	mux.HandleFunc("GET /api/v1/projects", handleListProjects(projects))
	mux.HandleFunc("POST /api/v1/projects", handleCreateProject(projects))
	mux.HandleFunc("GET /api/v1/projects/{id}", handleGetProject(projects))
	mux.HandleFunc("PUT /api/v1/projects/{id}", handleUpdateProject(projects))

	mux.HandleFunc("GET /api/v1/projects/{projectId}/tasks", handleListProjectTasks(projects, taskStores))
	mux.HandleFunc("POST /api/v1/projects/{projectId}/tasks", handleCreateProjectTask(projects, taskStores))
	mux.HandleFunc("GET /api/v1/projects/{projectId}/tasks/{taskId}", handleGetProjectTask(projects, taskStores))
	mux.HandleFunc("PUT /api/v1/projects/{projectId}/tasks/{taskId}", handleUpdateProjectTask(projects, taskStores))

	mux.HandleFunc("GET /api/v1/projects/{projectId}/tasks/{taskId}/context", handleGetTaskContext(projects, taskStores))
	mux.HandleFunc("GET /api/v1/projects/{projectId}/tasks/{taskId}/plan", handleGetTaskPlan(projects, taskStores))
	mux.HandleFunc("GET /api/v1/projects/{projectId}/tasks/{taskId}/stages/{stage}/conversation", handleGetStageConversation(projects, taskStores))
	mux.HandleFunc("POST /api/v1/projects/{projectId}/tasks/{taskId}/stages/{stage}/messages", handlePostStageMessage(projects, taskStores, chatCompleter, knowledgeReader))
	mux.HandleFunc("POST /api/v1/projects/{projectId}/tasks/{taskId}/requirements/finalize", handleFinalizeRequirements(projects, taskStores))
	mux.HandleFunc("POST /api/v1/projects/{projectId}/tasks/{taskId}/plan/finalize", handleFinalizePlan(projects, taskStores))
	mux.HandleFunc("POST /api/v1/projects/{projectId}/tasks/{taskId}/requirements/revise", handleReviseRequirements(projects, taskStores))
	mux.HandleFunc("POST /api/v1/projects/{projectId}/tasks/{taskId}/plan/revise", handleRevisePlan(projects, taskStores))

	mux.HandleFunc("POST /api/v1/chat/completions", handleChatCompletions(chatCompleter))
	mux.HandleFunc("GET /api/v1/chat/models", handleListModels(chatCompleter))

	mux.Handle("GET /", newFrontendHandler(frontendFS))

	return mux
}

func handleHealthcheck(chatCompleter chat.ChatClient, buildId string) http.HandlerFunc {
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

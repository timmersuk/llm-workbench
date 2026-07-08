// Package api wires the HTTP surface for the workbench server: health/version
// endpoints, read-only task/project listing, chat completions, and the
// embedded frontend.
package api

import (
	"fmt"
	"io/fs"
	"net/http"
	"sort"
	"strings"

	"github.com/timmersuk/llm-workbench/internal/agentrunner"
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
	ReplaceConversationMessages(id, stage string, msgs []task.ConversationMessage) (task.Conversation, error)
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
// paths). agentRunners keys the tool-equipped/chat executor options shared
// by both Requirements/Planning stage conversations and the free-floating
// Chat tab (e.g. "claude-code", "local") — every chat/agent interaction
// goes through this map, there is no separate direct chat.ChatClient path.
// Each entry's actual availability is determined at request time via
// AgentRunner.CheckHealth, not by static configuration. reposRoot is the
// local directory agent runners resolve a project's configured repository
// into a workspace under (see agentrunner.ResolveWorkspace); free chat has
// no per-task project, so it passes reposRoot itself as the workspace
// instead.
func NewRouter(projects ProjectStore, taskStores TaskStoreFactory, knowledgeReader KnowledgeReader, agentRunners map[string]agentrunner.AgentRunner, reposRoot string, frontendFS fs.FS, buildId string) http.Handler {
	mux := http.NewServeMux()

	mux.HandleFunc("GET /healthcheck", handleHealthcheck(agentRunners, buildId))
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
	mux.HandleFunc("POST /api/v1/projects/{projectId}/tasks/{taskId}/stages/{stage}/start", handleStartStageConversation(projects, taskStores, knowledgeReader, agentRunners, reposRoot))
	mux.HandleFunc("POST /api/v1/projects/{projectId}/tasks/{taskId}/stages/{stage}/messages", handlePostStageMessage(projects, taskStores, knowledgeReader, agentRunners, reposRoot))
	mux.HandleFunc("DELETE /api/v1/projects/{projectId}/tasks/{taskId}/stages/{stage}/messages/{index}", handleDeleteStageMessage(projects, taskStores, agentRunners))
	mux.HandleFunc("POST /api/v1/projects/{projectId}/tasks/{taskId}/stages/{stage}/messages/{index}/regenerate", handleRegenerateStageMessage(projects, taskStores, knowledgeReader, agentRunners, reposRoot))
	mux.HandleFunc("POST /api/v1/projects/{projectId}/tasks/{taskId}/requirements/finalize", handleFinalizeRequirements(projects, taskStores, agentRunners))
	mux.HandleFunc("POST /api/v1/projects/{projectId}/tasks/{taskId}/plan/finalize", handleFinalizePlan(projects, taskStores, agentRunners))
	mux.HandleFunc("POST /api/v1/projects/{projectId}/tasks/{taskId}/requirements/revise", handleReviseRequirements(projects, taskStores))
	mux.HandleFunc("POST /api/v1/projects/{projectId}/tasks/{taskId}/plan/revise", handleRevisePlan(projects, taskStores))

	mux.HandleFunc("POST /api/v1/chat/completions", handleChatCompletions(agentRunners, reposRoot))
	mux.HandleFunc("POST /api/v1/chat/sessions/close", handleCloseChatSession(agentRunners))
	mux.HandleFunc("GET /api/v1/chat/models", handleListModels(agentRunners))
	mux.HandleFunc("GET /api/v1/agent-executors", handleListAgentExecutors(agentRunners))

	mux.Handle("GET /", newFrontendHandler(frontendFS))

	return mux
}

// subsystemHealth is one entry in healthcheckResponse.Subsystems.
type subsystemHealth struct {
	Status string `json:"status"`
	Error  string `json:"error,omitempty"`
}

// healthcheckResponse reports per-subsystem status (docs/engineering
// conventions.md's Healthchecks section: "a caller should be able to tell
// *which* dependency is down"), not just a single collapsed boolean.
// Error is a semicolon-joined summary of every failing subsystem, kept
// alongside Subsystems for frontend backward-compatibility
// (frontend/src/api.ts's HealthStatus.error).
type healthcheckResponse struct {
	Status     string                     `json:"status"`
	BuildId    string                     `json:"build_id"`
	Error      string                     `json:"error,omitempty"`
	Subsystems map[string]subsystemHealth `json:"subsystems"`
}

func handleHealthcheck(agentRunners map[string]agentrunner.AgentRunner, buildId string) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		subsystems := map[string]subsystemHealth{}
		var failures []string

		probe := func(key string, check func() error) {
			if err := check(); err != nil {
				subsystems[key] = subsystemHealth{Status: "error", Error: err.Error()}
				failures = append(failures, fmt.Sprintf("%s: %s", key, err.Error()))
				return
			}
			subsystems[key] = subsystemHealth{Status: "ok"}
		}

		for name, runner := range agentRunners {
			probe("agent:"+name, func() error { return runner.CheckHealth(r.Context()) })
		}

		resp := healthcheckResponse{Status: "ok", BuildId: buildId, Subsystems: subsystems}
		status := http.StatusOK
		if len(failures) > 0 {
			sort.Strings(failures)
			resp.Status = "error"
			resp.Error = strings.Join(failures, "; ")
			status = http.StatusServiceUnavailable
		}
		writeJSON(w, status, resp)
	}
}

func handleVersion(buildId string) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		writeJSON(w, http.StatusOK, map[string]string{"version": buildId})
	}
}

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
	FinalizeReview(id string, draft task.ReviewDraft) (task.Task, error)
	MarkPRMerged(id string) (task.Task, error)
	RecordPullRequest(id string, pr task.PullRequest) (task.Task, error)
	ReviseToRequirements(id string) (task.Task, error)
	ReviseToPlanning(id string) (task.Task, error)

	NextExecutionID(id string) (string, error)
	RecordExecution(id string, exec task.Execution) (task.Execution, error)
	ListExecutions(id string) ([]task.Execution, error)
	ListReviews(id string) ([]task.Review, error)
}

// KnowledgeStore resolves, lists, and writes OKF concept documents under
// data/knowledge/. Satisfied by *knowledge.FileStore — see docs/knowledge
// schema v0.md §6. Get is the original Milestone 4 read: a concept id (e.g.
// "coding-standards/logging") to its parsed document. List/Put (Milestone 9)
// back the propose_knowledge write path and a future query tool: List
// returns every concept's browsable summary, Put is a whole-file
// create-or-replace by concept id.
type KnowledgeStore interface {
	Get(conceptID string) (knowledge.Concept, error)
	List() ([]knowledge.ConceptSummary, error)
	Put(conceptID string, c knowledge.Concept) error
}

// TaskStoreFactory builds a TaskStore rooted at the given directory. Task
// routes are always scoped to one project, so handlers resolve the root
// per-request via ProjectStore.TasksRoot and construct a store through this
// factory rather than holding a single package-level task store.
type TaskStoreFactory func(root string) TaskStore

// Server holds every dependency internal/api's handlers need that stays
// invariant for the lifetime of the server process — constructed once by
// NewRouter, with every handler (and the internal helpers that mix these
// same invariant deps with per-call data) as a method on it, rather than a
// free function re-closing over a fresh copy of the same parameter list
// each time. Package-internal by design: NewRouter is still the only public
// entrypoint (docs/adr/0016-api-handlers-become-methods-on-an-internal-server-struct.md).
type Server struct {
	Projects              ProjectStore
	TaskStores            TaskStoreFactory
	KnowledgeStore        KnowledgeStore
	AgentRunners          map[string]agentrunner.AgentRunner
	ReposRoot             string
	PRClient              agentrunner.GitHubPRClient
	DefaultBranchResolver agentrunner.DefaultBranchResolver
	FrontendFS            fs.FS
	BuildId               string
}

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
// instead. prClient is the seam handlePushPR uses to open/inspect GitHub
// PRs (agentrunner.GitHubPRClient) — a real one built via
// agentrunner.NewGitHubPRClient() in production, a fake in tests
// (docs/milestones/done/milestone7.md PR 3). defaultBranchResolver is the
// same shape of seam for determining a project's default branch
// (agentrunner.DefaultBranchResolver) — a real one built via
// agentrunner.NewDefaultBranchResolver() in production, a fake in tests
// (docs/milestones/done/milestone8a.md).
func NewRouter(projects ProjectStore, taskStores TaskStoreFactory, knowledgeStore KnowledgeStore, agentRunners map[string]agentrunner.AgentRunner, reposRoot string, prClient agentrunner.GitHubPRClient, defaultBranchResolver agentrunner.DefaultBranchResolver, frontendFS fs.FS, buildId string) http.Handler {
	s := &Server{
		Projects:              projects,
		TaskStores:            taskStores,
		KnowledgeStore:        knowledgeStore,
		AgentRunners:          agentRunners,
		ReposRoot:             reposRoot,
		PRClient:              prClient,
		DefaultBranchResolver: defaultBranchResolver,
		FrontendFS:            frontendFS,
		BuildId:               buildId,
	}

	mux := http.NewServeMux()

	mux.HandleFunc("GET /healthcheck", s.handleHealthcheck())
	mux.HandleFunc("GET /api/v1/version", s.handleVersion())

	mux.HandleFunc("GET /api/v1/projects", s.handleListProjects())
	mux.HandleFunc("POST /api/v1/projects", s.handleCreateProject())
	mux.HandleFunc("GET /api/v1/projects/{id}", s.handleGetProject())
	mux.HandleFunc("PUT /api/v1/projects/{id}", s.handleUpdateProject())
	mux.HandleFunc("GET /api/v1/projects/{projectId}/workspace-status", s.handleWorkspaceStatus())

	mux.HandleFunc("GET /api/v1/projects/{projectId}/tasks", s.handleListProjectTasks())
	mux.HandleFunc("POST /api/v1/projects/{projectId}/tasks", s.handleCreateProjectTask())
	mux.HandleFunc("GET /api/v1/projects/{projectId}/tasks/{taskId}", s.handleGetProjectTask())
	mux.HandleFunc("PUT /api/v1/projects/{projectId}/tasks/{taskId}", s.handleUpdateProjectTask())

	mux.HandleFunc("GET /api/v1/projects/{projectId}/tasks/{taskId}/context", s.handleGetTaskContext())
	mux.HandleFunc("GET /api/v1/projects/{projectId}/tasks/{taskId}/plan", s.handleGetTaskPlan())
	mux.HandleFunc("GET /api/v1/projects/{projectId}/tasks/{taskId}/stages/{stage}/conversation", s.handleGetStageConversation())
	mux.HandleFunc("POST /api/v1/projects/{projectId}/tasks/{taskId}/stages/{stage}/start", s.handleStartStageConversation())
	mux.HandleFunc("POST /api/v1/projects/{projectId}/tasks/{taskId}/stages/{stage}/messages", s.handlePostStageMessage())
	mux.HandleFunc("DELETE /api/v1/projects/{projectId}/tasks/{taskId}/stages/{stage}/messages/{index}", s.handleDeleteStageMessage())
	mux.HandleFunc("POST /api/v1/projects/{projectId}/tasks/{taskId}/stages/{stage}/messages/{index}/regenerate", s.handleRegenerateStageMessage())
	mux.HandleFunc("POST /api/v1/projects/{projectId}/tasks/{taskId}/requirements/finalize", s.handleFinalizeRequirements())
	mux.HandleFunc("POST /api/v1/projects/{projectId}/tasks/{taskId}/plan/finalize", s.handleFinalizePlan())
	mux.HandleFunc("POST /api/v1/projects/{projectId}/tasks/{taskId}/review/finalize", s.handleFinalizeReview())
	mux.HandleFunc("POST /api/v1/projects/{projectId}/tasks/{taskId}/knowledge/finalize", s.handleFinalizeKnowledge())
	mux.HandleFunc("POST /api/v1/projects/{projectId}/tasks/{taskId}/requirements/revise", s.handleReviseRequirements())
	mux.HandleFunc("POST /api/v1/projects/{projectId}/tasks/{taskId}/plan/revise", s.handleRevisePlan())
	mux.HandleFunc("POST /api/v1/projects/{projectId}/tasks/{taskId}/execute", s.handleStartExecution())
	mux.HandleFunc("GET /api/v1/projects/{projectId}/tasks/{taskId}/executions", s.handleListExecutions())
	mux.HandleFunc("GET /api/v1/projects/{projectId}/tasks/{taskId}/reviews", s.handleListReviews())
	mux.HandleFunc("GET /api/v1/projects/{projectId}/tasks/{taskId}/review/diff", s.handleReviewDiff())
	mux.HandleFunc("POST /api/v1/projects/{projectId}/tasks/{taskId}/pr/push", s.handlePushPR())
	mux.HandleFunc("POST /api/v1/projects/{projectId}/tasks/{taskId}/pr/merged", s.handleMarkPRMerged())

	mux.HandleFunc("POST /api/v1/chat/completions", s.handleChatCompletions())
	mux.HandleFunc("POST /api/v1/chat/sessions/close", s.handleCloseChatSession())
	mux.HandleFunc("GET /api/v1/chat/models", s.handleListModels())
	mux.HandleFunc("GET /api/v1/agent-executors", s.handleListAgentExecutors())

	mux.Handle("GET /", newFrontendHandler(s.FrontendFS))

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

func (s *Server) handleHealthcheck() http.HandlerFunc {
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

		for name, runner := range s.AgentRunners {
			probe("agent:"+name, func() error { return runner.CheckHealth(r.Context()) })
		}

		resp := healthcheckResponse{Status: "ok", BuildId: s.BuildId, Subsystems: subsystems}
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

func (s *Server) handleVersion() http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		writeJSON(w, http.StatusOK, map[string]string{"version": s.BuildId})
	}
}

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
	"time"

	"github.com/sirupsen/logrus"

	"github.com/timmersuk/llm-workbench/internal/agentrunner"
	"github.com/timmersuk/llm-workbench/internal/knowledge"
	"github.com/timmersuk/llm-workbench/internal/project"
	"github.com/timmersuk/llm-workbench/internal/task"
)

// ProjectStore lists, retrieves, creates, and updates projects. Satisfied
// by *project.FileStore (a test fixture; no longer reachable in
// production) and, in production, *gitstore.ProjectStore
// (internal/gitstore), which wraps a *project.FileStore for the actual
// read/write and commits every Create/Update to git.
type ProjectStore interface {
	List() (project.ListResult, error)
	Get(id string) (project.Project, error)
	Create(in project.CreateInput) (project.Project, error)
	Update(id string, in project.UpdateInput) (project.Project, error)
}

// TaskStore lists, retrieves, creates, and updates tasks across every
// project (an explicit projectID names which one on every call — see
// task.Store's doc comment), plus the GrillMe/Planning Mode lifecycle: the
// derived context.yaml/plan.yaml artifacts, each stage's persisted
// Conversation, and the Finalize/Revise transitions (CONTEXT.md). A single
// process-wide singleton, not one instance per project. Satisfied by
// *task.FileStore (a test fixture; no longer reachable in production) and,
// in production, *gitstore.TaskStore (internal/gitstore), which wraps a
// *task.FileStore for the actual read/write and commits every mutating
// call to git.
type TaskStore interface {
	List(projectID string) (task.ListResult, error)
	Get(projectID, id string) (task.Task, error)
	Create(projectID string, t task.Task) (task.Task, error)
	Update(projectID, id string, t task.Task) (task.Task, error)

	GetContext(projectID, id string) (task.Context, error)
	GetPlan(projectID, id string) (task.Plan, error)
	GetConversation(projectID, id, stage string) (task.Conversation, error)
	AppendConversationMessages(projectID, id, stage string, msgs ...task.ConversationMessage) (task.Conversation, error)
	ReplaceConversationMessages(projectID, id, stage string, msgs []task.ConversationMessage) (task.Conversation, error)

	// GetSessionID/SetSessionID back a stage Conversation's durable,
	// per-executor session/thread id (agentrunner.RunInput.ResumeSessionID/
	// RunOutput.SessionID) — a sibling of conversation-<stage>.yaml itself
	// (conversation-<stage>.session.yaml), so a runner's real session/thread
	// resume survives an API process restart the same way the message
	// transcript already does. executor is the AgentRunners map key
	// ("claude-code" | "codex") — each runner only ever reads/writes its own
	// key, so switching executors mid-conversation just falls back to
	// systemPromptWithHistory for the other runner rather than misreading a
	// foreign session id as its own.
	GetSessionID(projectID, id, stage, executor string) (string, error)
	SetSessionID(projectID, id, stage, executor, sessionID string) error

	// GetTaskDraftConversation/AppendTaskDraftConversationMessages/
	// ReplaceTaskDraftConversationMessages back the task-drafts backend
	// (internal/api/task_draft_conversation.go) — the pre-creation "New
	// Task" chat's persisted transcript, keyed by a client-minted sessionID
	// rather than a task id (there is no task yet). Full parity with the
	// three Conversation methods above, just rooted at
	// <projectId>/task-drafts/<sessionID>/conversation.yaml instead of a
	// task's own directory (docs/task schema v0.md §1).
	GetTaskDraftConversation(projectID, sessionID string) (task.Conversation, error)
	AppendTaskDraftConversationMessages(projectID, sessionID string, msgs ...task.ConversationMessage) (task.Conversation, error)
	ReplaceTaskDraftConversationMessages(projectID, sessionID string, msgs []task.ConversationMessage) (task.Conversation, error)

	// GetTaskDraftSessionID/SetTaskDraftSessionID mirror GetSessionID/
	// SetSessionID for a task-drafts session (conversation.session.yaml,
	// keyed by project+sessionID rather than task+stage).
	GetTaskDraftSessionID(projectID, sessionID, executor string) (string, error)
	SetTaskDraftSessionID(projectID, sessionID, executor, value string) error

	FinalizeRequirements(projectID, id string, draft task.RequirementsDraft) (task.Task, error)
	FinalizePlan(projectID, id string, plan task.Plan) (task.Task, error)
	FinalizeReview(projectID, id string, draft task.ReviewDraft) (task.Task, error)
	MarkPRMerged(projectID, id string) (task.Task, error)
	// CompleteCleanup/SetCleanupStatus back the execution-worktree cleanup
	// flow MarkPRMerged now starts (task.StageCleanup): handleMarkPRMerged/
	// handleTaskCleanup persist the latest cleanup pass's per-worktree
	// report via SetCleanupStatus, then call CompleteCleanup once every
	// worktree is confirmed removed or already gone.
	CompleteCleanup(projectID, id string) (task.Task, error)
	SetCleanupStatus(projectID, id string, status []task.CleanupWorktreeStatus) (task.Task, error)
	RecordPullRequest(projectID, id string, pr task.PullRequest) (task.Task, error)
	ReviseToRequirements(projectID, id, reason string) (task.Task, error)
	ReviseToPlanning(projectID, id, reason string) (task.Task, error)

	// AppendKnowledgeActivity records one accept/reject decision from
	// Review's propose_knowledge Draft onto the task's audit trail
	// (handleFinalizeKnowledge, knowledge_draft.go) — never touches Stage.
	AppendKnowledgeActivity(projectID, id string, entry task.KnowledgeActivityEntry) (task.Task, error)

	NextExecutionID(projectID, id string) (string, error)
	RecordExecution(projectID, id string, exec task.Execution) (task.Execution, error)
	ListExecutions(projectID, id string) ([]task.Execution, error)
	ListReviews(projectID, id string) ([]task.Review, error)
	ListStageTransitions(projectID, id string) ([]task.StageTransition, error)

	CreateExecutionLog(projectID, id, executionID string) error
	AppendExecutionLogEvent(projectID, id, executionID string, ev task.ExecutionLogEvent) error
	GetExecutionLog(projectID, id, executionID string) (task.ExecutionLog, error)
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

// Server holds every dependency internal/api's handlers need that stays
// invariant for the lifetime of the server process — constructed once by
// NewRouter, with every handler (and the internal helpers that mix these
// same invariant deps with per-call data) as a method on it, rather than a
// free function re-closing over a fresh copy of the same parameter list
// each time. Package-internal by design: NewRouter is still the only public
// entrypoint (docs/adr/0016-api-handlers-become-methods-on-an-internal-server-struct.md).
type Server struct {
	Projects                      ProjectStore
	Tasks                         TaskStore
	KnowledgeStore                KnowledgeStore
	AgentRunners                  map[string]agentrunner.AgentRunner
	ReposRoot                     string
	PRClient                      agentrunner.GitHubPRClient
	DefaultBranchResolver         agentrunner.DefaultBranchResolver
	FrontendFS                    fs.FS
	BuildId                       string
	StageConversationSeedExecutor string
	ExecutionSeedExecutor         string
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
func NewRouter(projects ProjectStore, tasks TaskStore, knowledgeStore KnowledgeStore, agentRunners map[string]agentrunner.AgentRunner, reposRoot string, prClient agentrunner.GitHubPRClient, defaultBranchResolver agentrunner.DefaultBranchResolver, frontendFS fs.FS, buildId string) http.Handler {
	return newRouter(projects, tasks, knowledgeStore, agentRunners, reposRoot, prClient, defaultBranchResolver, frontendFS, buildId, "local", "claude-code")
}

func NewRouterWithSeeds(projects ProjectStore, tasks TaskStore, knowledgeStore KnowledgeStore, agentRunners map[string]agentrunner.AgentRunner, reposRoot string, prClient agentrunner.GitHubPRClient, defaultBranchResolver agentrunner.DefaultBranchResolver, frontendFS fs.FS, buildId, stageSeed, executionSeed string) http.Handler {
	return newRouter(projects, tasks, knowledgeStore, agentRunners, reposRoot, prClient, defaultBranchResolver, frontendFS, buildId, stageSeed, executionSeed)
}

func newRouter(projects ProjectStore, tasks TaskStore, knowledgeStore KnowledgeStore, agentRunners map[string]agentrunner.AgentRunner, reposRoot string, prClient agentrunner.GitHubPRClient, defaultBranchResolver agentrunner.DefaultBranchResolver, frontendFS fs.FS, buildId, stageSeed, executionSeed string) http.Handler {
	s := &Server{
		Projects:                      projects,
		Tasks:                         tasks,
		KnowledgeStore:                knowledgeStore,
		AgentRunners:                  agentRunners,
		ReposRoot:                     reposRoot,
		PRClient:                      prClient,
		DefaultBranchResolver:         defaultBranchResolver,
		FrontendFS:                    frontendFS,
		BuildId:                       buildId,
		StageConversationSeedExecutor: stageSeed,
		ExecutionSeedExecutor:         executionSeed,
	}

	mux := http.NewServeMux()

	mux.HandleFunc("GET /healthcheck", s.handleHealthcheck())
	mux.HandleFunc("GET /api/v1/version", s.handleVersion())

	mux.HandleFunc("GET /api/v1/projects", s.handleListProjects())
	mux.HandleFunc("POST /api/v1/projects", s.handleCreateProject())
	mux.HandleFunc("GET /api/v1/projects/{id}", s.handleGetProject())
	mux.HandleFunc("PUT /api/v1/projects/{id}", s.handleUpdateProject())
	mux.HandleFunc("GET /api/v1/projects/{projectId}/workspace-status", s.handleWorkspaceStatus())

	mux.HandleFunc("GET /api/v1/projects/{projectId}/task-drafts/{sessionId}/conversation", s.handleGetTaskDraftConversation())
	mux.HandleFunc("POST /api/v1/projects/{projectId}/task-drafts/{sessionId}/start", s.handleStartTaskDraftConversation())
	mux.HandleFunc("POST /api/v1/projects/{projectId}/task-drafts/{sessionId}/messages", s.handlePostTaskDraftMessage())
	mux.HandleFunc("DELETE /api/v1/projects/{projectId}/task-drafts/{sessionId}/messages/{index}", s.handleDeleteTaskDraftMessage())
	mux.HandleFunc("POST /api/v1/projects/{projectId}/task-drafts/{sessionId}/messages/{index}/regenerate", s.handleRegenerateTaskDraftMessage())

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
	mux.HandleFunc("GET /api/v1/projects/{projectId}/tasks/{taskId}/executions/continuable", s.handleGetContinuableExecution())
	mux.HandleFunc("GET /api/v1/projects/{projectId}/tasks/{taskId}/executions/{executionId}/log", s.handleGetExecutionLog())
	mux.HandleFunc("GET /api/v1/projects/{projectId}/tasks/{taskId}/reviews", s.handleListReviews())
	mux.HandleFunc("GET /api/v1/projects/{projectId}/tasks/{taskId}/stage-transitions", s.handleListStageTransitions())
	mux.HandleFunc("GET /api/v1/projects/{projectId}/tasks/{taskId}/review/diff", s.handleReviewDiff())
	mux.HandleFunc("POST /api/v1/projects/{projectId}/tasks/{taskId}/pr/push", s.handlePushPR())
	mux.HandleFunc("POST /api/v1/projects/{projectId}/tasks/{taskId}/pr/merged", s.handleMarkPRMerged())
	mux.HandleFunc("POST /api/v1/projects/{projectId}/tasks/{taskId}/cleanup", s.handleTaskCleanup())

	mux.HandleFunc("POST /api/v1/chat/completions", s.handleChatCompletions())
	mux.HandleFunc("POST /api/v1/chat/sessions/close", s.handleCloseChatSession())
	mux.HandleFunc("GET /api/v1/agent-executors", s.handleListAgentExecutors())

	mux.Handle("GET /", newFrontendHandler(s.FrontendFS))

	return loggingMiddleware(mux)
}

// statusRecorder wraps http.ResponseWriter to capture the status code
// actually written — net/http doesn't expose it after the fact, and
// loggingMiddleware needs it to log each request's real outcome. Defaults
// to 200: a handler that calls Write without ever calling WriteHeader gets
// an implicit 200 from net/http, so this mirrors that default rather than
// misreporting an unset status as 0.
type statusRecorder struct {
	http.ResponseWriter
	status int
}

func (r *statusRecorder) WriteHeader(status int) {
	r.status = status
	r.ResponseWriter.WriteHeader(status)
}

// Flush makes statusRecorder itself satisfy http.Flusher by delegating to
// the wrapped ResponseWriter, when that concrete writer supports it.
// Without this, every SSE handler's own `w.(http.Flusher)` type assertion
// (e.g. beginStageStream, stage_conversation.go) fails once wrapped:
// embedding the http.ResponseWriter interface only promotes the methods
// that interface itself declares, not Flush — a method the concrete
// http.ResponseWriter value underneath satisfies but the ResponseWriter
// interface type never mentions. Every streaming endpoint would otherwise
// 500 with "streaming unsupported" as soon as this middleware wraps it.
func (r *statusRecorder) Flush() {
	if f, ok := r.ResponseWriter.(http.Flusher); ok {
		f.Flush()
	}
}

// loggingMiddleware logs one structured line per request — method, path,
// status, duration — the request-level visibility this API otherwise has
// none of (every handler's own logging, e.g. stage_conversation.go's
// persistence-failure logs, is best-effort and handler-specific). Level
// follows the response status (Info for 2xx/3xx, Warn for 4xx, Error for
// 5xx) so scanning for Warn+ surfaces every failing request without
// wading through healthy traffic. Matches the WithFields structured-
// logging convention (docs/engineering conventions.md's Logging section)
// rather than a printf-style access-log line.
func loggingMiddleware(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		start := time.Now()
		rec := &statusRecorder{ResponseWriter: w, status: http.StatusOK}
		next.ServeHTTP(rec, r)

		fields := logrus.Fields{
			"method":      r.Method,
			"path":        r.URL.Path,
			"status":      rec.status,
			"duration_ms": time.Since(start).Milliseconds(),
		}
		switch {
		case rec.status >= http.StatusInternalServerError:
			logrus.WithFields(fields).Error("http request")
		case rec.status >= http.StatusBadRequest:
			logrus.WithFields(fields).Warn("http request")
		default:
			logrus.WithFields(fields).Info("http request")
		}
	})
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

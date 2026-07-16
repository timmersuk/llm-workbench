export interface TaskReferences {
  knowledge: string[]
  repo: string[]
}

export type TaskStatus = 'draft' | 'ready' | 'in_progress' | 'blocked' | 'failed' | 'complete'
export type TaskStage = 'requirements' | 'planning' | 'implementation' | 'review' | 'merged'

export interface PullRequest {
  url: string
  number: number
  branch: string
}

export interface Task {
  id: string
  title: string
  project: string
  status: TaskStatus
  stage: TaskStage
  created_at: string
  updated_at: string
  objective: string
  constraints: string[]
  assumptions: string[]
  success_criteria: string[]
  references: TaskReferences
  pull_request?: PullRequest
}

export interface Project {
  id: string
  name: string
  description: string
  repositories: string[]
  knowledge: string[]
  constraints: string[]
  created_at: string
  updated_at: string
}

// CreateTaskRequest is the body for creating a task within a project (the
// project itself comes from the URL, not this body). id is client-chosen
// and must be unique within that project. status/stage/objective/
// constraints/assumptions/success_criteria are deliberately absent: a task
// always starts at stage: requirements, status: draft (server-defaulted),
// and its requirements fields are set later via GrillMe's Finalize, not at
// creation (see CONTEXT.md's "Draft"/"Finalize").
export interface CreateTaskRequest {
  id: string
  title: string
  references: TaskReferences
}

// UpdateTaskRequest is the body for editing a task in place — its id and
// project are fixed by the URL and can never change.
export interface UpdateTaskRequest {
  title: string
  status: TaskStatus
  stage: TaskStage
  objective: string
  constraints: string[]
  assumptions: string[]
  success_criteria: string[]
  references: TaskReferences
}

// CreateProjectRequest/UpdateProjectRequest have no id: project ids are
// always server-derived by slugifying name.
export interface CreateProjectRequest {
  name: string
  description: string
  repositories: string[]
  knowledge: string[]
  constraints: string[]
}

export type UpdateProjectRequest = CreateProjectRequest

// LoadError describes a single task/project directory that failed to load
// during a list call, mirroring task.LoadError / project.LoadError in Go —
// one malformed entry is reported here rather than failing the whole call.
export interface LoadError {
  id: string
  error: string
}

export interface TaskListResult {
  tasks: Task[] | null
  errors: LoadError[] | null
}

export interface ProjectListResult {
  projects: Project[] | null
  errors: LoadError[] | null
}

export interface ModelsListResult {
  models: string[]
}

// AgentExecutorsListResult mirrors internal/api/agent_executors.go's
// handleListAgentExecutors response — every registered agentRunners entry
// that currently responds to a live CheckHealth probe (e.g. "claude-code",
// "local"). The same endpoint backs both StageConversationPanel and
// ChatPanel's executor pickers; StageConversationPanel filters out "local"
// client-side since selecting it there would never produce a Draft.
export interface AgentExecutorsListResult {
  executors: string[]
}

// ChatCompletionRequestBody mirrors internal/api/chat.go's
// chatCompletionRequest — the free-floating Chat tab's request shape.
// session_key is required: the server holds this conversation's history
// keyed by it (chat.ChatClient.StreamSessionTurn), so the client sends
// only the newest turn rather than resending full history every call.
export interface ChatCompletionRequestBody {
  content: string
  model: string
  executor: string
  session_key: string
  // history is normally omitted — the server holds this session's history
  // itself. It's only populated by the frontend immediately after a
  // delete/edit/regenerate action closes the session (closeChatSession):
  // sending it right after an eviction is what makes the correction stick,
  // since the server has no other durable copy of free chat's history.
  history?: ChatHistoryEntry[]
}

// ChatHistoryEntry is the minimal wire shape internal/api/chat.go's
// chatCompletionRequest.History needs — role + content only, no tool-call
// structure, since free chat never registers tools.
export interface ChatHistoryEntry {
  role: string
  content: string
}

// ChatStreamEvent mirrors internal/api/chat.go's chatStreamEvent — one
// incremental piece of a streamed chat completion. content, reasoning_
// content, tool_call, and tool_activity are never more than one set on the
// same event; error is only set on the final event of a stream that failed
// partway through. tool_call (the single final Draft) and tool_activity
// (the agent's intermediate executed tool calls/results) are only populated
// on the stage-conversation endpoints — the free-floating chat endpoint
// never registers tools, so never sets either, but shares this event shape.
// Rendering tool_activity in a panel (ReviewPanel) is deferred to a later
// PR; this contract is carried now so the stream stays typed.
export interface ChatStreamEvent {
  content?: string
  reasoning_content?: string
  tool_call?: { name: string; arguments: string }
  tool_activity?: {
    phase: 'call' | 'result'
    name: string
    arguments?: string
    result?: string
    is_error?: boolean
  }
  error?: string
}

// VerificationKind mirrors task.VerificationKind* (internal/task/context.go)
// — who performs a verification step: agent_executable (the reviewing agent
// attempts it directly) or human_judgment (the human performs it, the agent
// only records their confirmation). See docs/adr/0008.
export type VerificationKind = 'agent_executable' | 'human_judgment'

// VerificationStep mirrors task.VerificationStep (internal/task/context.go) —
// one entry in context.yaml's verification list: a human-readable description
// plus a kind classifying who performs it.
export interface VerificationStep {
  description: string
  kind: VerificationKind
}

// TaskContext mirrors task.Context (internal/task/context.go) —
// context.yaml, GrillMe's Finalize output alongside the requirements
// fields on Task itself.
export interface TaskContext {
  summary: string
  background: string
  files: string[]
  detail: string
  verification: VerificationStep[]
  open_questions: string[]
}

// TaskPlan mirrors task.Plan (internal/task/plan.go) — plan.yaml, Planning
// Mode's Finalize output.
export interface TaskPlan {
  approach: string
  steps: string[]
  risks: string[]
  estimated_complexity: 'low' | 'medium' | 'high' | ''
  recommended_executor: string
}

// RequirementsDraft mirrors task.RequirementsDraft (internal/task/context.go)
// — the wire shape for GrillMe's Draft (both the task.yaml-subset fields it
// owns and the full TaskContext), used both to render an editable Draft
// form and as the body finalizeRequirements posts.
export interface RequirementsDraft {
  objective: string
  constraints: string[]
  assumptions: string[]
  success_criteria: string[]
  context: TaskContext
}

// ConversationToolCall/ConversationMessage/Conversation mirror
// internal/task/conversation.go — a stage's persisted, append-only message
// history (CONTEXT.md's "Conversation").
export interface ConversationToolCall {
  id: string
  name: string
  arguments: string
}

export interface ConversationMessage {
  role: string
  content: string
  tool_call?: ConversationToolCall
  tool_call_id?: string
  // error is set when this turn failed — a reload must still show that a
  // failure happened, not just an assistant message with empty content and
  // no explanation (internal/task/conversation.go's ConversationMessage.Error).
  error?: string
  created_at: string
}

// messages is nullable because Go's zero-value nil slice (a stage nobody
// has chatted in yet — internal/task/conversation.go's GetConversation)
// marshals to JSON null, not [] — same convention as TaskListResult.tasks/
// ProjectListResult.projects below.
export interface Conversation {
  stage: string
  messages: ConversationMessage[] | null
}

// ExecutionExecutor/ExecutionInput/ExecutionOutput/ExecutionMetrics/
// ExecutionFailure/Execution mirror internal/task/execution.go — one
// Implementation-stage execution attempt, stored as
// executions/<execution_id>.yaml. tokens_used/cost_estimate are 0 when the
// executor didn't report them, not fabricated.
export interface ExecutionExecutor {
  type: string
  version: string
}

export interface ExecutionInput {
  plan_ref: string
  context_refs: string[]
  review_feedback: string
}

export interface ExecutionOutput {
  artifacts: string[]
  git_branch: string
  commits: string[]
  forked_from_branch: string
}

export interface ExecutionMetrics {
  duration_seconds: number
  tokens_used: number
  cost_estimate: number
}

export interface ExecutionFailure {
  type: string
  message: string
}

export interface Execution {
  execution_id: string
  task_id: string
  executor: ExecutionExecutor
  input: ExecutionInput
  output: ExecutionOutput
  metrics: ExecutionMetrics
  status: 'success' | 'failure' | 'partial'
  failure?: ExecutionFailure
  created_at: string
}

// ExecutionsListResult mirrors handleListExecutions' response
// (internal/api/execution.go) — every recorded attempt for a task, oldest
// first. executions is nullable for the same reason Conversation.messages
// is: a task with no attempts yet has no executions/ directory at all.
export interface ExecutionsListResult {
  executions: Execution[] | null
}

// ExecuteStreamEvent mirrors internal/api/execution.go's
// executeStreamEvent — unlike ChatStreamEvent's flat "at most one field
// set" shape, this is a discriminated union on `type`: a single execution
// run can produce many tool_call/tool_result events, not at most one tool
// call per turn. `execution` is set only on the final "done" event, so the
// frontend learns the outcome without a second request.
export interface ExecuteStreamEvent {
  type: 'text' | 'tool_call' | 'tool_result' | 'error' | 'done'
  content?: string
  tool_name?: string
  tool_input?: string
  tool_result?: string
  is_error?: boolean
  error?: string
  execution?: Execution
}

// ReviewDecision/Review/ReviewDraft mirror internal/task/review.go — one
// review verdict, stored append-only as reviews/<review_id>.yaml. The
// decision drives the stage transition on Finalize: approved → complete,
// needs_changes → implementation, rejected → requirements.
export type ReviewDecision = 'approved' | 'rejected' | 'needs_changes'

export interface Review {
  review_id: string
  task_id: string
  execution_id: string
  decision: ReviewDecision
  notes: string
  created_at: string
}

// ReviewDraft is the propose_review tool-call / Review Finalize body — the
// three-way decision plus notes, never persisted as this exact shape
// (finalizeReview turns it into a Review). Mirrors task.ReviewDraft.
export interface ReviewDraft {
  decision: ReviewDecision
  notes: string
}

// FinalizeReviewResponse mirrors internal/api finalizeReviewResponse — the
// task (moved by the verdict) plus the review-NNN.yaml just recorded.
export interface FinalizeReviewResponse {
  task: Task
  review: Review
}

// ReviewsListResult mirrors handleListReviews' response — every verdict for a
// task, oldest first. Nullable for the same reason executions is: a task with
// no reviews yet has no reviews/ directory.
export interface ReviewsListResult {
  reviews: Review[] | null
}

// ReviewDiffResult mirrors handleReviewDiff's response — the raw patch of the
// execution under review. The branch/commit/file summary shown alongside comes
// from the executions list; this carries only the (potentially large) diff.
export interface ReviewDiffResult {
  patch: string
}

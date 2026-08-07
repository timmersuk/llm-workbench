export interface TaskReferences {
  knowledge: string[]
  repo: string[]
}

// 'cleanup' mirrors task.StageCleanup (internal/task/task.go): "Mark as
// merged" now lands here first, not straight on 'completed' — the execution-
// worktree cleanup routine runs synchronously and only advances the task
// the rest of the way once every worktree is confirmed removed or already
// gone. A task only "sticks" at 'cleanup' when CleanupWorktreeStatus records
// at least one skipped/failed worktree (CleanupPanel.tsx).
export type TaskStage = 'requirements' | 'planning' | 'implementation' | 'review' | 'pr_review' | 'cleanup' | 'completed'

export interface PullRequest {
  url: string
  number: number
  branch: string
}

// KnowledgeActivityAction mirrors task.KnowledgeActivityAction*
// (internal/task/task.go).
export type KnowledgeActivityAction = 'created' | 'updated' | 'rejected'

// KnowledgeActivityEntry mirrors task.KnowledgeActivityEntry — one row of
// Task.knowledge_activity, appended whenever a human accepts or rejects a
// Review-stage propose_knowledge Draft (handleFinalizeKnowledge,
// internal/api/knowledge_draft.go). Purely an audit trail: it never
// affects the task's stage.
export interface KnowledgeActivityEntry {
  concept_id: string
  type?: string
  action: KnowledgeActivityAction
  created_at: string
}

// CleanupWorktreeOutcome mirrors task.CleanupOutcome* (internal/task/task.go).
export type CleanupWorktreeOutcome = 'removed' | 'already-gone' | 'skipped' | 'failed'

// CleanupWorktreeStatus mirrors task.CleanupWorktreeStatus
// (internal/task/task.go) — one execution attempt's outcome from the most
// recent cleanup pass, rendered by CleanupPanel.tsx.
export interface CleanupWorktreeStatus {
  execution_id: string
  outcome: CleanupWorktreeOutcome
  reason?: string
}

export interface Task {
  id: string
  title: string
  project: string
  stage: TaskStage
  created_at: string
  updated_at: string
  objective: string
  constraints: string[]
  assumptions: string[]
  success_criteria: string[]
  references: TaskReferences
  agent_defaults?: AgentDefaults
  pull_request?: PullRequest
  // cleanup_status mirrors task.Task.CleanupStatus (internal/task/task.go)
  // — the latest execution-worktree cleanup pass's per-attempt report,
  // absent until a task first reaches stage 'cleanup'. Overwritten
  // wholesale by every subsequent pass (a retry/force re-run), not
  // append-only.
  cleanup_status?: CleanupWorktreeStatus[]
  // draft_session_id mirrors task.Task.DraftSessionID (internal/task/task.go)
  // — set at creation time when this task came from the chat-driven "New
  // Task" flow, pointing at the frozen pre-creation conversation
  // (task-drafts/<draft_session_id>/conversation.yaml). Absent for a task
  // created before this mechanism existed. TaskDetailPanel only shows its
  // "View pre-creation conversation" nav link when this is set.
  draft_session_id?: string
  // knowledge_activity mirrors task.Task.KnowledgeActivity — every
  // knowledge concept this task's Review conversation proposed and a human
  // then accepted or rejected. Absent/empty for a task that has never had
  // one proposed.
  knowledge_activity?: KnowledgeActivityEntry[]
}

export type ReasoningEffort = 'low' | 'medium' | 'high'
export interface AgentSelection { executor: string; model: string; effort: ReasoningEffort }
export interface AgentDefaults { stage_conversation: AgentSelection; execution: AgentSelection }
export interface ExecutorCapability {
  name: string
  models: string[]
  efforts: ReasoningEffort[]
  default_model: string
  default_effort: ReasoningEffort
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
// and must be unique within that project; stage is never sent — a task
// always starts at stage: requirements (server-defaulted). Widened beyond
// TaskForm's old id/title/references-only shape once task creation became
// chat-driven (propose_task, internal/drafttool): the human's confirmed (or
// edited) draft can already carry the requirements fields propose_task
// proposed, rather than always leaving them for GrillMe's Finalize to set
// afterward — objective/constraints/assumptions/success_criteria are
// therefore optional here (a human could still confirm a bare id+title and
// let GrillMe fill in the rest, same as before this flow existed), not
// required. draft_session_id, when the task came from that chat, points
// back at the task-drafts session whose conversation produced it — a
// permanent pointer set once at creation (task.Task.DraftSessionID);
// backend Create passes every field on this request straight into
// task.Task via json.Decode, so no corresponding Go-side request struct
// exists to keep in sync.
export interface CreateTaskRequest {
  id: string
  title: string
  references: TaskReferences
  objective?: string
  constraints?: string[]
  assumptions?: string[]
  success_criteria?: string[]
  draft_session_id?: string
}

// UpdateTaskRequest is the body for editing a task in place — its id,
// project, and stage are fixed by the URL/workflow and can never change
// here. Stage only ever moves through the dedicated Finalize/Revise
// actions (see finalizeRequirements/finalizePlan/finalizeReview/etc.
// below), never through this generic update.
export interface UpdateTaskRequest {
  title: string
  objective: string
  constraints: string[]
  assumptions: string[]
  success_criteria: string[]
  references: TaskReferences
  agent_defaults?: AgentDefaults
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

// ModelsListResult.efforts/default_effort mirror the matching
// ExecutorCapability entry listModels resolves against (api.ts) — present
// whenever that executor is found in the consolidated capabilities
// response, so callers can dynamically filter their effort picker the same
// way they already filter models, instead of offering a static
// low/medium/high list regardless of what the selected executor actually
// supports. Left optional (rather than always populated) so existing test
// fixtures that construct a bare {models: [...]} literal keep compiling and
// behaving the same — callers fall back to the full ReasoningEffort set
// when absent.
export interface ModelsListResult {
  models: string[]
  efforts?: ReasoningEffort[]
  default_effort?: ReasoningEffort
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

export interface ExecutorCapabilitiesResult { executors: ExecutorCapability[] }

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
  effort?: ReasoningEffort
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
// content, tool_call, tool_activity, and usage are never more than one set
// on the same event; error is only set on the final event of a stream that
// failed partway through. tool_call (the single final Draft) and
// tool_activity (the agent's intermediate executed tool calls/results) are
// only populated on the stage-conversation endpoints — the free-floating
// chat endpoint never registers tools, so never sets either, but shares
// this event shape. tool_activity is rendered live in StageConversationPanel
// as it streams in, then collapses to a single summary once the turn ends
// (docs/adr/0018; CONTEXT.md's "Tool Activity"). usage carries the real
// token total once available (only ever on a stream's final chunk) — the
// live token-estimate indicator snaps to it rather than staying an
// approximation forever.
export interface ChatStreamEvent {
  content?: string
  reasoning_content?: string
  tool_call?: { name: string; arguments: string }
  tool_activity?: {
    // id correlates a "call" phase event to its later "result" — see
    // ToolActivityEntry's doc comment (frontend/src/ToolActivity.tsx).
    id: string
    phase: 'call' | 'result'
    name: string
    arguments?: string
    result?: string
    is_error?: boolean
  }
  // permission_request is set when a human-paced stage turn escalates a tool
  // the agent can see but not auto-run (docs/adr/0024); the turn blocks until
  // the human posts an allow/deny to the stage /permission endpoint.
  permission_request?: {
    id: string
    name: string
    arguments?: string
  }
  usage?: { total_tokens: number }
  error?: string
  executor?: string
  model?: string
  effort?: ReasoningEffort
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

// ContextFile mirrors task.ContextFile (internal/task/context.go) — one
// entry in context.yaml's files list: a repo-relative path plus an
// optional note on why it matters to this task (docs/adr/0021).
export interface ContextFile {
  path: string
  role: string
}

// TaskContext mirrors task.Context (internal/task/context.go) —
// context.yaml, GrillMe's Finalize output alongside the requirements
// fields on Task itself.
export interface TaskContext {
  summary: string
  background: string
  files: ContextFile[]
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

// TaskDraft mirrors the propose_task tool's argument shape
// (internal/drafttool/drafttool.go's proposeTaskSchema) — the pre-creation
// "New Task" chat's Draft: a fully-populated draft task.yaml (minus the
// server-set id/project/stage/timestamps... except id itself, which the
// human is expected to review or override before creation, so it travels
// here unlike a real task.yaml's server-set fields). Rendered by
// TaskDraftForm.tsx for the human to edit before NewTaskPanel.tsx calls
// createProjectTask with it.
export interface TaskDraft {
  id: string
  title: string
  objective: string
  constraints: string[]
  assumptions: string[]
  success_criteria: string[]
  references: TaskReferences
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

// ConversationToolActivity mirrors internal/task/conversation.go's
// ConversationToolActivity — one intermediate tool call and its result from
// a past turn (CONTEXT.md's "Tool Activity"), already capped server-side
// (docs/adr/0018) before being persisted.
export interface ConversationToolActivity {
  name: string
  arguments?: string
  result?: string
  is_error?: boolean
}

// ConversationSegment mirrors internal/task/conversation.go's
// ConversationSegment (docs/adr/0023) — one piece of a turn's real
// chronological sequence, either a run of narration or a maximal run of
// consecutive tool calls (a "sequence", CONTEXT.md's Tool Activity). The
// server always populates this on every ConversationMessage it returns
// (synthesizing a best-effort [tools, text] pair for a turn recorded before
// this field existed — real order isn't recoverable retroactively), but the
// type is kept optional here so pre-existing test fixtures that construct a
// bare ConversationMessage literal without it don't need updating.
export type ConversationSegment =
  | { kind: 'text'; text: string }
  | { kind: 'tools'; tool_activity: ConversationToolActivity[] }

export interface ConversationMessage {
  role: string
  content: string
  // tool_call is tool_calls[0] when the turn proposed at least one Draft —
  // kept for a caller that only ever expects one Draft-proposing tool call
  // per turn. tool_calls carries every one a turn proposed, in order; a
  // stage that offers more than one Draft-proposing tool at once (Review's
  // propose_review + propose_knowledge, stageTool in
  // internal/api/stage_conversation.go) can have the model call more than
  // one in the same turn — tool_call alone used to silently lose every
  // call after the first (internal/task/conversation.go's
  // ConversationMessage.EffectiveToolCalls doc comment has the full
  // story). Always populated by the server (synthesized from the legacy
  // singular field for a message recorded before tool_calls existed), same
  // guarantee as segments below.
  tool_call?: ConversationToolCall
  tool_calls?: ConversationToolCall[]
  tool_call_id?: string
  tool_activity?: ConversationToolActivity[]
  segments?: ConversationSegment[]
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
  model?: string
  effort?: ReasoningEffort
}

export interface ExecutionInput {
  plan_ref: string
  context_refs: string[]
  review_feedback: string
  continued_from_execution_id: string
}

export interface ExecutionOutput {
  artifacts: string[]
  git_branch: string
  commits: string[]
  forked_from_branch: string
  workspace_dirty: boolean
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

// ContinuableExecutionResult mirrors handleGetContinuableExecution's
// response (internal/api/execution.go) — execution_id is empty when
// there's nothing eligible to continue from right now (resolveFailureContinuation).
export interface ContinuableExecutionResult {
  execution_id: string
}

// ExecutionLogEvent/ExecutionLog mirror internal/task/execution_log.go's
// ExecutionLogEvent/ExecutionLog — one execution attempt's full, untruncated
// recorded event stream (executions/exec-NNN.log.yaml), fetched on demand via
// getExecutionLog rather than bundled into ExecutionsListResult.
export interface ExecutionLogEvent {
  // id correlates a "tool_call" event to its later "tool_result" — see
  // ToolActivityEntry's doc comment (frontend/src/ToolActivity.tsx).
  // Persisted (unlike the live-only ids elsewhere), since a replayed log
  // has no other structure to attach a result to its real call.
  id?: string
  kind: 'text' | 'tool_call' | 'tool_result'
  text?: string
  tool_name?: string
  tool_input?: string
  tool_result?: string
  is_error?: boolean
  created_at: string
}

// backfilled is true only for the handful of executions recorded before
// this capture feature existed, whose logs a one-off migration
// reconstructed from the executor's own archived session transcript rather
// than capturing them live as the run happened.
export interface ExecutionLog {
  execution_id: string
  backfilled?: boolean
  events: ExecutionLogEvent[]
}

// ExecuteStreamEvent mirrors internal/api/execution.go's
// executeStreamEvent — unlike ChatStreamEvent's flat "at most one field
// set" shape, this is a discriminated union on `type`: a single execution
// run can produce many tool_call/tool_result events, not at most one tool
// call per turn. `execution` is set only on the final "done" event, so the
// frontend learns the outcome without a second request.
export interface ExecuteStreamEvent {
  type: 'text' | 'tool_call' | 'tool_result' | 'error' | 'done'
  // id is set on "tool_call"/"tool_result" — see ToolActivityEntry's doc
  // comment (frontend/src/ToolActivity.tsx).
  id?: string
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

// KnowledgeConceptDraftDecision mirrors internal/api's knowledgeDecision*
// constants (knowledge_draft.go) — two-way, unlike ReviewDecision's three:
// a knowledge proposal has no prior execution branch for a "needs_changes"
// state to continue from, so a rejected proposal is just more conversation,
// not a state the backend records (docs/milestones/done/milestone9.md).
export type KnowledgeConceptDraftDecision = 'accepted' | 'rejected'

// KnowledgeConceptDraft is the propose_knowledge tool-call / knowledge
// Finalize body — always the concept's full resulting content (never a
// diff), covering both a brand-new concept_id and an edit to an existing
// one with the same shape. Mirrors internal/api's finalizeKnowledgeRequest
// minus its decision field, which travels as finalizeKnowledge's separate
// parameter instead (so KnowledgeDraftForm's onChange never has to know
// about the decision being made around it).
export interface KnowledgeConceptDraft {
  concept_id: string
  type: string
  frontmatter?: Record<string, unknown>
  body: string
}

// FinalizeKnowledgeResponse mirrors internal/api finalizeKnowledgeResponse.
// concept is only populated on an accepted decision. task is the task with
// this decision's entry already appended to knowledge_activity —
// best-effort (absent if recording it failed server-side) — letting a
// caller refresh its own copy of the task and show the new log entry
// immediately, without a second GET. note is the exact acknowledgment text
// the server appended to the Review Conversation (also best-effort, empty
// if that append failed) — letting a caller show the same note inline in
// the live transcript without re-fetching it.
export interface FinalizeKnowledgeResponse {
  concept_id: string
  decision: KnowledgeConceptDraftDecision
  concept?: { type: string; frontmatter?: Record<string, unknown>; body: string }
  task?: Task
  note?: string
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

// StageTransitionTrigger mirrors task.TransitionTrigger* (internal/task/stage_transition.go)
// — the fixed vocabulary of ways a task's Stage can move.
export type StageTransitionTrigger =
  | 'finalize_requirements'
  | 'finalize_plan'
  | 'finalize_review'
  | 'execution_success'
  | 'mark_pr_merged'
  | 'revise_requirements'
  | 'revise_planning'
  | 'cleanup_complete'

// StageTransition mirrors task.StageTransition (internal/task/stage_transition.go)
// — one Stage move, appended to transitions.yaml. review_id/execution_id link
// to the review/execution that drove the move; reason is only ever set on a
// revise_* transition, and only when the human supplied one. TimelinePanel.tsx
// renders these merged with the full reviews list to show a task's real path
// through its lifecycle, including reversals.
export interface StageTransition {
  task_id: string
  from_stage: string
  to_stage: string
  trigger: StageTransitionTrigger
  review_id?: string
  execution_id?: string
  reason?: string
  created_at: string
}

// StageTransitionsListResult mirrors handleListStageTransitions' response —
// every transition for a task, oldest first. Nullable for the same reason
// executions/reviews are: a task with no transitions yet has no
// transitions.yaml at all.
export interface StageTransitionsListResult {
  stage_transitions: StageTransition[] | null
}

// BehindOriginStatus/DirtyStatus mirror gitutil.BehindOriginStatus/DirtyStatus
// (internal/gitutil/gitutil.go) — known is false when the check couldn't be
// completed (offline, checkout doesn't exist yet, no upstream configured,
// ...), in which case behind/dirty are meaningless, not "false."
export interface BehindOriginStatus {
  known: boolean
  behind: number
}

export interface DirtyStatus {
  known: boolean
  dirty: boolean
}

// WorkspaceStatusResult mirrors handleWorkspaceStatus's response
// (internal/api/workspace_status.go) — live git-derived advisory status for
// a project's shared checkout (docs/milestones/done/milestone8a.md's "Advisory
// checks"). repository_configured is false when the project has no
// repositories configured at all.
export interface WorkspaceStatusResult {
  repository_configured: boolean
  status: {
    behind_origin: BehindOriginStatus
    dirty: DirtyStatus
  }
}

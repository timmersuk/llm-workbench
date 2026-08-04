import type {
  AgentExecutorsListResult,
  ChatCompletionRequestBody,
  ChatHistoryEntry,
  ChatStreamEvent,
  ContinuableExecutionResult,
  Conversation,
  CreateProjectRequest,
  CreateTaskRequest,
  ExecuteStreamEvent,
  ExecutionLog,
  ExecutionsListResult,
  FinalizeKnowledgeResponse,
  FinalizeReviewResponse,
  KnowledgeConceptDraft,
  KnowledgeConceptDraftDecision,
  ModelsListResult,
  Project,
  ProjectListResult,
  RequirementsDraft,
  ReviewDiffResult,
  ReviewDraft,
  ReviewsListResult,
  StageTransitionsListResult,
  Task,
  TaskContext,
  TaskListResult,
  TaskPlan,
  UpdateProjectRequest,
  UpdateTaskRequest,
  WorkspaceStatusResult,
} from './types'

// isAbortError reports whether err is the DOMException fetch/streamSSE
// throws when an AbortController tied to the request's signal was aborted
// (e.g. the user clicked Stop mid-stream) — callers use this to distinguish
// a deliberate cancellation from a real network/server failure so they
// don't surface it as an error message.
export function isAbortError(err: unknown): boolean {
  return err instanceof DOMException && err.name === 'AbortError'
}

// GET_TIMEOUT_MS bounds every getJSON call — these are all quick lookups
// (project/task metadata, model and executor lists), so a server that's
// merely slow to answer isn't the expected case. Without this, a hung
// backend (unreachable, deadlocked, paused in a debugger) never rejects the
// fetch promise at all, so a caller's .catch() never fires either — a
// picker like listAgentExecutors would sit at its empty-state default
// forever with no sign anything's wrong, rather than surfacing an error.
const GET_TIMEOUT_MS = 15000

// parseAPIError extracts the human-readable message from a failed
// response, preferring real information over a generic status line:
// writeAPIError's {"error":{"code","message"}} envelope
// (internal/api/json.go) if the body parses as that shape; the raw text
// otherwise, provided there is some (a handler that failed before reaching
// writeAPIError, or a non-API error page — still worth showing verbatim
// rather than swallowing); fallback only when the body is empty, or valid
// JSON that isn't this envelope (a generic dump like `{}` is less useful
// than the fallback line). Never throws itself, so every caller can safely
// await it inside its own throw.
async function parseAPIError(res: Response, fallback: string): Promise<string> {
  const text = await res.text().catch(() => '')
  if (!text) {
    return fallback
  }
  try {
    const parsed = JSON.parse(text) as { error?: { message?: string } }
    return parsed.error?.message || fallback
  } catch {
    return text
  }
}

async function getJSON<T>(path: string): Promise<T> {
  let res: Response
  try {
    res = await fetch(path, { signal: AbortSignal.timeout(GET_TIMEOUT_MS) })
  } catch (err) {
    if (err instanceof DOMException && err.name === 'TimeoutError') {
      throw new Error(`${path} timed out after ${GET_TIMEOUT_MS / 1000}s — is the server running?`)
    }
    throw err
  }
  if (!res.ok) {
    throw new Error(await parseAPIError(res, `${path} returned ${res.status}`))
  }
  return res.json() as Promise<T>
}

async function mutateJSON<T>(method: 'POST' | 'PUT', path: string, body: unknown): Promise<T> {
  const res = await fetch(path, {
    method,
    headers: { 'Content-Type': 'application/json' },
    body: JSON.stringify(body),
  })
  if (!res.ok) {
    throw new Error(await parseAPIError(res, `${method} ${path} returned ${res.status}`))
  }
  return res.json() as Promise<T>
}

export function listProjects(): Promise<ProjectListResult> {
  return getJSON<ProjectListResult>('/api/v1/projects')
}

// getProject fetches a single project by id — used to resolve a deep-linked
// or reloaded /projects/:projectId URL whose id isn't (yet) present in an
// already-loaded projects list.
export function getProject(id: string): Promise<Project> {
  return getJSON<Project>(`/api/v1/projects/${encodeURIComponent(id)}`)
}

export function createProject(req: CreateProjectRequest): Promise<Project> {
  return mutateJSON<Project>('POST', '/api/v1/projects', req)
}

export function updateProject(id: string, req: UpdateProjectRequest): Promise<Project> {
  return mutateJSON<Project>('PUT', `/api/v1/projects/${encodeURIComponent(id)}`, req)
}

export function listProjectTasks(projectId: string): Promise<TaskListResult> {
  return getJSON<TaskListResult>(`/api/v1/projects/${encodeURIComponent(projectId)}/tasks`)
}

export function getProjectTask(projectId: string, taskId: string): Promise<Task> {
  return getJSON<Task>(`/api/v1/projects/${encodeURIComponent(projectId)}/tasks/${encodeURIComponent(taskId)}`)
}

export function createProjectTask(projectId: string, req: CreateTaskRequest): Promise<Task> {
  return mutateJSON<Task>('POST', `/api/v1/projects/${encodeURIComponent(projectId)}/tasks`, req)
}

export function updateProjectTask(projectId: string, taskId: string, req: UpdateTaskRequest): Promise<Task> {
  return mutateJSON<Task>(
    'PUT',
    `/api/v1/projects/${encodeURIComponent(projectId)}/tasks/${encodeURIComponent(taskId)}`,
    req,
  )
}

function taskPath(projectId: string, taskId: string): string {
  return `/api/v1/projects/${encodeURIComponent(projectId)}/tasks/${encodeURIComponent(taskId)}`
}

export function getTaskContext(projectId: string, taskId: string): Promise<TaskContext> {
  return getJSON<TaskContext>(`${taskPath(projectId, taskId)}/context`)
}

export function getTaskPlan(projectId: string, taskId: string): Promise<TaskPlan> {
  return getJSON<TaskPlan>(`${taskPath(projectId, taskId)}/plan`)
}

export function getStageConversation(projectId: string, taskId: string, stage: string): Promise<Conversation> {
  return getJSON<Conversation>(`${taskPath(projectId, taskId)}/stages/${encodeURIComponent(stage)}/conversation`)
}

export interface FinalizeRequirementsResponse {
  task: Task
  context: TaskContext
}

export interface FinalizePlanResponse {
  task: Task
  plan: TaskPlan
}

export function finalizeRequirements(projectId: string, taskId: string, draft: RequirementsDraft): Promise<FinalizeRequirementsResponse> {
  return mutateJSON<FinalizeRequirementsResponse>('POST', `${taskPath(projectId, taskId)}/requirements/finalize`, draft)
}

export function finalizePlan(projectId: string, taskId: string, plan: TaskPlan): Promise<FinalizePlanResponse> {
  return mutateJSON<FinalizePlanResponse>('POST', `${taskPath(projectId, taskId)}/plan/finalize`, plan)
}

export function finalizeReview(projectId: string, taskId: string, draft: ReviewDraft): Promise<FinalizeReviewResponse> {
  return mutateJSON<FinalizeReviewResponse>('POST', `${taskPath(projectId, taskId)}/review/finalize`, draft)
}

// finalizeKnowledge is the human's accept/reject decision on a
// propose_knowledge Draft (docs/milestones/done/milestone9.md) — unlike
// finalizeReview, this never changes the task's stage; the Review
// conversation continues regardless of what's decided here.
export function finalizeKnowledge(
  projectId: string,
  taskId: string,
  draft: KnowledgeConceptDraft,
  decision: KnowledgeConceptDraftDecision,
): Promise<FinalizeKnowledgeResponse> {
  return mutateJSON<FinalizeKnowledgeResponse>('POST', `${taskPath(projectId, taskId)}/knowledge/finalize`, { ...draft, decision })
}

// listReviews returns every recorded verdict for a task, oldest first — the
// terminal "merged" screen reads the latest to show its notes on a fresh
// visit (ReviewsListResult).
export function listReviews(projectId: string, taskId: string): Promise<ReviewsListResult> {
  return getJSON<ReviewsListResult>(`${taskPath(projectId, taskId)}/reviews`)
}

// listStageTransitions returns every recorded Stage move for a task, oldest
// first (internal/api/stage_transitions.go's handleListStageTransitions) —
// TimelinePanel.tsx merges this with the full reviews list to show the
// task's real path through its lifecycle, including reversals.
export function listStageTransitions(projectId: string, taskId: string): Promise<StageTransitionsListResult> {
  return getJSON<StageTransitionsListResult>(`${taskPath(projectId, taskId)}/stage-transitions`)
}

// getReviewDiff returns the raw patch of the task's most recent execution —
// the diff ReviewPanel (and PRReviewPanel) render per-file via DiffView
// before the review conversation starts. 404s when there's no execution
// yet, which the panel treats as "no diff to show" rather than an error.
export function getReviewDiff(projectId: string, taskId: string): Promise<ReviewDiffResult> {
  return getJSON<ReviewDiffResult>(`${taskPath(projectId, taskId)}/review/diff`)
}

// getWorkspaceStatus reports the project's shared checkout's current
// advisory hygiene status — project-scoped, not task-scoped, since the
// shared checkout is one per project.
export function getWorkspaceStatus(projectId: string): Promise<WorkspaceStatusResult> {
  return getJSON<WorkspaceStatusResult>(`/api/v1/projects/${encodeURIComponent(projectId)}/workspace-status`)
}

// pushPR is the "Push & Open PR" action for pr_review: pushes the task's
// latest execution branch and opens (or continues) a PR, returning the
// updated Task now carrying pull_request. Empty body — everything the
// backend needs is derived server-side from the task/execution/review
// (docs/milestones/done/milestone7.md PR 3).
export function pushPR(projectId: string, taskId: string): Promise<Task> {
  return mutateJSON<Task>('POST', `${taskPath(projectId, taskId)}/pr/push`, {})
}

// markPRMerged is the "Mark as merged" action for pr_review: a human
// assertion that the PR was merged on GitHub, advancing the task straight
// to merged with no review-record write.
export function markPRMerged(projectId: string, taskId: string): Promise<Task> {
  return mutateJSON<Task>('POST', `${taskPath(projectId, taskId)}/pr/merged`, {})
}

// reviseRequirements/revisePlan take an optional, skippable reason — a
// short human-typed explanation for why the task is going back (e.g. "the
// plan missed X"), recorded on the resulting StageTransition. Omitted
// (undefined) or empty means no reason given, not an error.
export function reviseRequirements(projectId: string, taskId: string, reason?: string): Promise<Task> {
  return mutateJSON<Task>('POST', `${taskPath(projectId, taskId)}/requirements/revise`, { reason: reason || '' })
}

export function revisePlan(projectId: string, taskId: string, reason?: string): Promise<Task> {
  return mutateJSON<Task>('POST', `${taskPath(projectId, taskId)}/plan/revise`, { reason: reason || '' })
}

export interface HealthStatus {
  status: string
  build_id: string
  error?: string
}

export async function getHealthStatus(): Promise<HealthStatus> {
  const res = await fetch('/healthcheck')
  if (!res.ok) {
    const body = await res.json().catch(() => ({}))
    throw new Error((body as HealthStatus).error || `healthcheck failed with status ${res.status}`)
  }
  return res.json() as Promise<HealthStatus>
}

export function listModels(executor = 'local'): Promise<ModelsListResult> {
	return listExecutorCapabilities().then((result) => ({
		models: result.executors.find((entry) => entry.name === executor)?.models ?? [],
	}))
}

// listAgentExecutors reports which registered agentRunners entries
// currently respond to a live CheckHealth probe — used to only offer an
// executor choice the server will actually accept. Shared by both the
// StageConversationPanel and ChatPanel pickers (see AgentExecutorsListResult).
export function listAgentExecutors(): Promise<AgentExecutorsListResult> {
  return listExecutorCapabilities().then((result) => ({ executors: result.executors.map((entry) => entry.name) }))
}

export function listExecutorCapabilities(): Promise<import('./types').ExecutorCapabilitiesResult> {
  return getJSON<import('./types').ExecutorCapabilitiesResult>('/api/v1/agent-executors')
}

// streamSSE reads res's body as a "data: {...}\n\n"-per-line Server-Sent-
// Events stream, invoking onEvent once per line until the stream ends.
// Shared by every SSE-streaming endpoint this client calls — the chat/
// stage-conversation ones (all emitting ChatStreamEvent, internal/api/chat.go)
// and startExecution (emitting ExecuteStreamEvent, internal/api/execution.go)
// — generic over T since the wire shape differs per endpoint.
async function streamSSE<T>(res: Response, onEvent: (event: T) => void): Promise<void> {
  if (!res.ok) {
    throw new Error(await parseAPIError(res, `request failed with status ${res.status}`))
  }
  if (!res.body) {
    throw new Error('request failed: no response body')
  }

  const reader = res.body.getReader()
  const decoder = new TextDecoder()
  let buffer = ''

  for (;;) {
    const { done, value } = await reader.read()
    if (done) {
      break
    }
    buffer += decoder.decode(value, { stream: true })

    let newlineIndex: number
    while ((newlineIndex = buffer.indexOf('\n')) >= 0) {
      const line = buffer.slice(0, newlineIndex).trim()
      buffer = buffer.slice(newlineIndex + 1)
      if (!line.startsWith('data: ')) {
        continue
      }
      onEvent(JSON.parse(line.slice('data: '.length)) as T)
    }
  }
}

// streamChatCompletion posts one new message to the chat completions
// endpoint and invokes onEvent once per incremental server-sent event as it
// arrives, until the stream ends. The server holds this conversation's
// history keyed by sessionKey (a client-generated id stable for the
// lifetime of one ChatPanel mount) — unlike postStageMessage's stage
// conversations, the client never resends prior turns. executor selects
// which registered agentRunners entry produces the reply (e.g. "local",
// "claude-code" — see AgentExecutorsListResult); model is only honored by
// model-selectable executors. Rejects if the request itself fails to start
// (bad status, no body); once streaming begins, upstream failures surface
// as a final event with `error` set (see ChatStreamEvent), not a rejection.
export async function streamChatCompletion(
  content: string,
  model: string,
  executor: string,
  sessionKey: string,
  onEvent: (event: ChatStreamEvent) => void,
  history?: ChatHistoryEntry[],
  signal?: AbortSignal,
): Promise<void> {
  const body: ChatCompletionRequestBody = { content, model, executor, session_key: sessionKey, history }
  const res = await fetch('/api/v1/chat/completions', {
    method: 'POST',
    headers: { 'Content-Type': 'application/json' },
    body: JSON.stringify(body),
    signal,
  })
  await streamSSE<ChatStreamEvent>(res, onEvent)
}

// closeChatSession discards a free-chat session's server-held history —
// the "New chat" action's server-side counterpart.
export async function closeChatSession(sessionKey: string): Promise<void> {
  const res = await fetch('/api/v1/chat/sessions/close', {
    method: 'POST',
    headers: { 'Content-Type': 'application/json' },
    body: JSON.stringify({ session_key: sessionKey }),
  })
  if (!res.ok) {
    throw new Error(await parseAPIError(res, `closing chat session returned ${res.status}`))
  }
}

// postStageMessage posts a user message to a task's GrillMe (stage:
// "requirements") or Planning Mode (stage: "planning") Conversation and
// streams the assistant's reply the same way streamChatCompletion does —
// including a `tool_call` event if the model proposes a Draft. executor
// selects which backend produces the reply: "" (default) is the local-LLM
// chat path (model applies); any other value (e.g. "claude-code") routes
// to a tool-equipped agent executor instead, and model is ignored
// (internal/api/stage_conversation.go).
export async function postStageMessage(
  projectId: string,
  taskId: string,
  stage: string,
  content: string,
  model: string,
  executor: string,
  onEvent: (event: ChatStreamEvent) => void,
  signal?: AbortSignal,
  effort?: string,
): Promise<void> {
  const res = await fetch(`${taskPath(projectId, taskId)}/stages/${encodeURIComponent(stage)}/messages`, {
    method: 'POST',
    headers: { 'Content-Type': 'application/json' },
    body: JSON.stringify({ content, model, executor, effort }),
    signal,
  })
  await streamSSE<ChatStreamEvent>(res, onEvent)
}

// startStageConversation begins a brand-new, empty stage Conversation on
// the agent's own initiative — there is no human reply yet to post via
// postStageMessage, so this runs one turn seeded server-side
// (internal/api/stage_conversation.go's kickoffUserMessageFor, never sent or
// shown here) and streams the resulting opening turn the same way
// postStageMessage streams a reply. The server rejects this with a 409 once
// the conversation already has messages (see streamSSE — a non-ok response
// with no body throws before any events fire), so callers should only
// invoke this once, when getStageConversation comes back empty.
export async function startStageConversation(
  projectId: string,
  taskId: string,
  stage: string,
  model: string,
  executor: string,
  onEvent: (event: ChatStreamEvent) => void,
  signal?: AbortSignal,
  effort?: string,
): Promise<void> {
  const res = await fetch(`${taskPath(projectId, taskId)}/stages/${encodeURIComponent(stage)}/start`, {
    method: 'POST',
    headers: { 'Content-Type': 'application/json' },
    body: JSON.stringify({ model, executor, effort }),
    signal,
  })
  await streamSSE<ChatStreamEvent>(res, onEvent)
}

// deleteStageMessage removes exactly one message (by its position in the
// conversation) from a stage's persisted Conversation, and evicts the
// task+stage's live agent session server-side (internal/api/stage_conversation.go's
// handleDeleteStageMessage) so the deletion is reflected in what the model
// sees on its next turn, not just on screen.
export async function deleteStageMessage(projectId: string, taskId: string, stage: string, index: number): Promise<Conversation> {
  const res = await fetch(`${taskPath(projectId, taskId)}/stages/${encodeURIComponent(stage)}/messages/${index}`, {
    method: 'DELETE',
  })
  if (!res.ok) {
    throw new Error(await parseAPIError(res, `deleting message returned ${res.status}`))
  }
  return res.json() as Promise<Conversation>
}

// regenerateStageMessage resends the user turn at index — either unchanged
// (Regenerate: index is the user message preceding the assistant reply
// being regenerated, content is that message's own existing text) or edited
// (Edit: index is the user message's own position, content is the new
// text). Everything from index onward is discarded server-side and
// replaced by a fresh [user(content), assistant] pair once the stream
// completes (internal/api/stage_conversation.go's handleRegenerateStageMessage).
export async function regenerateStageMessage(
  projectId: string,
  taskId: string,
  stage: string,
  index: number,
  content: string,
  model: string,
  executor: string,
  onEvent: (event: ChatStreamEvent) => void,
  signal?: AbortSignal,
  effort?: string,
): Promise<void> {
  const res = await fetch(`${taskPath(projectId, taskId)}/stages/${encodeURIComponent(stage)}/messages/${index}/regenerate`, {
    method: 'POST',
    headers: { 'Content-Type': 'application/json' },
    body: JSON.stringify({ content, model, executor, effort }),
    signal,
  })
  await streamSSE<ChatStreamEvent>(res, onEvent)
}

function taskDraftPath(projectId: string, sessionId: string): string {
  return `/api/v1/projects/${encodeURIComponent(projectId)}/task-drafts/${encodeURIComponent(sessionId)}`
}

// getTaskDraftConversation returns a task-drafts session's persisted
// message history — the pre-creation "New Task" chat's counterpart to
// getStageConversation, keyed by a client-minted sessionId instead of a
// task+stage (there is no task yet).
export function getTaskDraftConversation(projectId: string, sessionId: string): Promise<Conversation> {
  return getJSON<Conversation>(`${taskDraftPath(projectId, sessionId)}/conversation`)
}

// postTaskDraftMessage posts a user message to a task-drafts session and
// streams the assistant's reply — the task-drafts counterpart to
// postStageMessage, including a `tool_call` event when the model proposes
// a propose_task Draft.
export async function postTaskDraftMessage(
  projectId: string,
  sessionId: string,
  content: string,
  model: string,
  executor: string,
  onEvent: (event: ChatStreamEvent) => void,
  signal?: AbortSignal,
): Promise<void> {
  const res = await fetch(`${taskDraftPath(projectId, sessionId)}/messages`, {
    method: 'POST',
    headers: { 'Content-Type': 'application/json' },
    body: JSON.stringify({ content, model, executor }),
    signal,
  })
  await streamSSE<ChatStreamEvent>(res, onEvent)
}

// startTaskDraftConversation begins a brand-new task-drafts session's
// Conversation on the agent's own initiative — the task-drafts counterpart
// to startStageConversation. Rejects with 409 once the session already has
// messages (see streamSSE), so callers should only invoke this once, when
// getTaskDraftConversation comes back empty.
export async function startTaskDraftConversation(
  projectId: string,
  sessionId: string,
  model: string,
  executor: string,
  onEvent: (event: ChatStreamEvent) => void,
  signal?: AbortSignal,
): Promise<void> {
  const res = await fetch(`${taskDraftPath(projectId, sessionId)}/start`, {
    method: 'POST',
    headers: { 'Content-Type': 'application/json' },
    body: JSON.stringify({ model, executor }),
    signal,
  })
  await streamSSE<ChatStreamEvent>(res, onEvent)
}

// deleteTaskDraftMessage removes exactly one message from a task-drafts
// session's persisted Conversation, and evicts its live agent session
// server-side — the task-drafts counterpart to deleteStageMessage.
export async function deleteTaskDraftMessage(projectId: string, sessionId: string, index: number): Promise<Conversation> {
  const res = await fetch(`${taskDraftPath(projectId, sessionId)}/messages/${index}`, { method: 'DELETE' })
  if (!res.ok) {
    throw new Error(await parseAPIError(res, `deleting message returned ${res.status}`))
  }
  return res.json() as Promise<Conversation>
}

// regenerateTaskDraftMessage resends the user turn at index — the
// task-drafts counterpart to regenerateStageMessage; see that function's
// doc comment for the shared Regenerate/Edit semantics.
export async function regenerateTaskDraftMessage(
  projectId: string,
  sessionId: string,
  index: number,
  content: string,
  model: string,
  executor: string,
  onEvent: (event: ChatStreamEvent) => void,
  signal?: AbortSignal,
): Promise<void> {
  const res = await fetch(`${taskDraftPath(projectId, sessionId)}/messages/${index}/regenerate`, {
    method: 'POST',
    headers: { 'Content-Type': 'application/json' },
    body: JSON.stringify({ content, model, executor }),
    signal,
  })
  await streamSSE<ChatStreamEvent>(res, onEvent)
}

// startExecution runs one autonomous Implementation-stage execution
// attempt to completion, streaming the agent's live text/tool_call/
// tool_result activity via onEvent (internal/api/execution.go). The final
// event has type "done" and carries the recorded Execution outcome — the
// caller should reload the task afterward, since a successful run
// auto-advances task.stage to "review" server-side. model is the selected
// model for model-selectable executors such as Codex. continueFromExecutionId
// is the human's choice to continue
// from a prior failed/partial execution's branch (getContinuableExecution's
// hint, echoed back) — omitted (undefined) for a normal fresh-from-main run.
export async function startExecution(
  projectId: string,
  taskId: string,
  executor: string,
  onEvent: (event: ExecuteStreamEvent) => void,
  signal?: AbortSignal,
  continueFromExecutionId?: string,
  model?: string,
  effort?: string,
): Promise<void> {
  const res = await fetch(`${taskPath(projectId, taskId)}/execute`, {
    method: 'POST',
    headers: { 'Content-Type': 'application/json' },
    body: JSON.stringify({ executor, model, effort, continue_from_execution_id: continueFromExecutionId || undefined }),
    signal,
  })
  await streamSSE<ExecuteStreamEvent>(res, onEvent)
}

// listExecutions returns every recorded execution attempt for a task,
// oldest first (internal/api/execution.go's handleListExecutions).
export function listExecutions(projectId: string, taskId: string): Promise<ExecutionsListResult> {
  return getJSON<ExecutionsListResult>(`${taskPath(projectId, taskId)}/executions`)
}

// getContinuableExecution returns the execution_id a human could choose to
// continue from right now (empty string if none), per
// handleGetContinuableExecution/resolveFailureContinuation.
export function getContinuableExecution(projectId: string, taskId: string): Promise<ContinuableExecutionResult> {
  return getJSON<ContinuableExecutionResult>(`${taskPath(projectId, taskId)}/executions/continuable`)
}

// getExecutionLog returns one past execution attempt's full recorded event
// stream (internal/api/execution.go's handleGetExecutionLog) — fetched on
// demand (e.g. when a human expands a past attempt in ExecutePanel), not
// bundled into listExecutions, since a long unattended run's log can be
// large.
export function getExecutionLog(projectId: string, taskId: string, executionId: string): Promise<ExecutionLog> {
  return getJSON<ExecutionLog>(`${taskPath(projectId, taskId)}/executions/${executionId}/log`)
}

import type {
  AgentExecutorsListResult,
  ChatCompletionRequestBody,
  ChatHistoryEntry,
  ChatStreamEvent,
  Conversation,
  CreateProjectRequest,
  CreateTaskRequest,
  ExecuteStreamEvent,
  ExecutionsListResult,
  FinalizeReviewResponse,
  ModelsListResult,
  Project,
  ProjectListResult,
  RequirementsDraft,
  ReviewDiffResult,
  ReviewDraft,
  ReviewsListResult,
  Task,
  TaskContext,
  TaskListResult,
  TaskPlan,
  UpdateProjectRequest,
  UpdateTaskRequest,
} from './types'

// isAbortError reports whether err is the DOMException fetch/streamSSE
// throws when an AbortController tied to the request's signal was aborted
// (e.g. the user clicked Stop mid-stream) — callers use this to distinguish
// a deliberate cancellation from a real network/server failure so they
// don't surface it as an error message.
export function isAbortError(err: unknown): boolean {
  return err instanceof DOMException && err.name === 'AbortError'
}

async function getJSON<T>(path: string): Promise<T> {
  const res = await fetch(path)
  if (!res.ok) {
    throw new Error(`${path} returned ${res.status}`)
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
    const message = await res.text().catch(() => '')
    throw new Error(message || `${method} ${path} returned ${res.status}`)
  }
  return res.json() as Promise<T>
}

export function listProjects(): Promise<ProjectListResult> {
  return getJSON<ProjectListResult>('/api/v1/projects')
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

// listReviews returns every recorded verdict for a task, oldest first — the
// terminal "merged" screen reads the latest to show its notes on a fresh
// visit (ReviewsListResult).
export function listReviews(projectId: string, taskId: string): Promise<ReviewsListResult> {
  return getJSON<ReviewsListResult>(`${taskPath(projectId, taskId)}/reviews`)
}

// getReviewDiff returns the raw patch of the task's most recent execution —
// the diff ReviewPanel shows in its collapsed "View diff" before the review
// conversation starts. 404s when there's no execution yet, which the panel
// treats as "no diff to show" rather than an error.
export function getReviewDiff(projectId: string, taskId: string): Promise<ReviewDiffResult> {
  return getJSON<ReviewDiffResult>(`${taskPath(projectId, taskId)}/review/diff`)
}

export function reviseRequirements(projectId: string, taskId: string): Promise<Task> {
  return mutateJSON<Task>('POST', `${taskPath(projectId, taskId)}/requirements/revise`, {})
}

export function revisePlan(projectId: string, taskId: string): Promise<Task> {
  return mutateJSON<Task>('POST', `${taskPath(projectId, taskId)}/plan/revise`, {})
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

export function listModels(): Promise<ModelsListResult> {
  return getJSON<ModelsListResult>('/api/v1/chat/models')
}

// listAgentExecutors reports which registered agentRunners entries
// currently respond to a live CheckHealth probe — used to only offer an
// executor choice the server will actually accept. Shared by both the
// StageConversationPanel and ChatPanel pickers (see AgentExecutorsListResult).
export function listAgentExecutors(): Promise<AgentExecutorsListResult> {
  return getJSON<AgentExecutorsListResult>('/api/v1/agent-executors')
}

// streamSSE reads res's body as a "data: {...}\n\n"-per-line Server-Sent-
// Events stream, invoking onEvent once per line until the stream ends.
// Shared by every SSE-streaming endpoint this client calls — the chat/
// stage-conversation ones (all emitting ChatStreamEvent, internal/api/chat.go)
// and startExecution (emitting ExecuteStreamEvent, internal/api/execution.go)
// — generic over T since the wire shape differs per endpoint.
async function streamSSE<T>(res: Response, onEvent: (event: T) => void): Promise<void> {
  if (!res.ok || !res.body) {
    throw new Error(`request failed with status ${res.status}`)
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
    throw new Error(`closing chat session returned ${res.status}`)
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
): Promise<void> {
  const res = await fetch(`${taskPath(projectId, taskId)}/stages/${encodeURIComponent(stage)}/messages`, {
    method: 'POST',
    headers: { 'Content-Type': 'application/json' },
    body: JSON.stringify({ content, model, executor }),
    signal,
  })
  await streamSSE<ChatStreamEvent>(res, onEvent)
}

// startStageConversation begins a brand-new, empty stage Conversation on
// the agent's own initiative — there is no human reply yet to post via
// postStageMessage, so this runs one turn seeded server-side
// (internal/api/stage_conversation.go's kickoffUserMessage, never sent or
// shown here) and streams the resulting opening question the same way
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
): Promise<void> {
  const res = await fetch(`${taskPath(projectId, taskId)}/stages/${encodeURIComponent(stage)}/start`, {
    method: 'POST',
    headers: { 'Content-Type': 'application/json' },
    body: JSON.stringify({ model, executor }),
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
    const message = await res.text().catch(() => '')
    throw new Error(message || `deleting message returned ${res.status}`)
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
): Promise<void> {
  const res = await fetch(`${taskPath(projectId, taskId)}/stages/${encodeURIComponent(stage)}/messages/${index}/regenerate`, {
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
// auto-advances task.stage to "review" server-side. executor is currently
// only meaningfully "claude-code" — no picker is offered client-side (see
// ExecutePanel).
export async function startExecution(
  projectId: string,
  taskId: string,
  executor: string,
  onEvent: (event: ExecuteStreamEvent) => void,
  signal?: AbortSignal,
): Promise<void> {
  const res = await fetch(`${taskPath(projectId, taskId)}/execute`, {
    method: 'POST',
    headers: { 'Content-Type': 'application/json' },
    body: JSON.stringify({ executor }),
    signal,
  })
  await streamSSE<ExecuteStreamEvent>(res, onEvent)
}

// listExecutions returns every recorded execution attempt for a task,
// oldest first (internal/api/execution.go's handleListExecutions).
export function listExecutions(projectId: string, taskId: string): Promise<ExecutionsListResult> {
  return getJSON<ExecutionsListResult>(`${taskPath(projectId, taskId)}/executions`)
}

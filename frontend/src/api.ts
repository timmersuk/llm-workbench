import type {
  AgentExecutorsListResult,
  ChatCompletionRequestBody,
  ChatStreamEvent,
  Conversation,
  CreateProjectRequest,
  CreateTaskRequest,
  ModelsListResult,
  Project,
  ProjectListResult,
  RequirementsDraft,
  Task,
  TaskContext,
  TaskListResult,
  TaskPlan,
  UpdateProjectRequest,
  UpdateTaskRequest,
} from './types'

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
// Shared by streamChatCompletion (the free-floating chat tab) and
// postStageMessage (GrillMe/Planning Mode) — both endpoints emit the same
// ChatStreamEvent wire shape (internal/api/chat.go).
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
): Promise<void> {
  const body: ChatCompletionRequestBody = { content, model, executor, session_key: sessionKey }
  const res = await fetch('/api/v1/chat/completions', {
    method: 'POST',
    headers: { 'Content-Type': 'application/json' },
    body: JSON.stringify(body),
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
): Promise<void> {
  const res = await fetch(`${taskPath(projectId, taskId)}/stages/${encodeURIComponent(stage)}/messages`, {
    method: 'POST',
    headers: { 'Content-Type': 'application/json' },
    body: JSON.stringify({ content, model, executor }),
  })
  await streamSSE<ChatStreamEvent>(res, onEvent)
}

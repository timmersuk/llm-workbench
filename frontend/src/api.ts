import type {
  ChatCompletionResponse,
  ChatMessage,
  CreateProjectRequest,
  CreateTaskRequest,
  ModelsListResult,
  Project,
  ProjectListResult,
  Task,
  TaskListResult,
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

export async function sendChatMessage(messages: ChatMessage[], model: string): Promise<ChatCompletionResponse> {
  const res = await fetch('/api/v1/chat/completions', {
    method: 'POST',
    headers: { 'Content-Type': 'application/json' },
    body: JSON.stringify({ model, messages }),
  })
  if (!res.ok) {
    throw new Error(`chat completion request failed with status ${res.status}`)
  }
  return res.json() as Promise<ChatCompletionResponse>
}

export interface TaskReferences {
  knowledge: string[]
  repo: string[]
}

export type TaskStatus = 'draft' | 'ready' | 'in_progress' | 'blocked' | 'failed' | 'complete'
export type TaskStage = 'requirements' | 'planning' | 'implementation' | 'review' | 'complete'

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
// and must be unique within that project.
export interface CreateTaskRequest {
  id: string
  title: string
  status: TaskStatus
  stage: TaskStage
  objective: string
  constraints: string[]
  assumptions: string[]
  success_criteria: string[]
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

export interface ChatMessage {
  role: string
  content: string
}

export interface ModelsListResult {
  models: string[]
}

export interface ChatCompletionResponse {
  id: string
  model: string
  choices: {
    index: number
    message: ChatMessage
    finish_reason: string
  }[]
}

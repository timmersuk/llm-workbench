export interface TaskReferences {
  knowledge: string[]
  repo: string[]
}

export interface Task {
  id: string
  title: string
  project: string
  status: string
  stage: string
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

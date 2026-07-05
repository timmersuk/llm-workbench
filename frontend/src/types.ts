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

export interface ChatMessage {
  role: string
  content: string
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

import type { ChatCompletionResponse, ChatMessage, Project, Task } from './types'

async function getJSON<T>(path: string): Promise<T> {
  const res = await fetch(path)
  if (!res.ok) {
    throw new Error(`${path} returned ${res.status}`)
  }
  return res.json() as Promise<T>
}

export function listTasks(): Promise<Task[]> {
  return getJSON<Task[]>('/api/v1/tasks')
}

export function listProjects(): Promise<Project[]> {
  return getJSON<Project[]>('/api/v1/projects')
}

export async function sendChatMessage(messages: ChatMessage[]): Promise<ChatCompletionResponse> {
  const res = await fetch('/api/v1/chat/completions', {
    method: 'POST',
    headers: { 'Content-Type': 'application/json' },
    body: JSON.stringify({ messages }),
  })
  if (!res.ok) {
    throw new Error(`chat completion request failed with status ${res.status}`)
  }
  return res.json() as Promise<ChatCompletionResponse>
}

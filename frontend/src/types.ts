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
// content, and tool_call are never more than one set on the same event;
// error is only set on the final event of a stream that failed partway
// through. tool_call is only ever populated on the stage-conversation
// endpoint (postStageMessage) — the free-floating chat endpoint never
// registers tools, so never sets it, but shares this event shape.
export interface ChatStreamEvent {
  content?: string
  reasoning_content?: string
  tool_call?: { name: string; arguments: string }
  error?: string
}

// TaskContext mirrors task.Context (internal/task/context.go) —
// context.yaml, GrillMe's Finalize output alongside the requirements
// fields on Task itself.
export interface TaskContext {
  summary: string
  background: string
  files: string[]
  detail: string
  verification: string[]
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

// stageConversationOps.ts adapts api.ts's plain stage-conversation and
// task-drafts functions into the StageConversationOps shape
// StageConversationPanel takes as a single prop (getConversation/
// postMessage/startConversation/deleteMessage/regenerateMessage) — the
// binding GrillMePanel/PlanningModePanel/ReviewPanel (stageConversationOps,
// closing over projectId/taskId/stage) and NewTaskPanel/TaskDraftView
// (taskDraftConversationOps, closing over projectId/sessionId instead) all
// use.
//
// Deliberately its own module rather than exported from api.ts itself: every
// existing stage-panel test does `vi.mock('./api')`, which auto-mocks every
// export of that module, including a factory function defined there — so a
// panel calling `api.stageConversationOps(...)` would get back an object of
// undefined methods instead of the real bindings, and every existing test's
// `vi.mocked(api.getStageConversation).mockResolvedValue(...)` would be
// wired to a function this panel never actually calls. Living in a separate,
// unmocked file sidesteps that: this module still imports the real (mocked,
// under test) api.ts functions and calls them for real, so
// vi.mocked(api.getStageConversation) etc. keeps working exactly as it did
// before StageConversationPanel took these as props instead of hardcoding
// them.
import {
  deleteStageMessage,
  deleteTaskDraftMessage,
  getStageConversation,
  getTaskDraftConversation,
  postStageMessage,
  postStagePermissionDecision,
  postTaskDraftMessage,
  regenerateStageMessage,
  regenerateTaskDraftMessage,
  startStageConversation,
  startTaskDraftConversation,
} from './api'
import type { StageConversationOps } from './StageConversationPanel'

// stageConversationOps binds getStageConversation/postStageMessage/
// startStageConversation/deleteStageMessage/regenerateStageMessage to one
// task's stage conversation — used by GrillMePanel/PlanningModePanel/
// ReviewPanel.
export function stageConversationOps(projectId: string, taskId: string, stage: string): StageConversationOps {
  return {
    getConversation: () => getStageConversation(projectId, taskId, stage),
    postMessage: (content, model, executor, effort, onEvent, signal) =>
      postStageMessage(projectId, taskId, stage, content, model, executor, onEvent, signal, effort),
    startConversation: (model, executor, effort, onEvent, signal) =>
      startStageConversation(projectId, taskId, stage, model, executor, onEvent, signal, effort),
    deleteMessage: (index) => deleteStageMessage(projectId, taskId, stage, index),
    regenerateMessage: (index, content, model, executor, effort, onEvent, signal) =>
      regenerateStageMessage(projectId, taskId, stage, index, content, model, executor, onEvent, signal, effort),
    submitPermissionDecision: (requestId, allow) =>
      postStagePermissionDecision(projectId, taskId, stage, requestId, allow),
  }
}

// taskDraftConversationOps binds getTaskDraftConversation/
// postTaskDraftMessage/startTaskDraftConversation/deleteTaskDraftMessage/
// regenerateTaskDraftMessage to one task-drafts session — used by
// NewTaskPanel (a live chat) and TaskDraftView (the read-only historical
// view of the same conversation once a task exists).
export function taskDraftConversationOps(projectId: string, sessionId: string): StageConversationOps {
  return {
    getConversation: () => getTaskDraftConversation(projectId, sessionId),
    postMessage: (content, model, executor, _effort, onEvent, signal) =>
      postTaskDraftMessage(projectId, sessionId, content, model, executor, onEvent, signal),
    startConversation: (model, executor, _effort, onEvent, signal) =>
      startTaskDraftConversation(projectId, sessionId, model, executor, onEvent, signal),
    deleteMessage: (index) => deleteTaskDraftMessage(projectId, sessionId, index),
    regenerateMessage: (index, content, model, executor, _effort, onEvent, signal) =>
      regenerateTaskDraftMessage(projectId, sessionId, index, content, model, executor, onEvent, signal),
  }
}

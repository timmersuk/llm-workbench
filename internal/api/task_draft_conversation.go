package api

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"strconv"
	"strings"
	"time"

	"github.com/sirupsen/logrus"

	"github.com/timmersuk/llm-workbench/internal/agentrunner"
	"github.com/timmersuk/llm-workbench/internal/chat"
	"github.com/timmersuk/llm-workbench/internal/drafttool"
	"github.com/timmersuk/llm-workbench/internal/project"
	"github.com/timmersuk/llm-workbench/internal/task"
)

// task_draft_conversation.go is the "New Task" chat's backend: a small,
// deliberate fork of stage_conversation.go's five-endpoint surface
// (getConversation/postMessage/startConversation/deleteMessage/
// regenerateMessage), not a generalization of it. It exists because a
// task-drafts session runs *before* any task exists — there is no taskId
// to key a Conversation by, no task.yaml fields to seed the prompt with, no
// Finalize (the human confirms straight into the existing Create Task API,
// api-side nothing new to write), and no Review-style bash/worktree
// concerns. Reusing runStageTurn/beginStageStream/stageAssistantMessage/
// conversationHistoryToChatMessages/resolvedDecisionsSummary/chatToolFor
// from stage_conversation.go keeps the actual turn-running mechanics
// (streaming, tool-call validation, history replay, settled-decisions
// summary) identical and in one place; only the per-request plumbing around
// "which project, which session, what to seed the system prompt with" is
// forked. See docs/adr's discussion of this trade-off in the milestone that
// added it: a shared abstraction across "keyed by task+stage" and "keyed by
// project+session, no task yet" was judged to cost more in indirection than
// it would save in duplicated lines.
//
// A task-drafts session's transcript is never Finalized or discarded by
// this backend — it's a permanent record (docs/task schema v0.md §1), kept
// even after the human confirms the draft and Create Task is called,
// because GrillMe's first Requirements-stage turn reads it back as a
// prompt addendum (buildTaskDraftContext below) and a human can revisit it
// read-only afterward (TaskDraftView.tsx) via the task's permanent
// DraftSessionID pointer.

// taskDraftSystemPrompt drives the pre-creation "New Task" interview —
// structurally the same one-question-at-a-time, ask_question-composing
// discipline as grillMeSystemPrompt/planningModeSystemPrompt
// (stage_conversation.go), ending in propose_task instead of
// propose_context/propose_plan. Deliberately scoped to task.yaml's own
// terse fields (id/title/objective/constraints/assumptions/success
// criteria/references) — the deeper narrative interview (summary,
// background, file references, verification steps) stays GrillMe's job,
// run immediately after creation with this conversation's transcript
// folded in as an addendum (buildTaskDraftContext) so it isn't
// re-litigated from scratch.
const taskDraftSystemPrompt = `You are interviewing the user to draft a brand-new task for this project — nothing has been created yet.

Rules for this interview:
- Before exploring the repo or asking anything, check the "Decisions already settled in this conversation" list below (if present) and this conversation's own history. If the point you're about to research or ask about is already there, it is final — do not re-research or re-ask it, just proceed as if it were still true. Fresh repo research turning up nothing (e.g. an empty Glob/Grep) is not by itself evidence of an open question.
- If you have tools available (Read/Grep/Glob), explore the project's repository first and answer your own questions from the code wherever you can. Only ask the human what the code cannot tell you.
- Ask exactly one question per turn. Never batch multiple questions into one message.
- When a question has a small set of sensible answers, call ask_question with those options, your recommended one, and a short reason why — put the question text itself in your normal reply, not in the tool call. Present the recommendation as a default the user can accept or redirect, not a decision already made. If the question is genuinely open-ended (no useful fixed set of answers), just ask it in your reply text without calling ask_question.
- Walk the design tree: resolve dependent decisions in order, one branch at a time, rather than jumping around.
- Keep this interview scoped to the task's own terse fields: what it's called (id/title), its objective, constraints, assumptions, and success criteria, and which knowledge/repositories it references. Deeper narrative detail (background, file-level rationale, verification steps) is GrillMe's job, right after this task is created — don't try to cover it here.
- Do not call propose_task until the id, title, objective, constraints, assumptions, and success criteria are coherent AND the user has confirmed shared understanding — do not propose on your own initiative just because you have enough to guess.
- If the user's reply contains a fenced JSON block representing a requested change to a draft you already proposed, treat that block as the authoritative starting point for your revision — refine it, don't discard it and start over.

`

// taskDraftKickoffMessage seeds a brand-new task-drafts session's very
// first turn — mirroring kickoffUserMessageFor's role for stage
// conversations (never shown to the human or persisted; only the
// resulting assistant turn is).
const taskDraftKickoffMessage = "Begin the interview now: use the project context above (and the repository, if you have tools) to ask your first question about what task the human wants to create."

// taskDraftTools is the fixed tool set every task-drafts turn offers —
// propose_task (the Draft this whole conversation exists to produce) plus
// ask_question (the same structured interview-question affordance
// Requirements/Planning get, stageTool). Built once, not per-request, since
// it never varies.
var taskDraftTools = []chat.Tool{chatToolFor(drafttool.ProposeTask), chatToolFor(drafttool.AskQuestion)}

// taskDraftSessionKey is the agentrunner.RunInput.SessionKey a task-drafts
// session's turns share across requests — the same role taskId+":"+stage
// plays for a stage conversation, just keyed by project+session since
// there's no task yet.
func taskDraftSessionKey(projectId, sessionId string) string {
	return "task-draft:" + projectId + ":" + sessionId
}

// buildTaskDraftPrompt seeds the interview's system prompt with the
// project's own fields and the resolved body of every knowledge concept it
// references — the task-drafts analog of buildStagePrompt, minus the
// "## Task" section that function includes (there is no task yet).
func (s *Server) buildTaskDraftPrompt(proj project.Project) string {
	var b strings.Builder
	b.WriteString(taskDraftSystemPrompt)

	fmt.Fprintf(&b, "## Project: %s\n%s\n", proj.Name, proj.Description)
	if len(proj.Constraints) > 0 {
		fmt.Fprintf(&b, "Project constraints:\n- %s\n", strings.Join(proj.Constraints, "\n- "))
	}
	if len(proj.Repositories) > 0 {
		fmt.Fprintf(&b, "Repositories:\n- %s\n", strings.Join(proj.Repositories, "\n- "))
	}

	for _, id := range proj.Knowledge {
		concept, err := s.KnowledgeStore.Get(id)
		if err != nil {
			logrus.WithError(err).WithField("concept", id).Warn("skipping knowledge concept that failed to resolve")
			continue
		}
		fmt.Fprintf(&b, "\n## Knowledge: %s\n%s\n", id, concept.Body)
	}

	return b.String()
}

// taskDraftRun bundles the resolved workspace and system prompt for one
// task-drafts turn — the minimal analog of stageRun (stage_conversation.go)
// with no bash/worktree concerns, since this conversation never runs
// against an execution.
type taskDraftRun struct {
	Workspace    string
	SystemPrompt string
}

// resolveTaskDraftRun assembles the taskDraftRun for a turn: read-only
// against the project's shared checkout (a project with no repo is
// tolerated, yielding an empty workspace and a text-only turn, the same
// treatment resolveStageRun gives Requirements/Planning).
func (s *Server) resolveTaskDraftRun(ctx context.Context, proj project.Project, history task.Conversation) (taskDraftRun, error) {
	systemPrompt := s.buildTaskDraftPrompt(proj)
	systemPrompt = s.appendWorkspaceAdvisories(ctx, systemPrompt, proj.Repositories)
	systemPrompt += resolvedDecisionsSummary(history)

	ws, err := agentrunner.ResolveWorkspace(ctx, s.ReposRoot, proj.Repositories)
	if err != nil && !errors.Is(err, agentrunner.ErrNoRepository) {
		return taskDraftRun{}, fmt.Errorf("resolving workspace: %w", err)
	}
	return taskDraftRun{Workspace: ws, SystemPrompt: systemPrompt}, nil
}

// taskDraftStreamTarget bundles what a streaming task-draft handler needs
// once the per-request boilerplate — executor lookup plus project
// resolution — has run, the task-drafts analog of stageStreamTarget.
type taskDraftStreamTarget struct {
	runner    agentrunner.AgentRunner
	proj      project.Project
	store     TaskStore
	projectId string
	sessionId string
}

// resolveTaskDraftStreamTarget selects the executor's runner (defaulting to
// defaultChatExecutor, the same convention every other chat/stage-
// conversation endpoint follows) and resolves the owning project. Writes
// the appropriate 400/404 and returns false on failure, before any SSE
// header is sent.
func (s *Server) resolveTaskDraftStreamTarget(w http.ResponseWriter, executorKey, projectId, sessionId string) (taskDraftStreamTarget, bool) {
	if executorKey == "" {
		executorKey = defaultChatExecutor
	}
	runner, ok := s.AgentRunners[executorKey]
	if !ok {
		writeAPIError(w, http.StatusBadRequest, fmt.Sprintf("unknown executor %q", executorKey))
		return taskDraftStreamTarget{}, false
	}

	proj, err := s.Projects.Get(projectId)
	if err != nil {
		writeGetError(w, err)
		return taskDraftStreamTarget{}, false
	}

	return taskDraftStreamTarget{runner: runner, proj: proj, store: s.Tasks, projectId: projectId, sessionId: sessionId}, true
}

// handleGetTaskDraftConversation returns a task-drafts session's persisted
// message history.
func (s *Server) handleGetTaskDraftConversation() http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		projectId := r.PathValue("projectId")
		if _, err := s.Projects.Get(projectId); err != nil {
			writeGetError(w, err)
			return
		}

		conv, err := s.Tasks.GetTaskDraftConversation(projectId, r.PathValue("sessionId"))
		if err != nil {
			writeGetError(w, err)
			return
		}
		writeJSON(w, http.StatusOK, conv)
	}
}

// handleStartTaskDraftConversation begins a task-drafts session's
// Conversation on the agent's own initiative — mirroring
// handleStartStageConversation: a brand-new "New Task" screen lands the
// human on an empty panel with nothing to reply to, so this runs one agent
// turn seeded with taskDraftKickoffMessage and persists only the resulting
// assistant turn. 409 if the session already has messages.
func (s *Server) handleStartTaskDraftConversation() http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		var req stageStartRequest
		if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
			writeAPIError(w, http.StatusBadRequest, "invalid request body")
			return
		}

		projectId := r.PathValue("projectId")
		sessionId := r.PathValue("sessionId")
		target, ok := s.resolveTaskDraftStreamTarget(w, req.Executor, projectId, sessionId)
		if !ok {
			return
		}

		existing, err := target.store.GetTaskDraftConversation(projectId, sessionId)
		if err != nil {
			writeGetError(w, err)
			return
		}
		if len(existing.Messages) > 0 {
			writeAPIError(w, http.StatusConflict, "conversation already started")
			return
		}

		writeEvent, ok := beginStageStream(w)
		if !ok {
			return
		}

		var assistantContent string
		var proposed *chat.ToolCall
		var activity []task.ConversationToolActivity
		var segments []task.ConversationSegment
		var streamErr error

		run, runErr := s.resolveTaskDraftRun(r.Context(), target.proj, existing)
		if runErr != nil {
			streamErr = runErr
		} else {
			assistantContent, proposed, activity, segments, streamErr = runStageTurn(r.Context(), target.runner, agentrunner.RunInput{
				SessionKey:   taskDraftSessionKey(projectId, sessionId),
				Workspace:    run.Workspace,
				SystemPrompt: run.SystemPrompt,
				UserMessage:  taskDraftKickoffMessage,
				Model:        req.Model,
				Tools:        taskDraftTools,
				MaxTurns:     requirementsPlanningMaxTurns,
			}, writeEvent)
		}
		if streamErr != nil {
			writeEvent(chatStreamEvent{Error: streamErr.Error()})
		}

		assistantMsg := stageAssistantMessage(assistantContent, proposed, activity, segments, streamErr)

		if _, err := target.store.AppendTaskDraftConversationMessages(projectId, sessionId, assistantMsg); err != nil {
			logrus.WithError(err).WithFields(logrus.Fields{"project": projectId, "session": sessionId}).Error("persisting task draft conversation kickoff message")
			writeEvent(chatStreamEvent{Error: fmt.Sprintf("saving conversation: %v", err)})
		}
	}
}

// handlePostTaskDraftMessage posts a user message to a task-drafts
// session's Conversation and streams the assistant's reply — the
// task-drafts analog of handlePostStageMessage.
func (s *Server) handlePostTaskDraftMessage() http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		var req stageMessageRequest
		if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
			writeAPIError(w, http.StatusBadRequest, "invalid request body")
			return
		}

		projectId := r.PathValue("projectId")
		sessionId := r.PathValue("sessionId")
		target, ok := s.resolveTaskDraftStreamTarget(w, req.Executor, projectId, sessionId)
		if !ok {
			return
		}

		writeEvent, ok := beginStageStream(w)
		if !ok {
			return
		}

		var assistantContent string
		var proposed *chat.ToolCall
		var activity []task.ConversationToolActivity
		var segments []task.ConversationSegment
		var streamErr error

		history, convErr := target.store.GetTaskDraftConversation(projectId, sessionId)
		var run taskDraftRun
		var runErr error
		if convErr != nil {
			streamErr = fmt.Errorf("loading conversation history: %w", convErr)
		} else if run, runErr = s.resolveTaskDraftRun(r.Context(), target.proj, history); runErr != nil {
			streamErr = runErr
		} else {
			assistantContent, proposed, activity, segments, streamErr = runStageTurn(r.Context(), target.runner, agentrunner.RunInput{
				SessionKey:   taskDraftSessionKey(projectId, sessionId),
				Workspace:    run.Workspace,
				SystemPrompt: run.SystemPrompt,
				UserMessage:  req.Content,
				Model:        req.Model,
				Tools:        taskDraftTools,
				MaxTurns:     requirementsPlanningMaxTurns,
				History:      conversationHistoryToChatMessages(history),
			}, writeEvent)
		}
		if streamErr != nil {
			writeEvent(chatStreamEvent{Error: streamErr.Error()})
		}

		assistantMsg := stageAssistantMessage(assistantContent, proposed, activity, segments, streamErr)

		if _, err := target.store.AppendTaskDraftConversationMessages(projectId, sessionId,
			task.ConversationMessage{Role: "user", Content: req.Content},
			assistantMsg,
		); err != nil {
			logrus.WithError(err).WithFields(logrus.Fields{"project": projectId, "session": sessionId}).Error("persisting task draft conversation messages")
			writeEvent(chatStreamEvent{Error: fmt.Sprintf("saving conversation: %v", err)})
		}
	}
}

// handleDeleteTaskDraftMessage removes exactly one message from a
// task-drafts session's Conversation and evicts its live agent session —
// the task-drafts analog of handleDeleteStageMessage.
func (s *Server) handleDeleteTaskDraftMessage() http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		index, err := strconv.Atoi(r.PathValue("index"))
		if err != nil {
			writeAPIError(w, http.StatusBadRequest, "invalid message index")
			return
		}

		projectId := r.PathValue("projectId")
		if _, err := s.Projects.Get(projectId); err != nil {
			writeGetError(w, err)
			return
		}
		sessionId := r.PathValue("sessionId")

		existing, err := s.Tasks.GetTaskDraftConversation(projectId, sessionId)
		if err != nil {
			writeGetError(w, err)
			return
		}
		if index < 0 || index >= len(existing.Messages) {
			writeAPIError(w, http.StatusBadRequest, fmt.Sprintf("message index %d out of range", index))
			return
		}

		updated := append(append([]task.ConversationMessage{}, existing.Messages[:index]...), existing.Messages[index+1:]...)
		conv, err := s.Tasks.ReplaceTaskDraftConversationMessages(projectId, sessionId, updated)
		if err != nil {
			writeGetError(w, err)
			return
		}

		s.closeSessions(taskDraftSessionKey(projectId, sessionId))
		writeJSON(w, http.StatusOK, conv)
	}
}

// handleRegenerateTaskDraftMessage resends the user turn at index — the
// task-drafts analog of handleRegenerateStageMessage; see that handler's
// doc comment for the shared Regenerate/Edit semantics.
func (s *Server) handleRegenerateTaskDraftMessage() http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		index, err := strconv.Atoi(r.PathValue("index"))
		if err != nil {
			writeAPIError(w, http.StatusBadRequest, "invalid message index")
			return
		}

		var req stageRegenerateRequest
		if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
			writeAPIError(w, http.StatusBadRequest, "invalid request body")
			return
		}

		projectId := r.PathValue("projectId")
		sessionId := r.PathValue("sessionId")
		target, ok := s.resolveTaskDraftStreamTarget(w, req.Executor, projectId, sessionId)
		if !ok {
			return
		}

		existing, err := target.store.GetTaskDraftConversation(projectId, sessionId)
		if err != nil {
			writeGetError(w, err)
			return
		}
		if index < 0 || index >= len(existing.Messages) {
			writeAPIError(w, http.StatusBadRequest, fmt.Sprintf("message index %d out of range", index))
			return
		}
		if existing.Messages[index].Role != "user" {
			writeAPIError(w, http.StatusBadRequest, "can only regenerate/edit from a user message")
			return
		}

		historyPrefix := append([]task.ConversationMessage{}, existing.Messages[:index]...)

		writeEvent, ok := beginStageStream(w)
		if !ok {
			return
		}

		sessionKey := taskDraftSessionKey(projectId, sessionId)
		s.closeSessions(sessionKey)

		var assistantContent string
		var proposed *chat.ToolCall
		var activity []task.ConversationToolActivity
		var segments []task.ConversationSegment
		var streamErr error

		run, runErr := s.resolveTaskDraftRun(r.Context(), target.proj, task.Conversation{Messages: historyPrefix})
		if runErr != nil {
			streamErr = runErr
		} else {
			assistantContent, proposed, activity, segments, streamErr = runStageTurn(r.Context(), target.runner, agentrunner.RunInput{
				SessionKey:   sessionKey,
				Workspace:    run.Workspace,
				SystemPrompt: run.SystemPrompt,
				UserMessage:  req.Content,
				Model:        req.Model,
				Tools:        taskDraftTools,
				MaxTurns:     requirementsPlanningMaxTurns,
				History:      conversationHistoryToChatMessages(task.Conversation{Messages: historyPrefix}),
			}, writeEvent)
		}
		if streamErr != nil {
			writeEvent(chatStreamEvent{Error: streamErr.Error()})
		}

		assistantMsg := stageAssistantMessage(assistantContent, proposed, activity, segments, streamErr)

		now := time.Now().UTC()
		userMsg := task.ConversationMessage{Role: "user", Content: req.Content, CreatedAt: now}
		assistantMsg.CreatedAt = now

		newMessages := append(historyPrefix, userMsg, assistantMsg)
		if _, err := target.store.ReplaceTaskDraftConversationMessages(projectId, sessionId, newMessages); err != nil {
			logrus.WithError(err).WithFields(logrus.Fields{"project": projectId, "session": sessionId}).Error("persisting regenerated task draft conversation messages")
			writeEvent(chatStreamEvent{Error: fmt.Sprintf("saving conversation: %v", err)})
		}
	}
}

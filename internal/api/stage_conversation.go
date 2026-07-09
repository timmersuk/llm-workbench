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

// Tool names for the Draft mechanism (CONTEXT.md): GrillMe registers
// propose_context, Planning Mode registers propose_plan. The actual
// name/description/schema live in internal/drafttool, shared with
// cmd/draftmcp's static MCP server (CodexRunner's Draft-tool mechanism) so
// both call sites see the same tool shape by construction.
const (
	proposeContextToolName = drafttool.ProposeContextName
	proposePlanToolName    = drafttool.ProposePlanName
)

// grillMeSystemPrompt and planningModeSystemPrompt encode the "grilling"
// interview discipline (CONTEXT.md's GrillMe/Planning Mode entries) both
// stages share: one question at a time, a recommended answer with every
// question, questions resolved in dependency order, and no proposal until
// the human has confirmed shared understanding. They differ only in what
// they're interviewing toward and which tool they end with.
const (
	grillMeSystemPrompt = `You are GrillMe, interviewing the user to sharpen a task's requirements.

Rules for this interview:
- If you have tools available (Read/Grep/Glob), explore the project's repository first and answer your own questions from the code wherever you can. Only ask the human what the code cannot tell you.
- Ask exactly one question per turn. Never batch multiple questions into one message.
- Every question comes with your recommended answer and a short reason why. Present it as a default the user can accept or redirect, not a decision already made.
- Walk the design tree: resolve dependent decisions in order, one branch at a time, rather than jumping around.
- Do not call propose_context until the objective, constraints, assumptions, and success criteria are coherent AND the user has confirmed shared understanding — do not propose on your own initiative just because you have enough to guess.
- If the user's reply contains a fenced JSON block representing a requested change to a draft you already proposed, treat that block as the authoritative starting point for your revision — refine it, don't discard it and start over.

`
	planningModeSystemPrompt = `You are Planning Mode, interviewing the user to produce a structured execution plan.

Rules for this interview:
- If you have tools available (Read/Grep/Glob), explore the project's repository first and answer your own questions from the code wherever you can. Only ask the human what the code cannot tell you.
- Ask exactly one question per turn. Never batch multiple questions into one message.
- Every question comes with your recommended answer and a short reason why. Present it as a default the user can accept or redirect, not a decision already made.
- Walk the design tree: resolve dependent decisions in order (approach, then steps, then risks and complexity), one branch at a time, rather than jumping around.
- Do not call propose_plan until the approach, steps, risks, and estimated complexity are coherent AND the user has confirmed shared understanding — do not propose on your own initiative just because you have enough to guess.
- If the user's reply contains a fenced JSON block representing a requested change to a plan you already proposed, treat that block as the authoritative starting point for your revision — refine it, don't discard it and start over.

`
)

// kickoffUserMessage drives a stage conversation's very first turn
// (handleStartStageConversation) — chat completion APIs need a user-role
// message to produce a reply at all, but there is no real human reply yet
// on a brand-new conversation. This is never shown to the human or
// persisted; only the assistant's resulting first question is.
const kickoffUserMessage = "Begin the interview now: use the task/project/knowledge context above (and the repository, if you have tools) to ask your first question."

// stageTool returns the Draft-proposing tool registered for stage, and
// whether stage is a valid Conversation stage at all (requirements or
// planning — see task.ErrInvalidStage). Name/description/schema come from
// internal/drafttool, shared with cmd/draftmcp.
func stageTool(stage string) (chat.Tool, bool) {
	switch stage {
	case task.StageRequirements:
		return chat.Tool{Type: "function", Function: chat.ToolSchema{
			Name:        drafttool.ProposeContext.Name,
			Description: drafttool.ProposeContext.Description,
			Parameters:  drafttool.ProposeContext.Schema,
		}}, true
	case task.StagePlanning:
		return chat.Tool{Type: "function", Function: chat.ToolSchema{
			Name:        drafttool.ProposePlan.Name,
			Description: drafttool.ProposePlan.Description,
			Parameters:  drafttool.ProposePlan.Schema,
		}}, true
	default:
		return chat.Tool{}, false
	}
}

// stageMessageRequest is the request body for handlePostStageMessage.
// Executor selects which agentRunners entry produces the reply, defaulting
// to defaultChatExecutor (chat.go) when empty — same convention as the
// free-floating chat endpoint's chatCompletionRequest.Executor.
type stageMessageRequest struct {
	Content  string `json:"content"`
	Model    string `json:"model"`
	Executor string `json:"executor,omitempty"`
}

// stageStartRequest is the request body for handleStartStageConversation —
// the same executor/model selection as stageMessageRequest, minus Content:
// there is no human reply yet on a conversation that hasn't started.
type stageStartRequest struct {
	Model    string `json:"model"`
	Executor string `json:"executor,omitempty"`
}

// handleGetStageConversation returns a stage's persisted message history.
func handleGetStageConversation(projects ProjectStore, factory TaskStoreFactory) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		stage := r.PathValue("stage")
		if _, ok := stageTool(stage); !ok {
			http.Error(w, fmt.Sprintf("invalid stage %q", stage), http.StatusBadRequest)
			return
		}

		store, ok := resolveTaskStore(w, projects, factory, r.PathValue("projectId"))
		if !ok {
			return
		}

		conv, err := store.GetConversation(r.PathValue("taskId"), stage)
		if err != nil {
			writeGetError(w, err)
			return
		}
		writeJSON(w, http.StatusOK, conv)
	}
}

// handlePostStageMessage posts a user message to a stage's Conversation,
// streams the assistant's reply as SSE (reusing chatStreamEvent, same wire
// shape as the free-floating chat endpoint in chat.go), and persists both
// messages once the stream ends. If the model calls the stage's registered
// tool, that's surfaced mid-stream as a chatStreamEvent.ToolCall — the
// Draft itself (CONTEXT.md) — for the frontend to render for review; it is
// not persisted or written to disk here, only Finalize (finalize.go) does
// that.
func handlePostStageMessage(projects ProjectStore, factory TaskStoreFactory, knowledgeReader KnowledgeReader, agentRunners map[string]agentrunner.AgentRunner, reposRoot string) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		stage := r.PathValue("stage")
		tool, ok := stageTool(stage)
		if !ok {
			http.Error(w, fmt.Sprintf("invalid stage %q", stage), http.StatusBadRequest)
			return
		}

		var req stageMessageRequest
		if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
			http.Error(w, "invalid request body", http.StatusBadRequest)
			return
		}

		executorKey := req.Executor
		if executorKey == "" {
			executorKey = defaultChatExecutor
		}
		runner, ok := agentRunners[executorKey]
		if !ok {
			http.Error(w, fmt.Sprintf("unknown executor %q", executorKey), http.StatusBadRequest)
			return
		}

		projectId := r.PathValue("projectId")
		taskId := r.PathValue("taskId")

		proj, err := projects.Get(projectId)
		if err != nil {
			writeGetError(w, err)
			return
		}
		root, err := projects.TasksRoot(projectId)
		if err != nil {
			writeGetError(w, err)
			return
		}
		store := factory(root)

		t, err := store.Get(taskId)
		if err != nil {
			writeGetError(w, err)
			return
		}

		systemPrompt := buildStagePrompt(t, proj, stage, knowledgeReader)

		flusher, ok := w.(http.Flusher)
		if !ok {
			http.Error(w, "streaming unsupported", http.StatusInternalServerError)
			return
		}

		w.Header().Set("Content-Type", "text/event-stream")
		w.Header().Set("Cache-Control", "no-cache")
		w.Header().Set("Connection", "keep-alive")
		w.WriteHeader(http.StatusOK)

		writeEvent := func(ev chatStreamEvent) {
			data, _ := json.Marshal(ev)
			fmt.Fprintf(w, "data: %s\n\n", data)
			flusher.Flush()
		}

		var assistantContent string
		var proposed *chat.ToolCall
		var streamErr error

		// A project with no configured repository is a normal state (a
		// pure-planning project with nothing to check out) — only
		// ErrNoRepository is tolerated as "no workspace to offer", proceeding
		// with an empty Workspace; any other ResolveWorkspace failure (an
		// invalid or unresolvable repository identifier) still aborts the
		// turn, since that signals real misconfiguration rather than an
		// absent-by-design repository.
		workspace, wsErr := agentrunner.ResolveWorkspace(reposRoot, proj.Repositories)
		if wsErr != nil && !errors.Is(wsErr, agentrunner.ErrNoRepository) {
			streamErr = fmt.Errorf("resolving workspace: %w", wsErr)
		} else if history, convErr := store.GetConversation(taskId, stage); convErr != nil {
			streamErr = fmt.Errorf("loading conversation history: %w", convErr)
		} else {
			assistantContent, proposed, streamErr = runStageTurn(r.Context(), runner, agentrunner.RunInput{
				SessionKey:   taskId + ":" + stage,
				Workspace:    workspace,
				SystemPrompt: systemPrompt,
				UserMessage:  req.Content,
				Model:        req.Model,
				Tool:         tool,
				History:      conversationHistoryToChatMessages(history),
			}, writeEvent)
		}
		if streamErr != nil {
			// Headers (200 OK) are already sent, so a failed stream can't
			// surface as an HTTP error status — relayed as a final SSE
			// event instead, matching handleChatCompletions (chat.go).
			writeEvent(chatStreamEvent{Error: streamErr.Error()})
		}

		assistantMsg := task.ConversationMessage{Role: "assistant", Content: assistantContent}
		if streamErr != nil {
			assistantMsg.Error = streamErr.Error()
		}
		if proposed != nil {
			assistantMsg.ToolCall = &task.ConversationToolCall{
				ID:        proposed.ID,
				Name:      proposed.Function.Name,
				Arguments: proposed.Function.Arguments,
			}
		}

		if _, err := store.AppendConversationMessages(taskId, stage,
			task.ConversationMessage{Role: "user", Content: req.Content},
			assistantMsg,
		); err != nil {
			logrus.WithError(err).WithFields(logrus.Fields{"task": taskId, "stage": stage}).Error("persisting stage conversation messages")
			writeEvent(chatStreamEvent{Error: fmt.Sprintf("saving conversation: %v", err)})
		}
	}
}

// handleStartStageConversation begins a stage's Conversation on the
// agent's own initiative: a brand-new task lands the human on an empty
// GrillMe/Planning Mode panel with nothing to reply to, so this runs one
// agent turn seeded with kickoffUserMessage (never shown or persisted)
// instead of waiting for a human message that doesn't exist yet, and
// persists only the resulting assistant turn — there is no human message to
// pair it with. Rejects with 409 if the conversation already has messages,
// since starting is only meaningful once, before any real exchange exists;
// continuing an already-started conversation is handlePostStageMessage's
// job.
func handleStartStageConversation(projects ProjectStore, factory TaskStoreFactory, knowledgeReader KnowledgeReader, agentRunners map[string]agentrunner.AgentRunner, reposRoot string) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		stage := r.PathValue("stage")
		tool, ok := stageTool(stage)
		if !ok {
			http.Error(w, fmt.Sprintf("invalid stage %q", stage), http.StatusBadRequest)
			return
		}

		var req stageStartRequest
		if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
			http.Error(w, "invalid request body", http.StatusBadRequest)
			return
		}

		executorKey := req.Executor
		if executorKey == "" {
			executorKey = defaultChatExecutor
		}
		runner, ok := agentRunners[executorKey]
		if !ok {
			http.Error(w, fmt.Sprintf("unknown executor %q", executorKey), http.StatusBadRequest)
			return
		}

		projectId := r.PathValue("projectId")
		taskId := r.PathValue("taskId")

		proj, err := projects.Get(projectId)
		if err != nil {
			writeGetError(w, err)
			return
		}
		root, err := projects.TasksRoot(projectId)
		if err != nil {
			writeGetError(w, err)
			return
		}
		store := factory(root)

		t, err := store.Get(taskId)
		if err != nil {
			writeGetError(w, err)
			return
		}

		existing, err := store.GetConversation(taskId, stage)
		if err != nil {
			writeGetError(w, err)
			return
		}
		if len(existing.Messages) > 0 {
			http.Error(w, "conversation already started", http.StatusConflict)
			return
		}

		systemPrompt := buildStagePrompt(t, proj, stage, knowledgeReader)

		flusher, ok := w.(http.Flusher)
		if !ok {
			http.Error(w, "streaming unsupported", http.StatusInternalServerError)
			return
		}

		w.Header().Set("Content-Type", "text/event-stream")
		w.Header().Set("Cache-Control", "no-cache")
		w.Header().Set("Connection", "keep-alive")
		w.WriteHeader(http.StatusOK)

		writeEvent := func(ev chatStreamEvent) {
			data, _ := json.Marshal(ev)
			fmt.Fprintf(w, "data: %s\n\n", data)
			flusher.Flush()
		}

		var assistantContent string
		var proposed *chat.ToolCall
		var streamErr error

		workspace, wsErr := agentrunner.ResolveWorkspace(reposRoot, proj.Repositories)
		if wsErr != nil && !errors.Is(wsErr, agentrunner.ErrNoRepository) {
			streamErr = fmt.Errorf("resolving workspace: %w", wsErr)
		} else {
			assistantContent, proposed, streamErr = runStageTurn(r.Context(), runner, agentrunner.RunInput{
				SessionKey:   taskId + ":" + stage,
				Workspace:    workspace,
				SystemPrompt: systemPrompt,
				UserMessage:  kickoffUserMessage,
				Model:        req.Model,
				Tool:         tool,
			}, writeEvent)
		}
		if streamErr != nil {
			writeEvent(chatStreamEvent{Error: streamErr.Error()})
		}

		assistantMsg := task.ConversationMessage{Role: "assistant", Content: assistantContent}
		if streamErr != nil {
			assistantMsg.Error = streamErr.Error()
		}
		if proposed != nil {
			assistantMsg.ToolCall = &task.ConversationToolCall{
				ID:        proposed.ID,
				Name:      proposed.Function.Name,
				Arguments: proposed.Function.Arguments,
			}
		}

		if _, err := store.AppendConversationMessages(taskId, stage, assistantMsg); err != nil {
			logrus.WithError(err).WithFields(logrus.Fields{"task": taskId, "stage": stage}).Error("persisting stage conversation kickoff message")
			writeEvent(chatStreamEvent{Error: fmt.Sprintf("saving conversation: %v", err)})
		}
	}
}

// handleDeleteStageMessage removes exactly one message from a stage's
// Conversation by index and evicts the live agent session across every
// registered executor (closeSessions, finalize.go) — a message deleted from
// the persisted record but left live in a runner's in-memory/subprocess
// session would mean the deletion never actually reaches what the model
// sees on its next turn. No new turn runs; the next real message already
// reloads persisted history from disk (handlePostStageMessage).
func handleDeleteStageMessage(projects ProjectStore, factory TaskStoreFactory, agentRunners map[string]agentrunner.AgentRunner) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		stage := r.PathValue("stage")
		if _, ok := stageTool(stage); !ok {
			http.Error(w, fmt.Sprintf("invalid stage %q", stage), http.StatusBadRequest)
			return
		}

		index, err := strconv.Atoi(r.PathValue("index"))
		if err != nil {
			http.Error(w, "invalid message index", http.StatusBadRequest)
			return
		}

		store, ok := resolveTaskStore(w, projects, factory, r.PathValue("projectId"))
		if !ok {
			return
		}

		taskId := r.PathValue("taskId")
		existing, err := store.GetConversation(taskId, stage)
		if err != nil {
			writeGetError(w, err)
			return
		}
		if index < 0 || index >= len(existing.Messages) {
			http.Error(w, fmt.Sprintf("message index %d out of range", index), http.StatusBadRequest)
			return
		}

		updated := append(append([]task.ConversationMessage{}, existing.Messages[:index]...), existing.Messages[index+1:]...)
		conv, err := store.ReplaceConversationMessages(taskId, stage, updated)
		if err != nil {
			writeGetError(w, err)
			return
		}

		closeSessions(agentRunners, taskId+":"+stage)
		writeJSON(w, http.StatusOK, conv)
	}
}

// stageRegenerateRequest is the request body for handleRegenerateStageMessage.
type stageRegenerateRequest struct {
	Content  string `json:"content"`
	Model    string `json:"model"`
	Executor string `json:"executor,omitempty"`
}

// handleRegenerateStageMessage resends the user turn at index — either
// unchanged (Regenerate: the frontend targets the user message preceding
// the assistant reply being regenerated, with that reply's own original
// content) or edited (Edit: the frontend targets the user message itself,
// with new content) — both reduce to the same operation: everything from
// index onward is discarded and replaced by a fresh [user(content),
// assistant] pair. closeSessions runs before the turn so the truncated
// History built from what's kept is what the runner actually consults, per
// RunInput.History's "only consulted when the runner has no live session"
// contract (agentrunner/runner.go).
func handleRegenerateStageMessage(projects ProjectStore, factory TaskStoreFactory, knowledgeReader KnowledgeReader, agentRunners map[string]agentrunner.AgentRunner, reposRoot string) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		stage := r.PathValue("stage")
		tool, ok := stageTool(stage)
		if !ok {
			http.Error(w, fmt.Sprintf("invalid stage %q", stage), http.StatusBadRequest)
			return
		}

		index, err := strconv.Atoi(r.PathValue("index"))
		if err != nil {
			http.Error(w, "invalid message index", http.StatusBadRequest)
			return
		}

		var req stageRegenerateRequest
		if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
			http.Error(w, "invalid request body", http.StatusBadRequest)
			return
		}

		executorKey := req.Executor
		if executorKey == "" {
			executorKey = defaultChatExecutor
		}
		runner, ok := agentRunners[executorKey]
		if !ok {
			http.Error(w, fmt.Sprintf("unknown executor %q", executorKey), http.StatusBadRequest)
			return
		}

		projectId := r.PathValue("projectId")
		taskId := r.PathValue("taskId")

		proj, err := projects.Get(projectId)
		if err != nil {
			writeGetError(w, err)
			return
		}
		root, err := projects.TasksRoot(projectId)
		if err != nil {
			writeGetError(w, err)
			return
		}
		store := factory(root)

		t, err := store.Get(taskId)
		if err != nil {
			writeGetError(w, err)
			return
		}

		existing, err := store.GetConversation(taskId, stage)
		if err != nil {
			writeGetError(w, err)
			return
		}
		if index < 0 || index >= len(existing.Messages) {
			http.Error(w, fmt.Sprintf("message index %d out of range", index), http.StatusBadRequest)
			return
		}
		if existing.Messages[index].Role != "user" {
			http.Error(w, "can only regenerate/edit from a user message", http.StatusBadRequest)
			return
		}

		historyPrefix := append([]task.ConversationMessage{}, existing.Messages[:index]...)
		systemPrompt := buildStagePrompt(t, proj, stage, knowledgeReader)

		flusher, ok := w.(http.Flusher)
		if !ok {
			http.Error(w, "streaming unsupported", http.StatusInternalServerError)
			return
		}

		w.Header().Set("Content-Type", "text/event-stream")
		w.Header().Set("Cache-Control", "no-cache")
		w.Header().Set("Connection", "keep-alive")
		w.WriteHeader(http.StatusOK)

		writeEvent := func(ev chatStreamEvent) {
			data, _ := json.Marshal(ev)
			fmt.Fprintf(w, "data: %s\n\n", data)
			flusher.Flush()
		}

		sessionKey := taskId + ":" + stage
		closeSessions(agentRunners, sessionKey)

		var assistantContent string
		var proposed *chat.ToolCall
		var streamErr error

		workspace, wsErr := agentrunner.ResolveWorkspace(reposRoot, proj.Repositories)
		if wsErr != nil && !errors.Is(wsErr, agentrunner.ErrNoRepository) {
			streamErr = fmt.Errorf("resolving workspace: %w", wsErr)
		} else {
			assistantContent, proposed, streamErr = runStageTurn(r.Context(), runner, agentrunner.RunInput{
				SessionKey:   sessionKey,
				Workspace:    workspace,
				SystemPrompt: systemPrompt,
				UserMessage:  req.Content,
				Model:        req.Model,
				Tool:         tool,
				History:      conversationHistoryToChatMessages(task.Conversation{Messages: historyPrefix}),
			}, writeEvent)
		}
		if streamErr != nil {
			writeEvent(chatStreamEvent{Error: streamErr.Error()})
		}

		assistantMsg := task.ConversationMessage{Role: "assistant", Content: assistantContent}
		if streamErr != nil {
			assistantMsg.Error = streamErr.Error()
		}
		if proposed != nil {
			assistantMsg.ToolCall = &task.ConversationToolCall{
				ID:        proposed.ID,
				Name:      proposed.Function.Name,
				Arguments: proposed.Function.Arguments,
			}
		}

		now := time.Now().UTC()
		userMsg := task.ConversationMessage{Role: "user", Content: req.Content, CreatedAt: now}
		assistantMsg.CreatedAt = now

		newMessages := append(historyPrefix, userMsg, assistantMsg)
		if _, err := store.ReplaceConversationMessages(taskId, stage, newMessages); err != nil {
			logrus.WithError(err).WithFields(logrus.Fields{"task": taskId, "stage": stage}).Error("persisting regenerated stage conversation messages")
			writeEvent(chatStreamEvent{Error: fmt.Sprintf("saving conversation: %v", err)})
		}
	}
}

// runStageTurn runs one agent turn and streams its deltas via writeEvent
// (the chatStreamEvent shape both stage-conversation endpoints share),
// returning the assistant's accumulated content and any proposed Draft tool
// call. Shared by handlePostStageMessage and handleStartStageConversation —
// they differ in what UserMessage/History they supply and what gets
// persisted afterward, not in how a turn is actually run and streamed.
func runStageTurn(ctx context.Context, runner agentrunner.AgentRunner, in agentrunner.RunInput, writeEvent func(chatStreamEvent)) (content string, proposed *chat.ToolCall, err error) {
	out, runErr := runner.Run(ctx, in, func(d chat.Delta) error {
		writeEvent(chatStreamEvent{Content: d.Content, ReasoningContent: d.ReasoningContent})
		return nil
	})
	toolCall := out.ToolCall
	// A local OpenAI-compatible model can hallucinate a tool_calls delta for
	// a tool it was never offered (e.g. one primed by the "explore the
	// repo" instruction in the system prompt but never actually registered
	// here) — only ever trust a call whose name matches the one tool this
	// turn actually offered, in.Tool, or a hallucination gets surfaced to
	// the human as a real Draft proposal and persisted as one.
	if toolCall != nil && toolCall.Function.Name != in.Tool.Function.Name {
		logrus.WithFields(logrus.Fields{
			"session_key": in.SessionKey, "expected_tool": in.Tool.Function.Name, "got_tool": toolCall.Function.Name,
		}).Warn("ignoring tool call that doesn't match the stage's registered tool")
		toolCall = nil
	}
	if toolCall != nil {
		writeEvent(chatStreamEvent{ToolCall: &chatToolCallEvent{
			Name:      toolCall.Function.Name,
			Arguments: toolCall.Function.Arguments,
		}})
	}
	return out.Content, toolCall, runErr
}

// conversationHistoryToChatMessages maps a stage's persisted Conversation
// into the chat.Message shape agentrunner.RunInput.History expects, so an
// AgentRunner that lost its in-memory session (e.g. a server restart wiped
// ClaudeRunner's cached clients or ChatClientRunner's held history) can
// rehydrate from the durable record instead of starting the interview over.
// A tool-call proposal is flattened into a short annotation on the
// assistant message's content rather than reconstructed as a structured
// tool-call turn — this is a best-effort transcript for the model to read,
// not a protocol-valid replay of the original exchange.
func conversationHistoryToChatMessages(conv task.Conversation) []chat.Message {
	if len(conv.Messages) == 0 {
		return nil
	}
	out := make([]chat.Message, 0, len(conv.Messages))
	for _, m := range conv.Messages {
		content := m.Content
		if m.ToolCall != nil {
			content += fmt.Sprintf("\n(proposed a draft via %s: %s)", m.ToolCall.Name, m.ToolCall.Arguments)
		}
		out = append(out, chat.Message{Role: m.Role, Content: content})
	}
	return out
}

// buildStagePrompt seeds the interview's system prompt with the task's own
// fields, the owning project's fields, and the resolved body text of every
// knowledge concept either references (CONTEXT.md's GrillMe/Planning Mode
// entries). A concept that fails to resolve is logged and skipped rather
// than failing the whole request — the same "one bad entry doesn't fail
// everything" spirit as FileStore.List().
func buildStagePrompt(t task.Task, proj project.Project, stage string, knowledgeReader KnowledgeReader) string {
	var b strings.Builder

	switch stage {
	case task.StageRequirements:
		b.WriteString(grillMeSystemPrompt)
	case task.StagePlanning:
		b.WriteString(planningModeSystemPrompt)
	}

	fmt.Fprintf(&b, "## Task\nObjective: %s\n", t.Objective)
	if len(t.Constraints) > 0 {
		fmt.Fprintf(&b, "Constraints:\n- %s\n", strings.Join(t.Constraints, "\n- "))
	}
	if len(t.Assumptions) > 0 {
		fmt.Fprintf(&b, "Assumptions:\n- %s\n", strings.Join(t.Assumptions, "\n- "))
	}
	if len(t.SuccessCriteria) > 0 {
		fmt.Fprintf(&b, "Success criteria:\n- %s\n", strings.Join(t.SuccessCriteria, "\n- "))
	}

	fmt.Fprintf(&b, "\n## Project: %s\n%s\n", proj.Name, proj.Description)
	if len(proj.Constraints) > 0 {
		fmt.Fprintf(&b, "Project constraints:\n- %s\n", strings.Join(proj.Constraints, "\n- "))
	}
	if len(proj.Repositories) > 0 {
		fmt.Fprintf(&b, "Repositories:\n- %s\n", strings.Join(proj.Repositories, "\n- "))
	}

	conceptIDs := append(append([]string{}, proj.Knowledge...), t.References.Knowledge...)
	for _, id := range conceptIDs {
		concept, err := knowledgeReader.Get(id)
		if err != nil {
			logrus.WithError(err).WithField("concept", id).Warn("skipping knowledge concept that failed to resolve")
			continue
		}
		fmt.Fprintf(&b, "\n## Knowledge: %s\n%s\n", id, concept.Body)
	}

	return b.String()
}

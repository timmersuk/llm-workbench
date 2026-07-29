package api

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"path/filepath"
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
// propose_context, Planning Mode registers propose_plan, and Review
// registers both propose_review and propose_knowledge at once. The actual
// name/description/schema live in internal/drafttool, shared with
// cmd/draftmcp's static MCP server (CodexRunner's Draft-tool mechanism) so
// both call sites see the same tool shape by construction.
const (
	proposeContextToolName   = drafttool.ProposeContextName
	proposePlanToolName      = drafttool.ProposePlanName
	proposeReviewToolName    = drafttool.ProposeReviewName
	proposeKnowledgeToolName = drafttool.ProposeKnowledgeName
	askQuestionToolName      = drafttool.AskQuestionName
)

// grillMeSystemPrompt and planningModeSystemPrompt encode the "grilling"
// interview discipline (CONTEXT.md's GrillMe/Planning Mode entries) both
// stages share: one question at a time, a recommended answer with every
// question, questions resolved in dependency order, and no proposal until
// the human has confirmed shared understanding. They differ only in what
// they're interviewing toward and which tool they end with. The
// recommended-answer rule is expressed as a call to ask_question
// (internal/drafttool) rather than prose written into the reply: the
// question text itself still belongs in the assistant's normal message
// content, but the selectable options/recommendation travel as the tool
// call's structured arguments, which the frontend renders as clickable
// choices (StageConversationPanel.tsx) instead of text the human has to
// read and retype. This composes with the one-question-per-turn discipline
// below rather than replacing it — only the transport of the recommended
// answer changed, not the pacing or judgment rules around it.
const (
	grillMeSystemPrompt = `You are GrillMe, interviewing the user to sharpen a task's requirements.

Rules for this interview:
- Before exploring the repo or asking anything, check the "Decisions already settled in this conversation" list below (if present) and this conversation's own history. If the point you're about to research or ask about is already there, it is final — do not re-research or re-ask it, just proceed as if it were still true. Fresh repo research turning up nothing (e.g. an empty Glob/Grep) is not by itself evidence of an open question.
- If you have tools available (Read/Grep/Glob), explore the project's repository first and answer your own questions from the code wherever you can. Only ask the human what the code cannot tell you.
- Ask exactly one question per turn. Never batch multiple questions into one message.
- When a question has a small set of sensible answers, call ask_question with those options, your recommended one, and a short reason why — put the question text itself in your normal reply, not in the tool call. Present the recommendation as a default the user can accept or redirect, not a decision already made. If the question is genuinely open-ended (no useful fixed set of answers), just ask it in your reply text without calling ask_question.
- Walk the design tree: resolve dependent decisions in order, one branch at a time, rather than jumping around.
- Do not call propose_context until the objective, constraints, assumptions, and success criteria are coherent AND the user has confirmed shared understanding — do not propose on your own initiative just because you have enough to guess.
- If the user's reply contains a fenced JSON block representing a requested change to a draft you already proposed, treat that block as the authoritative starting point for your revision — refine it, don't discard it and start over.

`
	planningModeSystemPrompt = `You are Planning Mode, interviewing the user to produce a structured execution plan.

Rules for this interview:
- Before exploring the repo or asking anything, check the "Decisions already settled in this conversation" list below (if present) and this conversation's own history. If the point you're about to research or ask about is already there, it is final — do not re-research or re-ask it, just proceed as if it were still true. Fresh repo research turning up nothing (e.g. an empty Glob/Grep) is not by itself evidence of an open question.
- If you have tools available (Read/Grep/Glob), explore the project's repository first and answer your own questions from the code wherever you can. Only ask the human what the code cannot tell you.
- Ask exactly one question per turn. Never batch multiple questions into one message.
- When a question has a small set of sensible answers, call ask_question with those options, your recommended one, and a short reason why — put the question text itself in your normal reply, not in the tool call. Present the recommendation as a default the user can accept or redirect, not a decision already made. If the question is genuinely open-ended (no useful fixed set of answers), just ask it in your reply text without calling ask_question.
- Walk the design tree: resolve dependent decisions in order (approach, then steps, then risks and complexity), one branch at a time, rather than jumping around.
- Do not call propose_plan until the approach, steps, risks, and estimated complexity are coherent AND the user has confirmed shared understanding — do not propose on your own initiative just because you have enough to guess.
- If the user's reply contains a fenced JSON block representing a requested change to a plan you already proposed, treat that block as the authoritative starting point for your revision — refine it, don't discard it and start over.

`
	// reviewSystemPrompt drives the Review-stage conversation (CONTEXT.md's
	// **Review** entry, docs/milestones/done/milestone6.md "The review mechanism").
	// Unlike GrillMe/Planning Mode, which interview toward a new artifact, this
	// reviews a *prior* one — the execution's commits/changed-files summary is
	// supplied in the prompt addendum below, but the diff itself is not: the
	// agent is told to fetch it with its own confined bash tool (see
	// buildReviewContext's doc comment for why) — working through three phases
	// the human can interrupt at any point. Your workspace is the execution's
	// isolated git worktree, and bash is available (confined to it) so you
	// actually run the project's tests rather than guessing whether they pass.
	// A failing check is never an automatic verdict: surface findings and let
	// the human decide via Finalize.
	reviewSystemPrompt = `You are reviewing a completed execution of this task, conversing with the human who will make the final call. You are in the execution's isolated git worktree; you have read-only tools (read_file/grep_search/glob) and a confined bash tool that runs the project's own commands from that worktree. The prompt below tells you the exact git diff command to run for the change under review — start there.

Work through three phases, narrating what you find as you go and pausing for the human whenever they want to weigh in:
1. Automated checks: run the diff command noted below to see the change, run the project's test suite with bash, and do a Standards + Spec pass over the diff (does the change match the codebase's conventions, and does it actually do what the task asked?). Report what passed and what didn't.
2. Test-meaningfulness: look at what the tests in the diff actually assert, not just whether they pass — flag a test that can't fail, or one that doesn't exercise the code path it claims to cover.
3. Per-verification-step confirmation: walk the task's verification steps below one by one. Attempt each agent_executable step yourself (run the command, hit the endpoint, drive the check) and report what you observed; for each human_judgment step, ask the human to perform it and record their confirmation.

A failing check is not a rejection on its own — humans own the intent. Surface findings inside the conversation; do not decide the outcome unilaterally.

Do not call propose_review until you have worked through the checks AND the human has confirmed the outcome. Propose the decision (approved | rejected | needs_changes) with notes summarizing the findings that justify it. If the human's reply contains a fenced JSON block editing a review you already proposed, treat that block as the authoritative starting point for your revision.

If this review surfaces a durable, reusable learning — a coding standard worth codifying, a pitfall worth recording, a decision worth explaining to a future task — call propose_knowledge with the concept's full content (concept_id, type, frontmatter, body) for the human to accept or reject, alongside (not instead of) proposing the review verdict itself. Not every review has one; only propose when there is something genuinely worth keeping.

`
)

// kickoffUserMessageFor drives a stage conversation's very first turn
// (handleStartStageConversation) — chat completion APIs need a user-role
// message to produce a reply at all, but there is no real human reply yet
// on a brand-new conversation. This is never shown to the human or
// persisted; only the assistant's resulting first turn is. Worded per stage
// rather than as one shared constant: Requirements/Planning are genuinely
// interviewing toward a new artifact ("ask your first question"), but
// Review works through a prior execution's diff in phases (reviewSystemPrompt
// above) — reusing the interview wording there told the agent to "ask its
// first question" when its own system prompt says to run checks first,
// a real mismatch an executor had to notice and route around at runtime.
func kickoffUserMessageFor(stage string) string {
	switch stage {
	case task.StageReview:
		return "Begin the review now: use the task/project/knowledge context above (and the repository, since you have tools) to work through the three phases and report your findings."
	default:
		return "Begin the interview now: use the task/project/knowledge context above (and the repository, if you have tools) to ask your first question."
	}
}

// chatToolFor adapts a drafttool.Definition into the chat.Tool shape
// RunInput.Tools expects.
func chatToolFor(d drafttool.Definition) chat.Tool {
	return chat.Tool{Type: "function", Function: chat.ToolSchema{
		Name:        d.Name,
		Description: d.Description,
		Parameters:  d.Schema,
	}}
}

// stageTool returns the Draft-proposing tool(s) registered for stage, and
// whether stage is a valid Conversation stage at all (requirements,
// planning, or review — see task.ErrInvalidStage). Requirements/Planning
// each offer their own Draft-proposing tool plus ask_question (the
// structured interview-question affordance, internal/drafttool) — a model
// can call either one on a given turn, same as Review's two-at-once shape
// below, just never both. Review offers two at once — propose_review (the
// stage's own verdict) and propose_knowledge (folding a durable learning
// into the Knowledge layer, docs/milestones/done/milestone9.md) — since either
// may be called independently within the same conversation, not in a fixed
// order; ask_question is deliberately not offered there — reviewSystemPrompt
// has no "one question per turn" interview discipline for this to compose
// with. Name/description/schema come from internal/drafttool, shared with
// cmd/draftmcp.
func stageTool(stage string) ([]chat.Tool, bool) {
	switch stage {
	case task.StageRequirements:
		return []chat.Tool{chatToolFor(drafttool.ProposeContext), chatToolFor(drafttool.AskQuestion)}, true
	case task.StagePlanning:
		return []chat.Tool{chatToolFor(drafttool.ProposePlan), chatToolFor(drafttool.AskQuestion)}, true
	case task.StageReview:
		return []chat.Tool{chatToolFor(drafttool.ProposeReview), chatToolFor(drafttool.ProposeKnowledge)}, true
	default:
		return nil, false
	}
}

// requireCurrentStage returns task.ErrWrongStage if stage — the URL's
// stage path segment — doesn't match t's actual current Stage. stageTool()
// only checks that stage names a Conversation stage at all; this is the
// separate cross-check that the URL agrees with reality, closing the gap a
// "trusts the caller" audit found (docs/milestones/done/milestone7.md's PR 5):
// without it, a task at implementation could still be posted to via
// .../stage/requirements/message and pollute that stale conversation. Not a
// task.FileStore method like every other ErrWrongStage producer — this
// guards nothing on disk, so it belongs here with stageTool()'s own
// request-shape validation rather than in the domain package.
func requireCurrentStage(t task.Task, stage string) error {
	if t.Stage != stage {
		return task.ErrWrongStage
	}
	return nil
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
func (s *Server) handleGetStageConversation() http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		stage := r.PathValue("stage")
		if _, ok := stageTool(stage); !ok {
			http.Error(w, fmt.Sprintf("invalid stage %q", stage), http.StatusBadRequest)
			return
		}

		projectId := r.PathValue("projectId")
		store, ok := s.resolveTaskStore(w, projectId)
		if !ok {
			return
		}

		taskId := r.PathValue("taskId")
		t, err := store.Get(projectId, taskId)
		if err != nil {
			writeGetError(w, err)
			return
		}
		if err := requireCurrentStage(t, stage); err != nil {
			writeGetError(w, err)
			return
		}

		conv, err := store.GetConversation(projectId, taskId, stage)
		if err != nil {
			writeGetError(w, err)
			return
		}
		writeJSON(w, http.StatusOK, conv)
	}
}

// stageStreamTarget bundles what a streaming stage handler needs once the
// per-request boilerplate — executor lookup plus owning-project/task
// resolution — has run: the resolved runner, project, task store, and task.
// The three streaming handlers all consult these, resolved once here so each
// handler is left with only its distinct pre-stream logic.
type stageStreamTarget struct {
	runner    agentrunner.AgentRunner
	proj      project.Project
	store     TaskStore
	projectId string
	task      task.Task
}

// resolveStageStreamTarget selects the executor's runner (defaulting to
// defaultChatExecutor when unset — the same convention as chat.go's
// free-floating endpoint) and resolves the owning project and task. It writes
// the appropriate 400/404 and returns false on any failure; every one of these
// fires before the SSE headers are sent, so they surface as real HTTP status
// codes. Callers must already have validated the stage and decoded their
// request body first — the request types differ, so decode stays per-handler,
// and validating the stage before touching the body preserves the order in
// which those two 400s fire.
func (s *Server) resolveStageStreamTarget(w http.ResponseWriter, executorKey, projectId, taskId string) (stageStreamTarget, bool) {
	if executorKey == "" {
		executorKey = defaultChatExecutor
	}
	runner, ok := s.AgentRunners[executorKey]
	if !ok {
		http.Error(w, fmt.Sprintf("unknown executor %q", executorKey), http.StatusBadRequest)
		return stageStreamTarget{}, false
	}

	proj, err := s.Projects.Get(projectId)
	if err != nil {
		writeGetError(w, err)
		return stageStreamTarget{}, false
	}
	store := s.Tasks

	t, err := store.Get(projectId, taskId)
	if err != nil {
		writeGetError(w, err)
		return stageStreamTarget{}, false
	}

	return stageStreamTarget{runner: runner, proj: proj, store: store, projectId: projectId, task: t}, true
}

// beginStageStream confirms the ResponseWriter can stream, writes the SSE
// response headers (200 OK), and returns the writeEvent closure the turn uses
// to relay chatStreamEvents (the wire shape shared with chat.go). Once this
// returns true the headers are committed: from that point a failure can no
// longer surface as an HTTP status, only as a final SSE {Error:...} event — so
// callers must complete every real-HTTP-status check before calling this, and
// treat everything after it as stream-only. "streaming unsupported" itself is
// still an HTTP 500 because it happens before any header is written.
func beginStageStream(w http.ResponseWriter) (func(chatStreamEvent), bool) {
	flusher, ok := w.(http.Flusher)
	if !ok {
		http.Error(w, "streaming unsupported", http.StatusInternalServerError)
		return nil, false
	}

	w.Header().Set("Content-Type", "text/event-stream")
	w.Header().Set("Cache-Control", "no-cache")
	w.Header().Set("Connection", "keep-alive")
	w.WriteHeader(http.StatusOK)

	return func(ev chatStreamEvent) {
		data, _ := json.Marshal(ev)
		fmt.Fprintf(w, "data: %s\n\n", data)
		flusher.Flush()
	}, true
}

// stageAssistantMessage assembles the assistant turn to persist once a stream
// has ended: its accumulated content, the stream error stamped onto .Error
// (already surfaced to the human as an SSE event, recorded here so the durable
// transcript keeps it too), any Draft tool call the model proposed
// (CONTEXT.md), flattened into the persisted ConversationToolCall shape, and
// the turn's Tool Activity (docs/adr/0018), already capped by runStageTurn.
// All three streaming handlers build this identically; they differ only in
// how they then pair or replace it in the record.
func stageAssistantMessage(content string, proposed *chat.ToolCall, activity []task.ConversationToolActivity, streamErr error) task.ConversationMessage {
	msg := task.ConversationMessage{Role: "assistant", Content: content, ToolActivity: activity}
	if streamErr != nil {
		msg.Error = streamErr.Error()
	}
	if proposed != nil {
		msg.ToolCall = &task.ConversationToolCall{
			ID:        proposed.ID,
			Name:      proposed.Function.Name,
			Arguments: proposed.Function.Arguments,
		}
	}
	return msg
}

// handlePostStageMessage posts a user message to a stage's Conversation,
// streams the assistant's reply as SSE (reusing chatStreamEvent, same wire
// shape as the free-floating chat endpoint in chat.go), and persists both
// messages once the stream ends. If the model calls the stage's registered
// tool, that's surfaced mid-stream as a chatStreamEvent.ToolCall — the
// Draft itself (CONTEXT.md) — for the frontend to render for review; it is
// not persisted or written to disk here, only Finalize (finalize.go) does
// that.
func (s *Server) handlePostStageMessage() http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		stage := r.PathValue("stage")
		tools, ok := stageTool(stage)
		if !ok {
			http.Error(w, fmt.Sprintf("invalid stage %q", stage), http.StatusBadRequest)
			return
		}

		var req stageMessageRequest
		if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
			http.Error(w, "invalid request body", http.StatusBadRequest)
			return
		}

		taskId := r.PathValue("taskId")
		target, ok := s.resolveStageStreamTarget(w, req.Executor, r.PathValue("projectId"), taskId)
		if !ok {
			return
		}
		if err := requireCurrentStage(target.task, stage); err != nil {
			writeGetError(w, err)
			return
		}

		writeEvent, ok := beginStageStream(w)
		if !ok {
			return
		}

		var assistantContent string
		var proposed *chat.ToolCall
		var activity []task.ConversationToolActivity
		var streamErr error

		// resolveStageRun resolves the workspace and (for Review) appends the
		// execution's diff to the prompt and enables bash. A project with no
		// configured repository is tolerated for Requirements/Planning (an
		// empty workspace, a text-only turn); any other resolution failure
		// aborts the turn as an SSE error, since headers are already sent.
		history, convErr := target.store.GetConversation(target.projectId, taskId, stage)
		var run stageRun
		var runErr error
		if convErr != nil {
			streamErr = fmt.Errorf("loading conversation history: %w", convErr)
		} else if run, runErr = s.resolveStageRun(r.Context(), target.proj, target.store, target.projectId, target.task, stage, history); runErr != nil {
			streamErr = runErr
		} else {
			assistantContent, proposed, activity, streamErr = runStageTurn(r.Context(), target.runner, agentrunner.RunInput{
				SessionKey:     taskId + ":" + stage,
				Workspace:      run.Workspace,
				SystemPrompt:   run.SystemPrompt,
				UserMessage:    req.Content,
				Model:          req.Model,
				Tools:          tools,
				EnableBashTool: run.EnableBash,
				MaxTurns:       run.MaxTurns,
				History:        conversationHistoryToChatMessages(history),
			}, writeEvent)
		}
		if streamErr != nil {
			// Headers (200 OK) are already sent, so a failed stream can't
			// surface as an HTTP error status — relayed as a final SSE
			// event instead, matching handleChatCompletions (chat.go).
			writeEvent(chatStreamEvent{Error: streamErr.Error()})
		}

		assistantMsg := stageAssistantMessage(assistantContent, proposed, activity, streamErr)

		if _, err := target.store.AppendConversationMessages(target.projectId, taskId, stage,
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
// agent turn seeded with kickoffUserMessageFor(stage) (never shown or persisted)
// instead of waiting for a human message that doesn't exist yet, and
// persists only the resulting assistant turn — there is no human message to
// pair it with. Rejects with 409 if the conversation already has messages,
// since starting is only meaningful once, before any real exchange exists;
// continuing an already-started conversation is handlePostStageMessage's
// job.
func (s *Server) handleStartStageConversation() http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		stage := r.PathValue("stage")
		tools, ok := stageTool(stage)
		if !ok {
			http.Error(w, fmt.Sprintf("invalid stage %q", stage), http.StatusBadRequest)
			return
		}

		var req stageStartRequest
		if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
			http.Error(w, "invalid request body", http.StatusBadRequest)
			return
		}

		taskId := r.PathValue("taskId")
		target, ok := s.resolveStageStreamTarget(w, req.Executor, r.PathValue("projectId"), taskId)
		if !ok {
			return
		}
		if err := requireCurrentStage(target.task, stage); err != nil {
			writeGetError(w, err)
			return
		}

		existing, err := target.store.GetConversation(target.projectId, taskId, stage)
		if err != nil {
			writeGetError(w, err)
			return
		}
		if len(existing.Messages) > 0 {
			http.Error(w, "conversation already started", http.StatusConflict)
			return
		}

		writeEvent, ok := beginStageStream(w)
		if !ok {
			return
		}

		var assistantContent string
		var proposed *chat.ToolCall
		var activity []task.ConversationToolActivity
		var streamErr error

		run, runErr := s.resolveStageRun(r.Context(), target.proj, target.store, target.projectId, target.task, stage, existing)
		if runErr != nil {
			streamErr = runErr
		} else {
			assistantContent, proposed, activity, streamErr = runStageTurn(r.Context(), target.runner, agentrunner.RunInput{
				SessionKey:     taskId + ":" + stage,
				Workspace:      run.Workspace,
				SystemPrompt:   run.SystemPrompt,
				UserMessage:    kickoffUserMessageFor(stage),
				Model:          req.Model,
				Tools:          tools,
				EnableBashTool: run.EnableBash,
				MaxTurns:       run.MaxTurns,
			}, writeEvent)
		}
		if streamErr != nil {
			writeEvent(chatStreamEvent{Error: streamErr.Error()})
		}

		assistantMsg := stageAssistantMessage(assistantContent, proposed, activity, streamErr)

		if _, err := target.store.AppendConversationMessages(target.projectId, taskId, stage, assistantMsg); err != nil {
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
func (s *Server) handleDeleteStageMessage() http.HandlerFunc {
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

		projectId := r.PathValue("projectId")
		store, ok := s.resolveTaskStore(w, projectId)
		if !ok {
			return
		}

		taskId := r.PathValue("taskId")
		t, err := store.Get(projectId, taskId)
		if err != nil {
			writeGetError(w, err)
			return
		}
		if err := requireCurrentStage(t, stage); err != nil {
			writeGetError(w, err)
			return
		}

		existing, err := store.GetConversation(projectId, taskId, stage)
		if err != nil {
			writeGetError(w, err)
			return
		}
		if index < 0 || index >= len(existing.Messages) {
			http.Error(w, fmt.Sprintf("message index %d out of range", index), http.StatusBadRequest)
			return
		}

		updated := append(append([]task.ConversationMessage{}, existing.Messages[:index]...), existing.Messages[index+1:]...)
		conv, err := store.ReplaceConversationMessages(projectId, taskId, stage, updated)
		if err != nil {
			writeGetError(w, err)
			return
		}

		s.closeSessions(taskId + ":" + stage)
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
func (s *Server) handleRegenerateStageMessage() http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		stage := r.PathValue("stage")
		tools, ok := stageTool(stage)
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

		taskId := r.PathValue("taskId")
		target, ok := s.resolveStageStreamTarget(w, req.Executor, r.PathValue("projectId"), taskId)
		if !ok {
			return
		}
		if err := requireCurrentStage(target.task, stage); err != nil {
			writeGetError(w, err)
			return
		}

		existing, err := target.store.GetConversation(target.projectId, taskId, stage)
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

		writeEvent, ok := beginStageStream(w)
		if !ok {
			return
		}

		sessionKey := taskId + ":" + stage
		s.closeSessions(sessionKey)

		var assistantContent string
		var proposed *chat.ToolCall
		var activity []task.ConversationToolActivity
		var streamErr error

		run, runErr := s.resolveStageRun(r.Context(), target.proj, target.store, target.projectId, target.task, stage, task.Conversation{Messages: historyPrefix})
		if runErr != nil {
			streamErr = runErr
		} else {
			assistantContent, proposed, activity, streamErr = runStageTurn(r.Context(), target.runner, agentrunner.RunInput{
				SessionKey:     sessionKey,
				Workspace:      run.Workspace,
				SystemPrompt:   run.SystemPrompt,
				UserMessage:    req.Content,
				Model:          req.Model,
				Tools:          tools,
				EnableBashTool: run.EnableBash,
				MaxTurns:       run.MaxTurns,
				History:        conversationHistoryToChatMessages(task.Conversation{Messages: historyPrefix}),
			}, writeEvent)
		}
		if streamErr != nil {
			writeEvent(chatStreamEvent{Error: streamErr.Error()})
		}

		assistantMsg := stageAssistantMessage(assistantContent, proposed, activity, streamErr)

		now := time.Now().UTC()
		userMsg := task.ConversationMessage{Role: "user", Content: req.Content, CreatedAt: now}
		assistantMsg.CreatedAt = now

		newMessages := append(historyPrefix, userMsg, assistantMsg)
		if _, err := target.store.ReplaceConversationMessages(target.projectId, taskId, stage, newMessages); err != nil {
			logrus.WithError(err).WithFields(logrus.Fields{"task": taskId, "stage": stage}).Error("persisting regenerated stage conversation messages")
			writeEvent(chatStreamEvent{Error: fmt.Sprintf("saving conversation: %v", err)})
		}
	}
}

// runStageTurn runs one agent turn and streams its deltas via writeEvent
// (the chatStreamEvent shape both stage-conversation endpoints share),
// returning the assistant's accumulated content, any proposed Draft tool
// call, and the turn's Tool Activity for the caller to persist
// (docs/adr/0018). Shared by handlePostStageMessage and
// handleStartStageConversation — they differ in what UserMessage/History
// they supply and what gets persisted afterward, not in how a turn is
// actually run and streamed.
func runStageTurn(ctx context.Context, runner agentrunner.AgentRunner, in agentrunner.RunInput, writeEvent func(chatStreamEvent)) (content string, proposed *chat.ToolCall, activity []task.ConversationToolActivity, err error) {
	// Surface the agent's intermediate tool activity (the executed
	// read_file/grep_search/glob/bash calls and their results) live, so a
	// client can render "ran go test ./... -> ok" as it happens, AND
	// accumulate the same calls into activity for the caller to persist
	// (capped via task.TruncateForPersistence, independent of whatever the
	// live SSE event above carries). These are the loop's EXECUTED tools —
	// kept distinct from the single final Draft (validated against in.Tools
	// below), which is never routed here. General across all three stages:
	// Requirements/Planning only ever make read-only calls, Review adds bash.
	in.OnToolCall = func(name, argsJSON string) {
		writeEvent(chatStreamEvent{ToolActivity: &chatToolActivityEvent{Phase: "call", Name: name, Arguments: argsJSON}})
		activity = append(activity, task.ConversationToolActivity{Name: name, Arguments: task.TruncateForPersistence(argsJSON)})
	}
	in.OnToolResult = func(name, result string, isError bool) {
		writeEvent(chatStreamEvent{ToolActivity: &chatToolActivityEvent{Phase: "result", Name: name, Result: result, IsError: isError}})
		// Fill in the most recent call still missing its result — calls and
		// results arrive strictly paired and in order (both the toolloop
		// engine and the claude/codex CLI integrations execute/report one
		// call's result before the next call happens), so the last entry is
		// always the one this result belongs to.
		if n := len(activity); n > 0 {
			activity[n-1].Result = task.TruncateForPersistence(result)
			activity[n-1].IsError = isError
		}
	}
	out, runErr := runner.Run(ctx, in, func(d chat.Delta) error {
		writeEvent(chatStreamEvent{Content: d.Content, ReasoningContent: d.ReasoningContent, Usage: usageEvent(d.Usage)})
		return nil
	})
	toolCall := out.ToolCall
	// A local OpenAI-compatible model can hallucinate a tool_calls delta for
	// a tool it was never offered (e.g. one primed by the "explore the
	// repo" instruction in the system prompt but never actually registered
	// here) — only ever trust a call whose name matches one of the tools
	// this turn actually offered, in.Tools (usually one; Review offers two
	// at once), or a hallucination gets surfaced to the human as a real
	// Draft proposal and persisted as one.
	if toolCall != nil && !offersToolNamed(in.Tools, toolCall.Function.Name) {
		logrus.WithFields(logrus.Fields{
			"session_key": in.SessionKey, "expected_tools": toolNames(in.Tools), "got_tool": toolCall.Function.Name,
		}).Warn("ignoring tool call that doesn't match any of the stage's registered tools")
		toolCall = nil
	}
	if toolCall != nil {
		writeEvent(chatStreamEvent{ToolCall: &chatToolCallEvent{
			Name:      toolCall.Function.Name,
			Arguments: toolCall.Function.Arguments,
		}})
	}
	return out.Content, toolCall, activity, runErr
}

// offersToolNamed reports whether tools contains one named name.
func offersToolNamed(tools []chat.Tool, name string) bool {
	for _, t := range tools {
		if t.Function.Name == name {
			return true
		}
	}
	return false
}

// toolNames returns the bare names of tools, for logging.
func toolNames(tools []chat.Tool) []string {
	names := make([]string, len(tools))
	for i, t := range tools {
		names[i] = t.Function.Name
	}
	return names
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

// resolvedDecisionsSummary extracts a compact "already settled" list from
// conv, appended to the system prompt so the model has a cheap, explicit
// checklist to consult before re-deriving or re-asking something instead of
// relying solely on it noticing the answer buried in a long,
// tool-activity-heavy transcript (observed in practice: the same interview
// re-asked both an "own vs. supervise the process" fork and an
// icon-sourcing question it had already settled many turns earlier). Only
// ask_question turns count as a settled decision — propose_context/
// propose_plan/propose_review calls are drafts of the whole artifact, not a
// single resolved point, so they're excluded. Returns "" when there are no
// ask_question turns yet (a fresh or short conversation), so a normal
// interview's prompt is unchanged.
func resolvedDecisionsSummary(conv task.Conversation) string {
	var decisions []string
	for i, m := range conv.Messages {
		if m.Role != "assistant" || m.ToolCall == nil || m.ToolCall.Name != "ask_question" {
			continue
		}
		if i+1 >= len(conv.Messages) || conv.Messages[i+1].Role != "user" {
			continue
		}
		if answer := strings.TrimSpace(conv.Messages[i+1].Content); answer != "" {
			decisions = append(decisions, answer)
		}
	}
	if len(decisions) == 0 {
		return ""
	}

	var b strings.Builder
	b.WriteString("\n## Decisions already settled in this conversation\nThese are final — do not re-research or re-ask any of them unless the user reopens one:\n")
	for _, d := range decisions {
		fmt.Fprintf(&b, "- %s\n", d)
	}
	return b.String()
}

// buildStagePrompt seeds the interview's system prompt with the task's own
// fields, the owning project's fields, and the resolved body text of every
// knowledge concept either references (CONTEXT.md's GrillMe/Planning Mode
// entries). A concept that fails to resolve is logged and skipped rather
// than failing the whole request — the same "one bad entry doesn't fail
// everything" spirit as FileStore.List().
func (s *Server) buildStagePrompt(t task.Task, proj project.Project, stage string) string {
	var b strings.Builder

	switch stage {
	case task.StageRequirements:
		b.WriteString(grillMeSystemPrompt)
	case task.StagePlanning:
		b.WriteString(planningModeSystemPrompt)
	case task.StageReview:
		b.WriteString(reviewSystemPrompt)
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
		concept, err := s.KnowledgeStore.Get(id)
		if err != nil {
			logrus.WithError(err).WithField("concept", id).Warn("skipping knowledge concept that failed to resolve")
			continue
		}
		fmt.Fprintf(&b, "\n## Knowledge: %s\n%s\n", id, concept.Body)
	}

	return b.String()
}

// requirementsPlanningMaxTurns bounds Requirements/Planning stage turns —
// these agents stay read-only, so a short interview-style conversation
// exhausting this is itself a signal something's gone wrong (a loop, a
// confused agent), not evidence the cap needs raising.
const requirementsPlanningMaxTurns = 30

// reviewMaxTurns bounds Review stage turns. Review runs the confined bash
// tool over the executed change (tests, live smoke-testing) — a workload
// shaped like Execute's, not like an interview — so it gets Execute's same
// generous budget rather than requirementsPlanningMaxTurns.
const reviewMaxTurns = 1000

// stageRun bundles the resolved workspace, the (possibly stage-augmented)
// system prompt, whether the confined bash tool should be offered, and the
// turn budget, for one stage conversation turn — the three stage-conversation
// handlers share it so Review's extra plumbing (worktree, diff, bash, higher
// turn budget) lives in exactly one place. MaxTurns is decided here, by the
// caller, per stage — never inferred by an AgentRunner from EnableBash or any
// other signal (see agentrunner.RunInput.MaxTurns).
type stageRun struct {
	Workspace    string
	SystemPrompt string
	EnableBash   bool
	MaxTurns     int
}

// appendWorkspaceAdvisories appends a short, one-sentence note per true
// advisory signal (docs/milestones/done/milestone8a.md's "Advisory checks") to
// systemPrompt — never when a signal is clean or unknown, so a healthy
// checkout doesn't clutter every conversation with reassuring or unhelpful
// "can't tell" noise, matching buildRejectedReviewContext's existing
// minimal-noise convention (no addendum when there's nothing to report).
// Always checked against the project's shared checkout (proj.Repositories
// via GetWorkspaceStatus), never whatever workspace this particular turn
// actually resolved to — Review's own workspace is an isolated execution
// worktree (ResolveReviewWorkspace), but the hygiene these signals describe
// is a shared-checkout property regardless of which stage is asking, and
// ResolveReviewWorkspace itself resolves the shared checkout internally on
// every call anyway (worktree.go). Best-effort: a failure here (most
// commonly ErrNoRepository, a project with no configured repo) just means
// no advisory text is added — this must never fail the turn, matching
// every other advisory-only contract in this milestone. A method (using
// s.ReposRoot) rather than a free function taking reposRoot as a parameter
// — the same shape as this file's other invariant-mixing helpers
// (docs/adr/0016), folded in even though it postdates
// docs/milestones/done/milestone8b.md's original scan (it shipped in
// Milestone 8a PR 3, after that scan).
func (s *Server) appendWorkspaceAdvisories(ctx context.Context, systemPrompt string, repositories []string) string {
	status, err := agentrunner.GetWorkspaceStatus(ctx, s.ReposRoot, repositories)
	if err != nil {
		return systemPrompt
	}
	var b strings.Builder
	b.WriteString(systemPrompt)
	if status.BehindOrigin.Known && status.BehindOrigin.Behind > 0 {
		fmt.Fprintf(&b, "\nNote: the project's shared checkout is %d commit(s) behind its origin upstream.\n", status.BehindOrigin.Behind)
	}
	if status.Dirty.Known && status.Dirty.Dirty {
		b.WriteString("\nNote: the project's shared checkout has uncommitted changes (possibly including untracked files).\n")
	}
	return b.String()
}

// resolveStageRun assembles the stageRun for a turn. Requirements/Planning run
// read-only against the project's shared checkout (a project with no repo is
// tolerated, yielding an empty workspace and a text-only turn). Review runs
// against the execution's isolated worktree with bash enabled and the
// execution's diff + verification steps appended to the prompt — so the agent
// can actually run the tests and check the real change (Milestone 6).
//
// history is the exact message slice the caller is about to hand the runner
// as RunInput.History (the full persisted conversation for a normal turn, or
// the truncated prefix a Regenerate/Edit is replaying) — resolvedDecisionsSummary
// is derived from it rather than a fresh store fetch so the settled-decisions
// list never disagrees with what the model is actually being shown.
func (s *Server) resolveStageRun(ctx context.Context, proj project.Project, store TaskStore, projectId string, t task.Task, stage string, history task.Conversation) (stageRun, error) {
	systemPrompt := s.buildStagePrompt(t, proj, stage)
	systemPrompt = s.appendWorkspaceAdvisories(ctx, systemPrompt, proj.Repositories)
	systemPrompt += resolvedDecisionsSummary(history)

	if stage != task.StageReview {
		ws, err := agentrunner.ResolveWorkspace(ctx, s.ReposRoot, proj.Repositories)
		if err != nil && !errors.Is(err, agentrunner.ErrNoRepository) {
			return stageRun{}, fmt.Errorf("resolving workspace: %w", err)
		}
		if stage == task.StageRequirements {
			addendum, err := s.buildRejectedReviewContext(ctx, store, projectId, t, ws)
			if err != nil {
				return stageRun{}, err
			}
			systemPrompt += addendum
		}
		return stageRun{Workspace: ws, SystemPrompt: systemPrompt, MaxTurns: requirementsPlanningMaxTurns}, nil
	}

	defaultBranch, err := s.ensureDefaultBranch(ctx, proj)
	if err != nil {
		return stageRun{}, fmt.Errorf("determining default branch: %w", err)
	}
	addendum, workspace, err := s.buildReviewContext(ctx, proj, store, projectId, t.ID, defaultBranch)
	if err != nil {
		return stageRun{}, err
	}
	return stageRun{Workspace: workspace, SystemPrompt: systemPrompt + addendum, EnableBash: true, MaxTurns: reviewMaxTurns}, nil
}

// buildRejectedReviewContext returns a Requirements-stage prompt addendum
// surfacing the most recent review's notes when it was rejected —
// CONTEXT.md's **Review** entry already promises this ("rejected... with
// the review's notes surfaced into the reopened conversation"); this is
// what implements it (docs/milestones/done/milestone6.md's PR 5). Returns "" (no
// error) when the task has no reviews yet or the latest one wasn't
// rejected — StageRequirements is reachable from a rejected review or from
// the separate, unrelated "Revise Requirements" action (ReviseToRequirements),
// so this can also fire on that second path while an old rejection is still
// the latest one on record; that staleness is accepted rather than guarded
// against (docs/milestones/done/milestone6.md's PR 5, decision 1).
//
// ListExecutions's last entry is used for the rejected attempt's branch
// name without re-resolving a real workspace (ResolveReviewWorkspace would
// require a working git checkout just to read one string) — safe because as
// long as the latest review is rejected, no new execution can have been
// recorded since (executions are only created from StageImplementation,
// unreachable again without a fresh FinalizePlan first).
//
// When t.PullRequest is set (a rejection recorded from pr_review, after a PR
// was already pushed), this also fetches the PR's actual GitHub feedback and
// writes it to a gitignored scratch file under workspace
// (prCommentsRequirementsPath), referencing its path in the addendum —
// docs/adr/0015-pr-feedback-delivered-as-a-file-not-a-live-tool.md. A fetch
// failure here fails the whole turn, the same posture this function's own
// review/execution lookups already take (decision 6, M6 PR 5) — accepted for
// an external dependency for now; revisit toward graceful degradation only if
// GitHub's reliability proves a recurring practical problem.
func (s *Server) buildRejectedReviewContext(ctx context.Context, store TaskStore, projectId string, t task.Task, workspace string) (string, error) {
	reviews, err := store.ListReviews(projectId, t.ID)
	if err != nil {
		return "", fmt.Errorf("listing reviews for %s: %w", t.ID, err)
	}
	if len(reviews) == 0 {
		return "", nil
	}
	latest := reviews[len(reviews)-1]
	if latest.Decision != task.ReviewDecisionRejected {
		return "", nil
	}

	executions, err := store.ListExecutions(projectId, t.ID)
	if err != nil {
		return "", fmt.Errorf("listing executions for %s: %w", t.ID, err)
	}

	var b strings.Builder
	fmt.Fprintf(&b, "\n## This task was reopened after a rejected review\n")
	fmt.Fprintf(&b, "Review notes: %s\n", latest.Notes)
	if len(executions) > 0 {
		branch := agentrunner.ExecutionBranchName(t.ID, executions[len(executions)-1].ExecutionID)
		fmt.Fprintf(&b, "The rejected attempt's branch was %s — you have read_file_at_ref/list_files_at_ref to inspect its actual code (git refs only, not the working tree).\n", branch)
	}

	if t.PullRequest != nil && workspace != "" {
		path := prCommentsRequirementsPath(workspace, t.ID)
		if err := s.writePRCommentsFile(ctx, workspace, path, t.PullRequest.Number); err != nil {
			return "", fmt.Errorf("fetching PR comments for %s: %w", t.ID, err)
		}
		rel, relErr := filepath.Rel(workspace, path)
		if relErr != nil {
			rel = path
		}
		fmt.Fprintf(&b, "The PR opened for this task has real reviewer feedback on GitHub (comments, review verdicts, and inline code comments), saved to %s — read it with your file-reading tool for the reviewer's own words, in addition to the summary above.\n", filepath.ToSlash(rel))
	}

	return b.String(), nil
}

// buildReviewContext resolves the worktree of the task's most recent execution
// and builds the review prompt addendum: the execution's commits, its changed
// file list, and the task's structured verification steps for the agent to
// walk. Returns the addendum and the worktree path bash/the read-only tools
// are confined to.
//
// Unlike an earlier version of this function, the actual diff text is
// deliberately NOT inlined here — Review always has a confined bash tool over
// ws.Path (resolveStageRun's EnableBash), so the agent can run `git diff`
// itself, and the addendum tells it the exact command. Embedding the diff
// (previously capped at 24KB via maxReviewPatchBytes) was the single largest
// contributor to the review system prompt's size, and the claude CLI receives
// that whole prompt as one --system-prompt argument — on Windows, the total
// command line (this addendum plus everything else in resolveStageRun's
// prompt, plus systemPromptWithHistory's replayed conversation) shares a
// single ~32,767 character CreateProcess limit, so an inlined diff both hit
// an arbitrary truncation cliff on real changes and made that limit easier to
// blow. Commits/artifacts come from latest.Output (recorded by
// agentrunner.CollectExecutionOutput when the execution completed,
// internal/api/execution.go) rather than a fresh git call here.
func (s *Server) buildReviewContext(ctx context.Context, proj project.Project, store TaskStore, projectId, taskID, defaultBranch string) (addendum, workspace string, err error) {
	executions, err := store.ListExecutions(projectId, taskID)
	if err != nil {
		return "", "", fmt.Errorf("listing executions for review: %w", err)
	}
	if len(executions) == 0 {
		return "", "", fmt.Errorf("no execution to review for task %s", taskID)
	}
	// ListExecutions sorts ascending by the zero-padded id, so the last entry
	// is the most recent attempt — the one that advanced the task to review.
	latest := executions[len(executions)-1]

	ws, err := agentrunner.ResolveReviewWorkspace(ctx, s.ReposRoot, proj.Repositories, taskID, latest.ExecutionID, defaultBranch)
	if err != nil {
		return "", "", fmt.Errorf("resolving review workspace: %w", err)
	}

	var b strings.Builder
	fmt.Fprintf(&b, "\n## Execution under review: %s\n", latest.ExecutionID)
	fmt.Fprintf(&b, "Branch %s, based on %s.\n", ws.Branch, ws.BaseBranch)
	if len(latest.Output.Commits) > 0 {
		fmt.Fprintf(&b, "Commits (oldest first): %s\n", strings.Join(latest.Output.Commits, ", "))
	}
	if len(latest.Output.Artifacts) > 0 {
		fmt.Fprintf(&b, "Changed files:\n- %s\n", strings.Join(latest.Output.Artifacts, "\n- "))
	}
	fmt.Fprintf(&b, "\nRun `git diff %s...HEAD` with your bash tool in this worktree to see the full diff before starting the checks below.\n", ws.BaseBranch)

	// The structured verification steps are the checklist phase 3 walks. A
	// missing/unreadable context.yaml just omits them rather than failing the
	// whole review.
	if c, ctxErr := store.GetContext(projectId, taskID); ctxErr != nil {
		logrus.WithError(ctxErr).WithField("task", taskID).Warn("review: skipping verification steps (context unavailable)")
	} else if len(c.Verification) > 0 {
		b.WriteString("\n### Verification steps to confirm\n")
		for _, v := range c.Verification {
			fmt.Fprintf(&b, "- [%s] %s\n", v.Kind, v.Description)
		}
	}

	return b.String(), ws.Path, nil
}

package api

import (
	"encoding/json"
	"fmt"
	"net/http"
	"strings"

	"github.com/sirupsen/logrus"

	"github.com/timmersuk/llm-workbench/internal/agentrunner"
	"github.com/timmersuk/llm-workbench/internal/chat"
	"github.com/timmersuk/llm-workbench/internal/project"
	"github.com/timmersuk/llm-workbench/internal/task"
)

// Tool names/schemas for the Draft mechanism (CONTEXT.md): GrillMe
// registers propose_context, Planning Mode registers propose_plan. Plain Go
// constants/vars, not Knowledge documents — these are prompt-engineering
// scaffolding, not durable domain content.
const (
	proposeContextToolName = "propose_context"
	proposePlanToolName    = "propose_plan"
)

var proposeContextToolSchema = json.RawMessage(`{
  "type": "object",
  "properties": {
    "objective": {"type": "string"},
    "constraints": {"type": "array", "items": {"type": "string"}},
    "assumptions": {"type": "array", "items": {"type": "string"}},
    "success_criteria": {"type": "array", "items": {"type": "string"}},
    "context": {
      "type": "object",
      "properties": {
        "summary": {"type": "string"},
        "background": {"type": "string"},
        "files": {"type": "array", "items": {"type": "string"}},
        "detail": {"type": "string"},
        "verification": {"type": "array", "items": {"type": "string"}},
        "open_questions": {"type": "array", "items": {"type": "string"}}
      },
      "required": ["summary"]
    }
  },
  "required": ["objective", "context"]
}`)

var proposePlanToolSchema = json.RawMessage(`{
  "type": "object",
  "properties": {
    "approach": {"type": "string"},
    "steps": {"type": "array", "items": {"type": "string"}},
    "risks": {"type": "array", "items": {"type": "string"}},
    "estimated_complexity": {"type": "string", "enum": ["low", "medium", "high"]},
    "recommended_executor": {"type": "string"}
  },
  "required": ["approach", "steps", "estimated_complexity"]
}`)

// stageTool returns the Draft-proposing tool registered for stage, and
// whether stage is a valid Conversation stage at all (requirements or
// planning — see task.ErrInvalidStage).
func stageTool(stage string) (chat.Tool, bool) {
	switch stage {
	case task.StageRequirements:
		return chat.Tool{Type: "function", Function: chat.ToolSchema{
			Name:        proposeContextToolName,
			Description: "Propose the task's requirements (objective, constraints, assumptions, success criteria) and narrative context for the human to review before Finalize.",
			Parameters:  proposeContextToolSchema,
		}}, true
	case task.StagePlanning:
		return chat.Tool{Type: "function", Function: chat.ToolSchema{
			Name:        proposePlanToolName,
			Description: "Propose a structured execution plan for the human to review before Finalize.",
			Parameters:  proposePlanToolSchema,
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

		var assistantContent strings.Builder
		var proposed *chat.ToolCall
		var streamErr error

		workspace, wsErr := agentrunner.ResolveWorkspace(reposRoot, proj.Repositories)
		if wsErr != nil {
			streamErr = fmt.Errorf("resolving workspace: %w", wsErr)
		} else {
			out, runErr := runner.Run(r.Context(), agentrunner.RunInput{
				SessionKey:   taskId + ":" + stage,
				Workspace:    workspace,
				SystemPrompt: systemPrompt,
				UserMessage:  req.Content,
				Model:        req.Model,
				Tool:         tool,
			}, func(d chat.Delta) error {
				writeEvent(chatStreamEvent{Content: d.Content, ReasoningContent: d.ReasoningContent})
				return nil
			})
			streamErr = runErr
			assistantContent.WriteString(out.Content)
			proposed = out.ToolCall
			if proposed != nil && proposed.Function.Name != tool.Function.Name {
				logrus.WithFields(logrus.Fields{
					"task": taskId, "stage": stage,
					"expected_tool": tool.Function.Name, "got_tool": proposed.Function.Name,
				}).Warn("ignoring tool call that doesn't match the stage's registered tool")
				proposed = nil
			}
			if proposed != nil {
				writeEvent(chatStreamEvent{ToolCall: &chatToolCallEvent{
					Name:      proposed.Function.Name,
					Arguments: proposed.Function.Arguments,
				}})
			}
		}
		if streamErr != nil {
			// Headers (200 OK) are already sent, so a failed stream can't
			// surface as an HTTP error status — relayed as a final SSE
			// event instead, matching handleChatCompletions (chat.go).
			writeEvent(chatStreamEvent{Error: streamErr.Error()})
		}

		assistantMsg := task.ConversationMessage{Role: "assistant", Content: assistantContent.String()}
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
		b.WriteString("You are GrillMe, interviewing the user to sharpen a task's requirements. ")
		b.WriteString("Ask focused questions until the objective, constraints, assumptions, and success criteria are coherent, then call propose_context with your proposal.\n\n")
	case task.StagePlanning:
		b.WriteString("You are Planning Mode, interviewing the user to produce a structured execution plan. ")
		b.WriteString("Once the approach is coherent, call propose_plan with your proposal.\n\n")
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

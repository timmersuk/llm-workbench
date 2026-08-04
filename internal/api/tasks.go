package api

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"

	"github.com/timmersuk/llm-workbench/internal/agentrunner"
	"github.com/timmersuk/llm-workbench/internal/task"
)

// resolveTaskStore confirms the project named by projectId exists, then
// returns the shared TaskStore singleton (s.Tasks) for the caller to
// operate against, always passing projectId explicitly on every call.
// Returns false (having already written a response) if the project doesn't
// exist or the id is otherwise invalid. A method rather than a free
// function taking ProjectStore/TaskStore as parameters — the same shape as
// stage_conversation.go's helpers, folded into the Server-methods
// refactor even though it wasn't one of the five originally named in
// docs/milestones/done/milestone8b.md's scan, since it re-passes the exact
// same invariant deps to a dozen call sites (docs/adr/0016).
func (s *Server) resolveTaskStore(w http.ResponseWriter, projectId string) (TaskStore, bool) {
	if _, err := s.Projects.Get(projectId); err != nil {
		writeGetError(w, err)
		return nil, false
	}
	return s.Tasks, true
}

func (s *Server) handleListProjectTasks() http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		projectId := r.PathValue("projectId")
		store, ok := s.resolveTaskStore(w, projectId)
		if !ok {
			return
		}

		result, err := store.List(projectId)
		if err != nil {
			writeAPIError(w, http.StatusInternalServerError, "internal error")
			return
		}
		writeJSON(w, http.StatusOK, result)
	}
}

func (s *Server) handleGetProjectTask() http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		projectId := r.PathValue("projectId")
		store, ok := s.resolveTaskStore(w, projectId)
		if !ok {
			return
		}

		t, err := store.Get(projectId, r.PathValue("taskId"))
		if err != nil {
			writeGetError(w, err)
			return
		}
		writeJSON(w, http.StatusOK, t)
	}
}

func (s *Server) handleCreateProjectTask() http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		projectId := r.PathValue("projectId")
		store, ok := s.resolveTaskStore(w, projectId)
		if !ok {
			return
		}

		var t task.Task
		if err := json.NewDecoder(r.Body).Decode(&t); err != nil {
			writeAPIError(w, http.StatusBadRequest, "invalid request body")
			return
		}
		t.Project = projectId
		if t.AgentDefaults == nil && len(s.AgentRunners) > 0 {
			defaults, err := s.newTaskAgentDefaults(r.Context())
			if err != nil {
				writeAPIError(w, http.StatusInternalServerError, err.Error())
				return
			}
			t.AgentDefaults = defaults
		}

		created, err := store.Create(projectId, t)
		if err != nil {
			writeMutationError(w, err)
			return
		}
		writeJSON(w, http.StatusCreated, created)
	}
}

func (s *Server) handleUpdateProjectTask() http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		projectId := r.PathValue("projectId")
		store, ok := s.resolveTaskStore(w, projectId)
		if !ok {
			return
		}

		var t task.Task
		if err := json.NewDecoder(r.Body).Decode(&t); err != nil {
			writeAPIError(w, http.StatusBadRequest, "invalid request body")
			return
		}
		t.Project = projectId
		current, err := store.Get(projectId, r.PathValue("taskId"))
		if err != nil {
			writeGetError(w, err)
			return
		}
		if t.AgentDefaults == nil {
			t.AgentDefaults = current.AgentDefaults
		}
		if !agentDefaultsEqual(current.AgentDefaults, t.AgentDefaults) {
			if err := s.validateAgentDefaults(r.Context(), t.AgentDefaults); err != nil {
				writeAPIError(w, http.StatusBadRequest, err.Error())
				return
			}
		}

		updated, err := store.Update(projectId, r.PathValue("taskId"), t)
		if err != nil {
			writeMutationError(w, err)
			return
		}
		writeJSON(w, http.StatusOK, updated)
	}
}

func (s *Server) newTaskAgentDefaults(ctx context.Context) (*task.AgentDefaults, error) {
	stageSeed := s.StageConversationSeedExecutor
	if stageSeed == "" {
		stageSeed = "local"
	}
	executionSeed := s.ExecutionSeedExecutor
	if executionSeed == "" {
		executionSeed = "claude-code"
	}
	stage, err := s.seedSelection(ctx, stageSeed)
	if err != nil {
		return nil, fmt.Errorf("initializing stage conversation default: %w", err)
	}
	execution, err := s.seedSelection(ctx, executionSeed)
	if err != nil {
		return nil, fmt.Errorf("initializing execution default: %w", err)
	}
	return &task.AgentDefaults{StageConversation: taskAgentSelection(stage), Execution: taskAgentSelection(execution)}, nil
}

func (s *Server) seedSelection(ctx context.Context, name string) (agentrunner.Selection, error) {
	runner, ok := s.AgentRunners[name]
	if !ok {
		return agentrunner.Selection{}, fmt.Errorf("seed executor %q is not configured", name)
	}
	capability, err := runner.Capabilities(ctx)
	if err != nil {
		return agentrunner.Selection{}, err
	}
	capability.Name = name
	selection := capability.DefaultSelection()
	return selection, agentrunner.ValidateSelection(selection, capability)
}

func (s *Server) validateAgentDefaults(ctx context.Context, defaults *task.AgentDefaults) error {
	if defaults == nil {
		return fmt.Errorf("%w: agent_defaults is required", agentrunner.ErrInvalidSelection)
	}
	for _, selection := range []task.AgentSelection{defaults.StageConversation, defaults.Execution} {
		sel := taskSelection(selection)
		runner, ok := s.AgentRunners[sel.Executor]
		if !ok {
			return fmt.Errorf("%w: unknown executor %q", agentrunner.ErrInvalidSelection, sel.Executor)
		}
		capability, err := runner.Capabilities(ctx)
		if err != nil {
			return err
		}
		capability.Name = sel.Executor
		if err := agentrunner.ValidateSelection(sel, capability); err != nil {
			return err
		}
	}
	return nil
}

func agentDefaultsEqual(a, b *task.AgentDefaults) bool {
	if a == nil || b == nil {
		return a == b
	}
	return a.StageConversation == b.StageConversation && a.Execution == b.Execution
}

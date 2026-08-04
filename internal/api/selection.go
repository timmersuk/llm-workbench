package api

import (
	"context"
	"fmt"

	"github.com/timmersuk/llm-workbench/internal/agentrunner"
	"github.com/timmersuk/llm-workbench/internal/task"
)

type selectionScope string

const (
	scopeStage     selectionScope = "stage_conversation"
	scopeExecution selectionScope = "execution"
)

func taskSelection(v task.AgentSelection) agentrunner.Selection {
	return agentrunner.Selection{Executor: v.Executor, Model: v.Model, Effort: agentrunner.ReasoningEffort(v.Effort)}
}

func persistedSelection(t task.Task, scope selectionScope) (agentrunner.Selection, error) {
	if t.AgentDefaults == nil {
		return agentrunner.Selection{}, fmt.Errorf("%w: task has no agent_defaults", agentrunner.ErrInvalidSelection)
	}
	if scope == scopeExecution {
		return taskSelection(t.AgentDefaults.Execution), nil
	}
	return taskSelection(t.AgentDefaults.StageConversation), nil
}

func (s *Server) resolveSelection(ctx context.Context, t task.Task, scope selectionScope, submitted agentrunner.Selection) (agentrunner.Selection, agentrunner.AgentRunner, error) {
	any := submitted.Executor != "" || submitted.Model != "" || submitted.Effort != ""
	all := submitted.Executor != "" && submitted.Effort != ""
	if any && !all {
		return agentrunner.Selection{}, nil, fmt.Errorf("%w: override must include executor, model, and effort", agentrunner.ErrInvalidSelection)
	}
	sel := submitted
	if !any {
		var err error
		sel, err = persistedSelection(t, scope)
		if err != nil {
			return agentrunner.Selection{}, nil, err
		}
	}
	runner, ok := s.AgentRunners[sel.Executor]
	if !ok {
		return agentrunner.Selection{}, nil, fmt.Errorf("%w: unknown executor %q", agentrunner.ErrInvalidSelection, sel.Executor)
	}
	capability, err := runner.Capabilities(ctx)
	if err != nil {
		return agentrunner.Selection{}, nil, fmt.Errorf("loading capabilities for %q: %w", sel.Executor, err)
	}
	capability.Name = sel.Executor
	if err := agentrunner.ValidateSelection(sel, capability); err != nil {
		return agentrunner.Selection{}, nil, err
	}
	return sel, runner, nil
}

func taskAgentSelection(sel agentrunner.Selection) task.AgentSelection {
	return task.AgentSelection{Executor: sel.Executor, Model: sel.Model, Effort: string(sel.Effort)}
}

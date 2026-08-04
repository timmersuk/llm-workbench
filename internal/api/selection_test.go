package api

import (
	"context"
	"testing"

	"github.com/stretchr/testify/require"
	"github.com/timmersuk/llm-workbench/internal/agentrunner"
	"github.com/timmersuk/llm-workbench/internal/task"
)

func TestResolveSelection_OverridePrecedesIndependentDefaults(t *testing.T) {
	runner := new(mockAgentRunner)
	s := &Server{AgentRunners: map[string]agentrunner.AgentRunner{"local": runner}}
	taskValue := task.Task{AgentDefaults: &task.AgentDefaults{
		StageConversation: task.AgentSelection{Executor: "stale", Model: "old", Effort: "high"},
		Execution:         task.AgentSelection{Executor: "local", Effort: "medium"},
	}}
	override := agentrunner.Selection{Executor: "local", Effort: agentrunner.EffortLow}
	got, _, err := s.resolveSelection(context.Background(), taskValue, scopeStage, override)
	require.NoError(t, err)
	require.Equal(t, override, got)
	got, _, err = s.resolveSelection(context.Background(), taskValue, scopeExecution, agentrunner.Selection{})
	require.NoError(t, err)
	require.Equal(t, agentrunner.EffortMedium, got.Effort)
}

func TestResolveSelection_StaleDefaultBlockedButOverrideEscapes(t *testing.T) {
	runner := new(mockAgentRunner)
	s := &Server{AgentRunners: map[string]agentrunner.AgentRunner{"local": runner}}
	taskValue := task.Task{AgentDefaults: &task.AgentDefaults{StageConversation: task.AgentSelection{Executor: "removed", Effort: "high"}}}
	_, _, err := s.resolveSelection(context.Background(), taskValue, scopeStage, agentrunner.Selection{})
	require.ErrorIs(t, err, agentrunner.ErrInvalidSelection)
	_, _, err = s.resolveSelection(context.Background(), taskValue, scopeStage, agentrunner.Selection{Executor: "local", Effort: agentrunner.EffortHigh})
	require.NoError(t, err)
}

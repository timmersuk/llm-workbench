package agentrunner

import (
	"errors"
	"testing"

	"github.com/stretchr/testify/require"
)

func TestValidateSelection(t *testing.T) {
	capability := ExecutorCapabilities{Name: "local", Models: []string{"m"}, Efforts: []ReasoningEffort{EffortLow, EffortHigh}}
	require.NoError(t, ValidateSelection(Selection{Executor: "local", Model: "m", Effort: EffortHigh}, capability))
	for _, invalid := range []Selection{
		{Executor: "other", Model: "m", Effort: EffortHigh},
		{Executor: "local", Model: "", Effort: EffortHigh},
		{Executor: "local", Model: "bad", Effort: EffortHigh},
		{Executor: "local", Model: "m", Effort: EffortMedium},
	} {
		require.ErrorIs(t, ValidateSelection(invalid, capability), ErrInvalidSelection)
	}
}

func TestValidateSelection_ModelLessExecutorRequiresEmptyModel(t *testing.T) {
	capability := ExecutorCapabilities{Name: "human", Efforts: []ReasoningEffort{EffortLow}}
	require.NoError(t, ValidateSelection(Selection{Executor: "human", Effort: EffortLow}, capability))
	require.True(t, errors.Is(ValidateSelection(Selection{Executor: "human", Model: "m", Effort: EffortLow}, capability), ErrInvalidSelection))
}

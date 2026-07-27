package task

import (
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestFileStore_FinalizeRequirements_AdvancesStageAndPersistsBoth(t *testing.T) {
	root := t.TempDir()
	store := NewFileStore(root)
	_, err := store.Create("demo-project", Task{ID: "task-a", Title: "A"})
	require.NoError(t, err)

	draft := RequirementsDraft{
		Objective:       "ship login",
		Constraints:     []string{"no new deps"},
		Assumptions:     []string{"auth service exists"},
		SuccessCriteria: []string{"tests pass"},
		Context: Context{
			Summary: "adds a login page",
		},
	}

	updated, err := store.FinalizeRequirements("demo-project", "task-a", draft)
	require.NoError(t, err)
	assert.Equal(t, StagePlanning, updated.Stage)
	assert.Equal(t, "ship login", updated.Objective)
	assert.Equal(t, []string{"no new deps"}, updated.Constraints)

	fetched, err := store.Get("demo-project", "task-a")
	require.NoError(t, err)
	assert.Equal(t, StagePlanning, fetched.Stage)

	ctx, err := store.GetContext("demo-project", "task-a")
	require.NoError(t, err)
	assert.Equal(t, "adds a login page", ctx.Summary)
}

func TestFileStore_FinalizeRequirements_TrimsFieldsToAvoidYAMLRoundTripBug(t *testing.T) {
	root := t.TempDir()
	store := NewFileStore(root)
	_, err := store.Create("demo-project", Task{ID: "task-a", Title: "A"})
	require.NoError(t, err)

	draft := RequirementsDraft{
		Objective:   "\nship login",
		Constraints: []string{"\nno new deps"},
		Context: Context{
			Summary: "\nadds a login page",
			Detail:  "\nsome detail",
			Verification: []VerificationStep{
				{Description: "\nrun the tests", Kind: VerificationKindAgentExecutable},
				{Description: "   ", Kind: VerificationKindHumanJudgment}, // dropped: empty after trim
			},
		},
	}
	updated, err := store.FinalizeRequirements("demo-project", "task-a", draft)
	require.NoError(t, err)
	assert.Equal(t, "ship login", updated.Objective)
	assert.Equal(t, []string{"no new deps"}, updated.Constraints)

	// The persisted task.yaml/context.yaml must themselves be readable back
	// (gopkg.in/yaml.v3 fails to round-trip a string starting with a
	// newline) — re-fetching from disk is the real assertion here.
	fetched, err := store.Get("demo-project", "task-a")
	require.NoError(t, err)
	assert.Equal(t, "ship login", fetched.Objective)

	ctx, err := store.GetContext("demo-project", "task-a")
	require.NoError(t, err)
	assert.Equal(t, "adds a login page", ctx.Summary)
	// The empty-Description step is dropped; the surviving one is trimmed and
	// keeps its Kind, and round-trips back off disk intact.
	assert.Equal(t, []VerificationStep{{Description: "run the tests", Kind: VerificationKindAgentExecutable}}, ctx.Verification)
}

func TestFileStore_FinalizeRequirements_WrongStage(t *testing.T) {
	root := t.TempDir()
	store := NewFileStore(root)
	_, err := store.Create("demo-project", Task{ID: "task-a", Title: "A"})
	require.NoError(t, err)
	_, err = store.FinalizeRequirements("demo-project", "task-a", RequirementsDraft{})
	require.NoError(t, err) // requirements -> planning, valid once

	_, err = store.FinalizeRequirements("demo-project", "task-a", RequirementsDraft{})
	require.Error(t, err)
	assert.ErrorIs(t, err, ErrWrongStage)
}

func TestFileStore_FinalizePlan_AdvancesStageAndPersistsBoth(t *testing.T) {
	root := t.TempDir()
	store := NewFileStore(root)
	_, err := store.Create("demo-project", Task{ID: "task-a", Title: "A"})
	require.NoError(t, err)
	_, err = store.FinalizeRequirements("demo-project", "task-a", RequirementsDraft{})
	require.NoError(t, err)

	plan := Plan{Approach: "incremental", EstimatedComplexity: "low"}
	updated, err := store.FinalizePlan("demo-project", "task-a", plan)
	require.NoError(t, err)
	assert.Equal(t, StageImplementation, updated.Stage)

	got, err := store.GetPlan("demo-project", "task-a")
	require.NoError(t, err)
	assert.Equal(t, "incremental", got.Approach)
}

func TestFileStore_FinalizePlan_WrongStage(t *testing.T) {
	root := t.TempDir()
	store := NewFileStore(root)
	_, err := store.Create("demo-project", Task{ID: "task-a", Title: "A"}) // still in requirements
	require.NoError(t, err)

	_, err = store.FinalizePlan("demo-project", "task-a", Plan{})
	require.Error(t, err)
	assert.ErrorIs(t, err, ErrWrongStage)
}

func TestFileStore_ReviseToRequirements(t *testing.T) {
	root := t.TempDir()
	store := NewFileStore(root)
	_, err := store.Create("demo-project", Task{ID: "task-a", Title: "A"})
	require.NoError(t, err)
	_, err = store.FinalizeRequirements("demo-project", "task-a", RequirementsDraft{})
	require.NoError(t, err)

	updated, err := store.ReviseToRequirements("demo-project", "task-a")
	require.NoError(t, err)
	assert.Equal(t, StageRequirements, updated.Stage)
}

func TestFileStore_ReviseToRequirements_WrongStage(t *testing.T) {
	root := t.TempDir()
	store := NewFileStore(root)
	_, err := store.Create("demo-project", Task{ID: "task-a", Title: "A"}) // still requirements
	require.NoError(t, err)

	_, err = store.ReviseToRequirements("demo-project", "task-a")
	require.Error(t, err)
	assert.ErrorIs(t, err, ErrWrongStage)
}

func TestFileStore_ReviseToPlanning_FromImplementationOrReview(t *testing.T) {
	root := t.TempDir()
	store := NewFileStore(root)
	_, err := store.Create("demo-project", Task{ID: "task-a", Title: "A"})
	require.NoError(t, err)
	_, err = store.FinalizeRequirements("demo-project", "task-a", RequirementsDraft{})
	require.NoError(t, err)
	_, err = store.FinalizePlan("demo-project", "task-a", Plan{})
	require.NoError(t, err)

	updated, err := store.ReviseToPlanning("demo-project", "task-a")
	require.NoError(t, err)
	assert.Equal(t, StagePlanning, updated.Stage)

	// Also valid from "review".
	reviewTask := updated
	reviewTask.Stage = StageReview
	updated, err = store.Update("demo-project", "task-a", reviewTask)
	require.NoError(t, err)
	updated, err = store.ReviseToPlanning("demo-project", "task-a")
	require.NoError(t, err)
	assert.Equal(t, StagePlanning, updated.Stage)
}

func TestFileStore_ReviseToPlanning_WrongStage(t *testing.T) {
	root := t.TempDir()
	store := NewFileStore(root)
	_, err := store.Create("demo-project", Task{ID: "task-a", Title: "A"}) // requirements
	require.NoError(t, err)

	_, err = store.ReviseToPlanning("demo-project", "task-a")
	require.Error(t, err)
	assert.ErrorIs(t, err, ErrWrongStage)
}

func TestFileStore_ReviseThenFinalize_ConversationHistoryPreserved(t *testing.T) {
	root := t.TempDir()
	store := NewFileStore(root)
	_, err := store.Create("demo-project", Task{ID: "task-a", Title: "A"})
	require.NoError(t, err)

	_, err = store.AppendConversationMessages("demo-project", "task-a", StagePlanning,
		ConversationMessage{Role: "user", Content: "first planning pass"})
	require.NoError(t, err)

	_, err = store.FinalizeRequirements("demo-project", "task-a", RequirementsDraft{})
	require.NoError(t, err)
	_, err = store.FinalizePlan("demo-project", "task-a", Plan{})
	require.NoError(t, err)
	_, err = store.ReviseToPlanning("demo-project", "task-a")
	require.NoError(t, err)

	_, err = store.AppendConversationMessages("demo-project", "task-a", StagePlanning,
		ConversationMessage{Role: "user", Content: "second planning pass"})
	require.NoError(t, err)

	conv, err := store.GetConversation("demo-project", "task-a", StagePlanning)
	require.NoError(t, err)
	require.Len(t, conv.Messages, 2)
	assert.Equal(t, "first planning pass", conv.Messages[0].Content)
	assert.Equal(t, "second planning pass", conv.Messages[1].Content)
}

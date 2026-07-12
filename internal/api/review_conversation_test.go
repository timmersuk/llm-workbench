package api

import (
	"context"
	"os"
	"os/exec"
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/timmersuk/llm-workbench/internal/agentrunner"
	"github.com/timmersuk/llm-workbench/internal/drafttool"
	"github.com/timmersuk/llm-workbench/internal/project"
	"github.com/timmersuk/llm-workbench/internal/task"
)

func TestStageTool_ReviewReturnsProposeReview(t *testing.T) {
	tool, ok := stageTool(task.StageReview)
	require.True(t, ok)
	assert.Equal(t, drafttool.ProposeReviewName, tool.Function.Name)
	assert.NotEmpty(t, tool.Function.Parameters)
}

func TestBuildStagePrompt_ReviewUsesReviewSystemPrompt(t *testing.T) {
	prompt := buildStagePrompt(
		task.Task{ID: "task-a", Objective: "ship it"},
		project.Project{Name: "demo"},
		task.StageReview,
		new(mockKnowledgeReader),
	)
	// The review discipline (three phases, confined bash) leads the prompt.
	assert.Contains(t, prompt, "reviewing a completed execution")
	assert.Contains(t, prompt, "propose_review")
	assert.Contains(t, prompt, "ship it")
}

// gitRun runs a git subprocess for test setup (package api can't reach
// agentrunner's unexported runGit).
func gitRun(t *testing.T, dir string, args ...string) {
	t.Helper()
	cmd := exec.Command("git", args...)
	cmd.Dir = dir
	out, err := cmd.CombinedOutput()
	require.NoErrorf(t, err, "git %v: %s", args, out)
}

// initReviewRepo creates a real repo with an execution worktree (exec-001)
// carrying one extra commit, so buildReviewContext has a real diff to collect.
func initReviewRepo(t *testing.T, reposRoot string) agentrunner.ExecutionWorkspace {
	t.Helper()
	dir := filepath.Join(reposRoot, "myrepo")
	require.NoError(t, os.MkdirAll(dir, 0o755))
	gitRun(t, dir, "init", "-q")
	gitRun(t, dir, "config", "user.email", "t@example.com")
	gitRun(t, dir, "config", "user.name", "T")
	require.NoError(t, os.WriteFile(filepath.Join(dir, "README.md"), []byte("hi\n"), 0o644))
	gitRun(t, dir, "add", ".")
	gitRun(t, dir, "commit", "-q", "-m", "init")

	ws, err := agentrunner.ResolveExecutionWorkspace(context.Background(), reposRoot, []string{"github.com/x/myrepo"}, "task-a", "exec-001")
	require.NoError(t, err)
	require.NoError(t, os.WriteFile(filepath.Join(ws.Path, "feature.go"), []byte("package main\n"), 0o644))
	gitRun(t, ws.Path, "add", ".")
	gitRun(t, ws.Path, "commit", "-q", "-m", "add feature")
	return ws
}

// seedReviewableTask drives a task through the public store API to stage
// review with a recorded exec-001 and a context.yaml carrying verification
// steps — the state a real review conversation starts from.
func seedReviewableTask(t *testing.T, store *task.FileStore, id string) {
	t.Helper()
	_, err := store.Create(task.Task{ID: id, Title: "A"})
	require.NoError(t, err)
	_, err = store.FinalizeRequirements(id, task.RequirementsDraft{
		Objective: "ship it",
		Context: task.Context{
			Summary: "does the thing",
			Verification: []task.VerificationStep{
				{Description: "run go test", Kind: task.VerificationKindAgentExecutable},
				{Description: "copy reads well", Kind: task.VerificationKindHumanJudgment},
			},
		},
	}) // requirements -> planning, writes context.yaml
	require.NoError(t, err)
	_, err = store.FinalizePlan(id, task.Plan{Approach: "do it", EstimatedComplexity: "low"}) // planning -> implementation
	require.NoError(t, err)
	_, err = store.RecordExecution(id, task.Execution{ExecutionID: "exec-001", Status: task.ExecutionStatusSuccess}) // implementation -> review
	require.NoError(t, err)
}

func TestBuildReviewContext_IncludesDiffAndVerificationSteps(t *testing.T) {
	reposRoot := t.TempDir()
	ws := initReviewRepo(t, reposRoot)

	store := task.NewFileStore(t.TempDir())
	seedReviewableTask(t, store, "task-a")

	addendum, workspace, err := buildReviewContext(
		context.Background(), reposRoot,
		project.Project{Repositories: []string{"github.com/x/myrepo"}},
		store, "task-a",
	)
	require.NoError(t, err)
	assert.Equal(t, ws.Path, workspace)
	assert.Contains(t, addendum, "exec-001")
	assert.Contains(t, addendum, "```diff")
	assert.Contains(t, addendum, "feature.go") // the real diff is inlined
	assert.Contains(t, addendum, "[agent_executable] run go test")
	assert.Contains(t, addendum, "[human_judgment] copy reads well")
}

func TestBuildReviewContext_NoExecutionIsAnError(t *testing.T) {
	reposRoot := t.TempDir()
	initReviewRepo(t, reposRoot)

	store := task.NewFileStore(t.TempDir())
	_, err := store.Create(task.Task{ID: "task-b", Title: "B"})
	require.NoError(t, err)

	_, _, err = buildReviewContext(
		context.Background(), reposRoot,
		project.Project{Repositories: []string{"github.com/x/myrepo"}},
		store, "task-b",
	)
	require.Error(t, err)
	assert.Contains(t, err.Error(), "no execution to review")
}

package main

import (
	"context"
	"fmt"
	"os"

	"github.com/timmersuk/llm-workbench/internal/agentrunner"
	"github.com/timmersuk/llm-workbench/internal/chat"
	"github.com/timmersuk/llm-workbench/internal/gitstore"
	"github.com/timmersuk/llm-workbench/internal/serverapp"
	"github.com/timmersuk/llm-workbench/internal/task"
)

func main() {
	if err := run(context.Background()); err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
}

func run(ctx context.Context) error {
	cfg := serverapp.LoadConfig()
	runners := map[string]agentrunner.AgentRunner{
		"local":       agentrunner.NewChatClientRunner(chat.NewOpenAIClient(cfg.LLMBaseURL, cfg.LLMAPIKey, cfg.LLMTimeout), nil, agentrunner.Selection{Executor: "local", Effort: cfg.LLMDefaultEffort}),
		"claude-code": agentrunner.NewClaudeRunner(cfg.AgentTimeout, cfg.AgentExecutionTimeout, cfg.ReposRoot, nil),
		"codex":       agentrunner.NewCodexRunner(cfg.AgentTimeout, cfg.AgentExecutionTimeout, cfg.ReposRoot, nil),
	}
	seed := func(name string) (task.AgentSelection, error) {
		runner, ok := runners[name]
		if !ok {
			return task.AgentSelection{}, fmt.Errorf("seed executor %q is not configured", name)
		}
		capability, err := runner.Capabilities(ctx)
		if err != nil {
			return task.AgentSelection{}, err
		}
		capability.Name = name
		sel := capability.DefaultSelection()
		if err := agentrunner.ValidateSelection(sel, capability); err != nil {
			return task.AgentSelection{}, err
		}
		return task.AgentSelection{Executor: sel.Executor, Model: sel.Model, Effort: string(sel.Effort)}, nil
	}
	stage, err := seed(cfg.StageConversationSeedExecutor)
	if err != nil {
		return fmt.Errorf("stage seed: %w", err)
	}
	execution, err := seed(cfg.ExecutionSeedExecutor)
	if err != nil {
		return fmt.Errorf("execution seed: %w", err)
	}
	store, err := gitstore.Open(cfg.WorkspaceRoot, cfg.DataRepoURL)
	if err != nil {
		return err
	}
	projects, err := store.Projects.List()
	if err != nil {
		return err
	}
	failed := 0
	for _, project := range projects.Projects {
		result, listErr := store.Tasks.List(project.ID)
		if listErr != nil {
			fmt.Printf("failed %s: %v\n", project.ID, listErr)
			failed++
			continue
		}
		for _, t := range result.Tasks {
			if t.AgentDefaults != nil {
				fmt.Printf("skipped %s/%s (already initialized)\n", project.ID, t.ID)
				continue
			}
			t.AgentDefaults = &task.AgentDefaults{StageConversation: stage, Execution: execution}
			if _, err := store.Tasks.Update(project.ID, t.ID, t); err != nil {
				fmt.Printf("failed %s/%s: %v\n", project.ID, t.ID, err)
				failed++
			} else {
				fmt.Printf("migrated %s/%s\n", project.ID, t.ID)
			}
		}
		for _, loadErr := range result.Errors {
			fmt.Printf("failed %s/%s: %s\n", project.ID, loadErr.ID, loadErr.Error)
			failed++
		}
	}
	if failed > 0 {
		return fmt.Errorf("migration completed with %d failure(s)", failed)
	}
	return nil
}

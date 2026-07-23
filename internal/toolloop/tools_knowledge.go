package toolloop

import (
	"context"
	"encoding/json"
	"fmt"

	"github.com/timmersuk/llm-workbench/internal/chat"
	"github.com/timmersuk/llm-workbench/internal/knowledgetool"
)

// KnowledgeTools returns the always-available, read-only knowledge query
// toolset (docs/milestones/done/milestone9.md's "discovery mechanism") for
// ChatClientRunner — list_knowledge_concepts and get_knowledge_concept,
// backed by store. Unlike ReadOnlyTools, these ignore the workspace
// entirely (data/knowledge/ is a workspace-wide bundle, not part of any
// project's checked-out repository), so callers should offer them
// regardless of whether a usable workspace was resolved for this turn.
// Returns nil if store is nil, so a caller with no configured
// KnowledgeStore doesn't advertise tools it can't actually serve.
func KnowledgeTools(store knowledgetool.Store) []Tool {
	if store == nil {
		return nil
	}
	return []Tool{knowledgeListTool{store: store}, knowledgeGetTool{store: store}}
}

type knowledgeListTool struct{ store knowledgetool.Store }

func (t knowledgeListTool) Spec() chat.Tool {
	return chat.Tool{Type: "function", Function: chat.ToolSchema{
		Name:        knowledgetool.List.Name,
		Description: knowledgetool.List.Description,
		Parameters:  knowledgetool.List.Schema,
	}}
}

func (t knowledgeListTool) Execute(_ context.Context, _ string, _ string) (string, error) {
	return knowledgetool.ExecuteList(t.store)
}

type knowledgeGetTool struct{ store knowledgetool.Store }

func (t knowledgeGetTool) Spec() chat.Tool {
	return chat.Tool{Type: "function", Function: chat.ToolSchema{
		Name:        knowledgetool.Get.Name,
		Description: knowledgetool.Get.Description,
		Parameters:  knowledgetool.Get.Schema,
	}}
}

func (t knowledgeGetTool) Execute(_ context.Context, _ string, argumentsJSON string) (string, error) {
	var args struct {
		ConceptID string `json:"concept_id"`
	}
	if err := json.Unmarshal([]byte(argumentsJSON), &args); err != nil {
		return "", fmt.Errorf("invalid arguments: %w", err)
	}
	return knowledgetool.ExecuteGet(t.store, args.ConceptID)
}

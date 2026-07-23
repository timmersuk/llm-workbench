package toolloop

import (
	"context"
	"errors"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/timmersuk/llm-workbench/internal/knowledge"
)

type fakeKnowledgeStore struct {
	summaries []knowledge.ConceptSummary
	concepts  map[string]knowledge.Concept
}

func (f *fakeKnowledgeStore) List() ([]knowledge.ConceptSummary, error) {
	return f.summaries, nil
}

func (f *fakeKnowledgeStore) Get(conceptID string) (knowledge.Concept, error) {
	c, ok := f.concepts[conceptID]
	if !ok {
		return knowledge.Concept{}, errors.New("not found")
	}
	return c, nil
}

func TestKnowledgeTools_NilStoreReturnsNil(t *testing.T) {
	assert.Nil(t, KnowledgeTools(nil))
}

func TestKnowledgeTools_ReturnsBothTools(t *testing.T) {
	tools := KnowledgeTools(&fakeKnowledgeStore{})
	require.Len(t, tools, 2)
	assert.Equal(t, "list_knowledge_concepts", tools[0].Spec().Function.Name)
	assert.Equal(t, "get_knowledge_concept", tools[1].Spec().Function.Name)
}

func TestKnowledgeListTool_IgnoresWorkspace(t *testing.T) {
	store := &fakeKnowledgeStore{summaries: []knowledge.ConceptSummary{{ConceptID: "a", Type: "Reference"}}}
	tool := knowledgeListTool{store: store}

	out, err := tool.Execute(context.Background(), "", `{}`)
	require.NoError(t, err)
	assert.Contains(t, out, "a [Reference]")
}

func TestKnowledgeGetTool_FetchesByConceptID(t *testing.T) {
	store := &fakeKnowledgeStore{concepts: map[string]knowledge.Concept{
		"a": {Type: "Reference", Body: "hello\n"},
	}}
	tool := knowledgeGetTool{store: store}

	out, err := tool.Execute(context.Background(), "/nonexistent/workspace", `{"concept_id":"a"}`)
	require.NoError(t, err)
	assert.Contains(t, out, "concept_id: a")
	assert.Contains(t, out, "hello")
}

func TestKnowledgeGetTool_InvalidArgumentsJSON(t *testing.T) {
	tool := knowledgeGetTool{store: &fakeKnowledgeStore{}}
	_, err := tool.Execute(context.Background(), "", `not json`)
	require.Error(t, err)
}

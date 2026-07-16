package api

import (
	"context"

	"github.com/stretchr/testify/mock"

	"github.com/timmersuk/llm-workbench/internal/agentrunner"
	"github.com/timmersuk/llm-workbench/internal/chat"
	"github.com/timmersuk/llm-workbench/internal/knowledge"
	"github.com/timmersuk/llm-workbench/internal/project"
	"github.com/timmersuk/llm-workbench/internal/task"
)

type mockTaskStore struct{ mock.Mock }

func (m *mockTaskStore) List() (task.ListResult, error) {
	args := m.Called()
	var result task.ListResult
	if v := args.Get(0); v != nil {
		result = v.(task.ListResult)
	}
	return result, args.Error(1)
}

func (m *mockTaskStore) Get(id string) (task.Task, error) {
	args := m.Called(id)
	var t task.Task
	if v := args.Get(0); v != nil {
		t = v.(task.Task)
	}
	return t, args.Error(1)
}

func (m *mockTaskStore) Create(t task.Task) (task.Task, error) {
	args := m.Called(t)
	var created task.Task
	if v := args.Get(0); v != nil {
		created = v.(task.Task)
	}
	return created, args.Error(1)
}

func (m *mockTaskStore) Update(id string, t task.Task) (task.Task, error) {
	args := m.Called(id, t)
	var updated task.Task
	if v := args.Get(0); v != nil {
		updated = v.(task.Task)
	}
	return updated, args.Error(1)
}

func (m *mockTaskStore) GetContext(id string) (task.Context, error) {
	args := m.Called(id)
	var c task.Context
	if v := args.Get(0); v != nil {
		c = v.(task.Context)
	}
	return c, args.Error(1)
}

func (m *mockTaskStore) GetPlan(id string) (task.Plan, error) {
	args := m.Called(id)
	var p task.Plan
	if v := args.Get(0); v != nil {
		p = v.(task.Plan)
	}
	return p, args.Error(1)
}

func (m *mockTaskStore) GetConversation(id, stage string) (task.Conversation, error) {
	args := m.Called(id, stage)
	var c task.Conversation
	if v := args.Get(0); v != nil {
		c = v.(task.Conversation)
	}
	return c, args.Error(1)
}

func (m *mockTaskStore) AppendConversationMessages(id, stage string, msgs ...task.ConversationMessage) (task.Conversation, error) {
	args := m.Called(id, stage, msgs)
	var c task.Conversation
	if v := args.Get(0); v != nil {
		c = v.(task.Conversation)
	}
	return c, args.Error(1)
}

func (m *mockTaskStore) ReplaceConversationMessages(id, stage string, msgs []task.ConversationMessage) (task.Conversation, error) {
	args := m.Called(id, stage, msgs)
	var c task.Conversation
	if v := args.Get(0); v != nil {
		c = v.(task.Conversation)
	}
	return c, args.Error(1)
}

func (m *mockTaskStore) FinalizeRequirements(id string, draft task.RequirementsDraft) (task.Task, error) {
	args := m.Called(id, draft)
	var t task.Task
	if v := args.Get(0); v != nil {
		t = v.(task.Task)
	}
	return t, args.Error(1)
}

func (m *mockTaskStore) FinalizePlan(id string, plan task.Plan) (task.Task, error) {
	args := m.Called(id, plan)
	var t task.Task
	if v := args.Get(0); v != nil {
		t = v.(task.Task)
	}
	return t, args.Error(1)
}

func (m *mockTaskStore) ReviseToRequirements(id string) (task.Task, error) {
	args := m.Called(id)
	var t task.Task
	if v := args.Get(0); v != nil {
		t = v.(task.Task)
	}
	return t, args.Error(1)
}

func (m *mockTaskStore) ReviseToPlanning(id string) (task.Task, error) {
	args := m.Called(id)
	var t task.Task
	if v := args.Get(0); v != nil {
		t = v.(task.Task)
	}
	return t, args.Error(1)
}

func (m *mockTaskStore) NextExecutionID(id string) (string, error) {
	args := m.Called(id)
	return args.String(0), args.Error(1)
}

func (m *mockTaskStore) RecordExecution(id string, exec task.Execution) (task.Execution, error) {
	args := m.Called(id, exec)
	var recorded task.Execution
	if v := args.Get(0); v != nil {
		recorded = v.(task.Execution)
	}
	return recorded, args.Error(1)
}

func (m *mockTaskStore) ListExecutions(id string) ([]task.Execution, error) {
	args := m.Called(id)
	var executions []task.Execution
	if v := args.Get(0); v != nil {
		executions = v.([]task.Execution)
	}
	return executions, args.Error(1)
}

func (m *mockTaskStore) FinalizeReview(id string, draft task.ReviewDraft) (task.Task, error) {
	args := m.Called(id, draft)
	var t task.Task
	if v := args.Get(0); v != nil {
		t = v.(task.Task)
	}
	return t, args.Error(1)
}

func (m *mockTaskStore) ListReviews(id string) ([]task.Review, error) {
	args := m.Called(id)
	var reviews []task.Review
	if v := args.Get(0); v != nil {
		reviews = v.([]task.Review)
	}
	return reviews, args.Error(1)
}

func (m *mockTaskStore) MarkPRMerged(id string) (task.Task, error) {
	args := m.Called(id)
	var t task.Task
	if v := args.Get(0); v != nil {
		t = v.(task.Task)
	}
	return t, args.Error(1)
}

func (m *mockTaskStore) RecordPullRequest(id string, pr task.PullRequest) (task.Task, error) {
	args := m.Called(id, pr)
	var t task.Task
	if v := args.Get(0); v != nil {
		t = v.(task.Task)
	}
	return t, args.Error(1)
}

type mockKnowledgeReader struct{ mock.Mock }

func (m *mockKnowledgeReader) Get(conceptID string) (knowledge.Concept, error) {
	args := m.Called(conceptID)
	var c knowledge.Concept
	if v := args.Get(0); v != nil {
		c = v.(knowledge.Concept)
	}
	return c, args.Error(1)
}

// fixedTaskStoreFactory adapts an already-constructed TaskStore (typically
// a mock) into a TaskStoreFactory that ignores the resolved root and always
// returns the same store — used by tests that don't care about the actual
// per-project root path.
func fixedTaskStoreFactory(store TaskStore) TaskStoreFactory {
	return func(root string) TaskStore { return store }
}

type mockProjectStore struct{ mock.Mock }

func (m *mockProjectStore) List() (project.ListResult, error) {
	args := m.Called()
	var result project.ListResult
	if v := args.Get(0); v != nil {
		result = v.(project.ListResult)
	}
	return result, args.Error(1)
}

func (m *mockProjectStore) Get(id string) (project.Project, error) {
	args := m.Called(id)
	var p project.Project
	if v := args.Get(0); v != nil {
		p = v.(project.Project)
	}
	return p, args.Error(1)
}

func (m *mockProjectStore) Create(in project.CreateInput) (project.Project, error) {
	args := m.Called(in)
	var p project.Project
	if v := args.Get(0); v != nil {
		p = v.(project.Project)
	}
	return p, args.Error(1)
}

func (m *mockProjectStore) Update(id string, in project.UpdateInput) (project.Project, error) {
	args := m.Called(id, in)
	var p project.Project
	if v := args.Get(0); v != nil {
		p = v.(project.Project)
	}
	return p, args.Error(1)
}

func (m *mockProjectStore) TasksRoot(id string) (string, error) {
	args := m.Called(id)
	return args.String(0), args.Error(1)
}

type mockAgentRunner struct{ mock.Mock }

// Run's test double is configured with .Return(deltas, out, err): deltas
// ([]chat.Delta) are fed through onDelta in order before out/err are
// returned.
func (m *mockAgentRunner) Run(ctx context.Context, in agentrunner.RunInput, onDelta func(chat.Delta) error) (agentrunner.RunOutput, error) {
	args := m.Called(ctx, in, onDelta)
	if deltas, ok := args.Get(0).([]chat.Delta); ok {
		for _, d := range deltas {
			if err := onDelta(d); err != nil {
				return agentrunner.RunOutput{}, err
			}
		}
	}
	var out agentrunner.RunOutput
	if v := args.Get(1); v != nil {
		out = v.(agentrunner.RunOutput)
	}
	return out, args.Error(2)
}

// Execute's test double is configured with .Return(events, out, err):
// events ([]agentrunner.ExecuteEvent) are fed through onEvent in order
// before out/err are returned — same shape as Run's mock above.
func (m *mockAgentRunner) Execute(ctx context.Context, in agentrunner.ExecuteInput, onEvent func(agentrunner.ExecuteEvent) error) (agentrunner.ExecuteOutput, error) {
	args := m.Called(ctx, in, onEvent)
	if events, ok := args.Get(0).([]agentrunner.ExecuteEvent); ok {
		for _, e := range events {
			if err := onEvent(e); err != nil {
				return agentrunner.ExecuteOutput{}, err
			}
		}
	}
	var out agentrunner.ExecuteOutput
	if v := args.Get(1); v != nil {
		out = v.(agentrunner.ExecuteOutput)
	}
	return out, args.Error(2)
}

func (m *mockAgentRunner) CheckHealth(ctx context.Context) error {
	args := m.Called(ctx)
	return args.Error(0)
}

func (m *mockAgentRunner) ListModels(ctx context.Context) ([]string, error) {
	args := m.Called(ctx)
	var models []string
	if v := args.Get(0); v != nil {
		models = v.([]string)
	}
	return models, args.Error(1)
}

func (m *mockAgentRunner) CloseSession(sessionKey string) {
	m.Called(sessionKey)
}

package api

import (
	"context"

	"github.com/stretchr/testify/mock"

	"github.com/timmersuk/llm-workbench/internal/chat"
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

type mockChatCompleter struct{ mock.Mock }

func (m *mockChatCompleter) CreateChatCompletion(ctx context.Context, req chat.CompletionRequest) (chat.CompletionResponse, error) {
	args := m.Called(ctx, req)
	var resp chat.CompletionResponse
	if v := args.Get(0); v != nil {
		resp = v.(chat.CompletionResponse)
	}
	return resp, args.Error(1)
}

// StreamChatCompletion's test double is configured with .Return(deltas,
// err): deltas ([]chat.Delta) are fed through onDelta in order (stopping
// early if onDelta itself errors), then err is returned.
func (m *mockChatCompleter) StreamChatCompletion(ctx context.Context, req chat.CompletionRequest, onDelta func(chat.Delta) error) error {
	args := m.Called(ctx, req, onDelta)
	if deltas, ok := args.Get(0).([]chat.Delta); ok {
		for _, d := range deltas {
			if err := onDelta(d); err != nil {
				return err
			}
		}
	}
	return args.Error(1)
}

func (m *mockChatCompleter) CheckHealth(ctx context.Context) error {
	args := m.Called(ctx)
	return args.Error(0)
}

func (m *mockChatCompleter) ListModels(ctx context.Context) ([]string, error) {
	args := m.Called(ctx)
	var models []string
	if v := args.Get(0); v != nil {
		models = v.([]string)
	}
	return models, args.Error(1)
}

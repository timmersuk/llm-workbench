package api

import (
	"context"

	"github.com/stretchr/testify/mock"

	"github.com/timmersuk/llm-workbench/internal/chat"
	"github.com/timmersuk/llm-workbench/internal/project"
	"github.com/timmersuk/llm-workbench/internal/task"
)

type mockTaskLister struct{ mock.Mock }

func (m *mockTaskLister) List() (task.ListResult, error) {
	args := m.Called()
	var result task.ListResult
	if v := args.Get(0); v != nil {
		result = v.(task.ListResult)
	}
	return result, args.Error(1)
}

func (m *mockTaskLister) Get(id string) (task.Task, error) {
	args := m.Called(id)
	var t task.Task
	if v := args.Get(0); v != nil {
		t = v.(task.Task)
	}
	return t, args.Error(1)
}

type mockProjectLister struct{ mock.Mock }

func (m *mockProjectLister) List() (project.ListResult, error) {
	args := m.Called()
	var result project.ListResult
	if v := args.Get(0); v != nil {
		result = v.(project.ListResult)
	}
	return result, args.Error(1)
}

func (m *mockProjectLister) Get(id string) (project.Project, error) {
	args := m.Called(id)
	var p project.Project
	if v := args.Get(0); v != nil {
		p = v.(project.Project)
	}
	return p, args.Error(1)
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

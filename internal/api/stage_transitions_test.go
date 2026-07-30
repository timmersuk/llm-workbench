package api

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/timmersuk/llm-workbench/internal/project"
	"github.com/timmersuk/llm-workbench/internal/task"
)

func TestHandleListStageTransitions_OK(t *testing.T) {
	projects := new(mockProjectStore)
	projects.On("Get", "demo-project").Return(project.Project{ID: "demo-project"}, nil)

	transitions := []task.StageTransition{
		{TaskID: "TASK-0001", FromStage: task.StageReview, ToStage: task.StagePlanning, Trigger: task.TransitionTriggerReviseToPlanning, Reason: "I wanted icons, not words"},
	}
	tasks := new(mockTaskStore)
	tasks.On("ListStageTransitions", "demo-project", "TASK-0001").Return(transitions, nil)

	req := httptest.NewRequest(http.MethodGet, "/api/v1/projects/demo-project/tasks/TASK-0001/stage-transitions", nil)
	req.SetPathValue("projectId", "demo-project")
	req.SetPathValue("taskId", "TASK-0001")
	w := httptest.NewRecorder()
	(&Server{Projects: projects, Tasks: tasks}).handleListStageTransitions()(w, req)

	require.Equal(t, http.StatusOK, w.Code)
	var got map[string][]task.StageTransition
	require.NoError(t, json.Unmarshal(w.Body.Bytes(), &got))
	require.Len(t, got["stage_transitions"], 1)
	assert.Equal(t, "I wanted icons, not words", got["stage_transitions"][0].Reason)
}

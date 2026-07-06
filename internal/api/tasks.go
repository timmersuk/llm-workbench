package api

import (
	"encoding/json"
	"net/http"

	"github.com/timmersuk/llm-workbench/internal/task"
)

// resolveTaskStore confirms the project named by projectId exists, then
// builds a TaskStore rooted at that project's tasks directory. Returns
// false (having already written a response) if the project doesn't exist
// or the id is otherwise invalid.
func resolveTaskStore(w http.ResponseWriter, projects ProjectStore, factory TaskStoreFactory, projectId string) (TaskStore, bool) {
	if _, err := projects.Get(projectId); err != nil {
		writeGetError(w, err)
		return nil, false
	}

	root, err := projects.TasksRoot(projectId)
	if err != nil {
		writeGetError(w, err)
		return nil, false
	}

	return factory(root), true
}

func handleListProjectTasks(projects ProjectStore, factory TaskStoreFactory) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		store, ok := resolveTaskStore(w, projects, factory, r.PathValue("projectId"))
		if !ok {
			return
		}

		result, err := store.List()
		if err != nil {
			http.Error(w, "internal error", http.StatusInternalServerError)
			return
		}
		writeJSON(w, http.StatusOK, result)
	}
}

func handleGetProjectTask(projects ProjectStore, factory TaskStoreFactory) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		store, ok := resolveTaskStore(w, projects, factory, r.PathValue("projectId"))
		if !ok {
			return
		}

		t, err := store.Get(r.PathValue("taskId"))
		if err != nil {
			writeGetError(w, err)
			return
		}
		writeJSON(w, http.StatusOK, t)
	}
}

func handleCreateProjectTask(projects ProjectStore, factory TaskStoreFactory) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		projectId := r.PathValue("projectId")
		store, ok := resolveTaskStore(w, projects, factory, projectId)
		if !ok {
			return
		}

		var t task.Task
		if err := json.NewDecoder(r.Body).Decode(&t); err != nil {
			http.Error(w, "invalid request body", http.StatusBadRequest)
			return
		}
		t.Project = projectId

		created, err := store.Create(t)
		if err != nil {
			writeMutationError(w, err)
			return
		}
		writeJSON(w, http.StatusCreated, created)
	}
}

func handleUpdateProjectTask(projects ProjectStore, factory TaskStoreFactory) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		projectId := r.PathValue("projectId")
		store, ok := resolveTaskStore(w, projects, factory, projectId)
		if !ok {
			return
		}

		var t task.Task
		if err := json.NewDecoder(r.Body).Decode(&t); err != nil {
			http.Error(w, "invalid request body", http.StatusBadRequest)
			return
		}
		t.Project = projectId

		updated, err := store.Update(r.PathValue("taskId"), t)
		if err != nil {
			writeMutationError(w, err)
			return
		}
		writeJSON(w, http.StatusOK, updated)
	}
}

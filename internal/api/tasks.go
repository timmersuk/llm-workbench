package api

import "net/http"

func handleListTasks(lister TaskLister) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		tasks, err := lister.List()
		if err != nil {
			http.Error(w, "internal error", http.StatusInternalServerError)
			return
		}
		writeJSON(w, http.StatusOK, tasks)
	}
}

func handleGetTask(lister TaskLister) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		t, err := lister.Get(r.PathValue("id"))
		if err != nil {
			writeGetError(w, err)
			return
		}
		writeJSON(w, http.StatusOK, t)
	}
}

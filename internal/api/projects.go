package api

import "net/http"

func handleListProjects(lister ProjectLister) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		projects, err := lister.List()
		if err != nil {
			http.Error(w, "internal error", http.StatusInternalServerError)
			return
		}
		writeJSON(w, http.StatusOK, projects)
	}
}

func handleGetProject(lister ProjectLister) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		p, err := lister.Get(r.PathValue("id"))
		if err != nil {
			writeGetError(w, err)
			return
		}
		writeJSON(w, http.StatusOK, p)
	}
}

package api

import "net/http"

func handleListProjects(lister ProjectLister) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		result, err := lister.List()
		if err != nil {
			http.Error(w, "internal error", http.StatusInternalServerError)
			return
		}
		writeJSON(w, http.StatusOK, result)
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

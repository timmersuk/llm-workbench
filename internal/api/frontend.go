package api

import (
	"io/fs"
	"net/http"
	"strings"
)

// newFrontendHandler serves the embedded frontend build out of frontendFS,
// falling back to index.html for any path that doesn't match a real file
// (client-side routing).
func newFrontendHandler(frontendFS fs.FS) http.Handler {
	fileServer := http.FileServer(http.FS(frontendFS))

	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if strings.HasPrefix(r.URL.Path, "/api/") {
			http.NotFound(w, r)
			return
		}
		reqPath := strings.TrimPrefix(r.URL.Path, "/")
		if reqPath == "" {
			reqPath = "index.html"
		}

		if _, err := fs.Stat(frontendFS, reqPath); err != nil {
			r = r.Clone(r.Context())
			r.URL.Path = "/"
		}

		fileServer.ServeHTTP(w, r)
	})
}

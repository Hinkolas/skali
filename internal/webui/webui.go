// Package webui serves the static console embedded only in skalid.
package webui

import (
	"bytes"
	"embed"
	"io/fs"
	"net/http"
	"path"
	"strings"
	"time"
)

//go:embed all:dist
var embedded embed.FS

// Handler keeps the API's reserved paths out of the SPA fallback.
func Handler(api http.Handler) http.Handler {
	assets, err := fs.Sub(embedded, "dist")
	if err != nil {
		panic(err)
	}
	return handler(api, assets)
}

func handler(api http.Handler, assets fs.FS) http.Handler {
	fallback, buildErr := fs.ReadFile(assets, "200.html")
	files := http.FileServer(http.FS(assets))
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		for _, prefix := range []string{"/api", "/v1", "/healthz", "/openapi.yaml", "/token"} {
			if r.URL.Path == prefix || strings.HasPrefix(r.URL.Path, prefix+"/") {
				api.ServeHTTP(w, r)
				return
			}
		}
		if r.Method != http.MethodGet && r.Method != http.MethodHead {
			w.Header().Set("Allow", "GET, HEAD")
			http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
			return
		}
		if buildErr != nil {
			http.Error(w, "The console is not embedded. Run task build, or use task dev:web during development.", http.StatusServiceUnavailable)
			return
		}
		name := strings.TrimPrefix(r.URL.Path, "/")
		for _, segment := range strings.Split(name, "/") {
			if strings.HasPrefix(segment, ".") {
				http.NotFound(w, r)
				return
			}
		}
		if name != "" && fs.ValidPath(name) {
			if info, err := fs.Stat(assets, name); err == nil && !info.IsDir() {
				w.Header().Set("Cache-Control", "no-cache")
				if strings.HasPrefix(name, "_app/immutable/") {
					w.Header().Set("Cache-Control", "public, max-age=31536000, immutable")
				}
				files.ServeHTTP(w, r)
				return
			}
		}
		if name == "_app" || strings.HasPrefix(name, "_app/") || path.Ext(name) != "" ||
			!strings.Contains(r.Header.Get("Accept"), "text/html") {
			http.NotFound(w, r)
			return
		}
		w.Header().Set("Content-Type", "text/html; charset=utf-8")
		w.Header().Set("Cache-Control", "no-cache")
		http.ServeContent(w, r, "200.html", time.Time{}, bytes.NewReader(fallback))
	})
}

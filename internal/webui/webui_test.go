package webui

import (
	"net/http"
	"net/http/httptest"
	"testing"
	"testing/fstest"

	"github.com/stretchr/testify/require"
)

func TestStaticConsoleRouting(t *testing.T) {
	assets := fstest.MapFS{
		"200.html":                {Data: []byte("<!doctype html><html>console</html>")},
		"_app/immutable/start.js": {Data: []byte("export const start = true;")},
		"favicon.svg":             {Data: []byte("<svg></svg>")},
		".gitkeep":                {},
	}
	api := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		http.Error(w, `{"error":"API miss"}`, http.StatusNotFound)
	})
	h := handler(api, assets)
	for _, tc := range []struct {
		method, path, accept     string
		status                   int
		contentType, cache, body string
	}{
		{"GET", "/", "text/html", 200, "text/html", "no-cache", "console"},
		{"GET", "/projects/example?env=production", "text/html", 200, "text/html", "no-cache", "console"},
		{"HEAD", "/auth/login", "text/html", 200, "text/html", "no-cache", ""},
		{"GET", "/_app/immutable/start.js", "*/*", 200, "javascript", "public, max-age=31536000, immutable", "export"},
		{"GET", "/favicon.svg", "image/svg+xml", 200, "image/svg+xml", "no-cache", "<svg>"},
		{"GET", "/_app/immutable/missing.js", "text/html", 404, "text/plain", "", "404"},
		{"GET", "/_app/immutable/", "text/html", 404, "text/plain", "", "404"},
		{"GET", "/missing.png", "image/png", 404, "text/plain", "", "404"},
		{"GET", "/missing.png", "text/html", 404, "text/plain", "", "404"},
		{"GET", "/.gitkeep", "text/html", 404, "text/plain", "", "404"},
		{"GET", "/api/missing", "text/html", 404, "", "", "API miss"},
		{"GET", "/v1/missing", "text/html", 404, "", "", "API miss"},
		{"GET", "/healthz", "text/html", 404, "", "", "API miss"},
		{"GET", "/openapi.yaml", "text/html", 404, "", "", "API miss"},
		{"GET", "/token", "text/html", 404, "", "", "API miss"},
		{"POST", "/auth/login", "text/html", 405, "text/plain", "", "method not allowed"},
	} {
		t.Run(tc.method+tc.path, func(t *testing.T) {
			r := httptest.NewRequest(tc.method, tc.path, nil)
			r.Header.Set("Accept", tc.accept)
			w := httptest.NewRecorder()
			h.ServeHTTP(w, r)
			require.Equal(t, tc.status, w.Code)
			require.Contains(t, w.Header().Get("Content-Type"), tc.contentType)
			require.Equal(t, tc.cache, w.Header().Get("Cache-Control"))
			if tc.method == "HEAD" {
				require.Empty(t, w.Body.String())
			} else {
				require.Contains(t, w.Body.String(), tc.body)
			}
		})
	}
}

func TestMissingConsoleBuildPreservesAPI(t *testing.T) {
	h := handler(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) { w.WriteHeader(204) }), fstest.MapFS{".gitkeep": {}})
	for path, status := range map[string]int{"/": 503, "/auth/login": 503, "/api/healthz": 204} {
		w := httptest.NewRecorder()
		h.ServeHTTP(w, httptest.NewRequest("GET", path, nil))
		require.Equal(t, status, w.Code)
	}
}

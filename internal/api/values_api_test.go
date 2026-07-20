package api

import (
	"net/http"
	"testing"

	"github.com/stretchr/testify/require"
)

// Manifest declaring one plain and one secret value; secrecy comes from the
// values block alone.
const valuesManifest = `version: "1"
name: demo
values:
  SESSION_SECRET:
    secret: true
applications:
  api:
    image: ghcr.io/example/api:1.0.0
    ports:
      http:
        port: 8080
        protocol: http
    environment:
      APP_DOMAIN: "${APP_DOMAIN:-demo.localhost}"
      SESSION_SECRET: "${SESSION_SECRET}"
`

func TestEnvironmentValues(t *testing.T) {
	a := newTestAPI(t)
	a.createUser("nick@example.com", "hunter2hunter2")
	token := a.login("nick@example.com", "hunter2hunter2")

	status, body := a.do("POST", "/v1/projects", token, map[string]any{"name": "demo"})
	require.Equal(t, http.StatusCreated, status)
	projectID := body["project"].(map[string]any)["id"].(string)
	status, body = a.do("POST", "/v1/projects/"+projectID+"/environments", token, map[string]any{"name": "production"})
	require.Equal(t, http.StatusCreated, status)
	envID := body["environment"].(map[string]any)["id"].(string)

	// Without a draft, secrecy is unknown and submission is refused.
	status, body = a.do("PUT", "/v1/environments/"+envID+"/values", token, map[string]any{
		"values": map[string]string{"APP_DOMAIN": "demo.example.com"},
	})
	require.Equal(t, http.StatusConflict, status)
	require.Equal(t, "conflict", errorCode(t, body))

	status, body = a.do("PUT", "/v1/projects/"+projectID+"/draft", token, map[string]any{"source": valuesManifest})
	require.Equal(t, http.StatusOK, status, "body: %v", body)

	// Undeclared names are rejected by name.
	status, body = a.do("PUT", "/v1/environments/"+envID+"/values", token, map[string]any{
		"values": map[string]string{"NOT_DECLARED": "x"},
	})
	require.Equal(t, http.StatusBadRequest, status)
	require.Equal(t, "bad_request", errorCode(t, body))

	// The server separates plain from secret; secrets are not echoed.
	status, body = a.do("PUT", "/v1/environments/"+envID+"/values", token, map[string]any{
		"values": map[string]string{
			"APP_DOMAIN":     "demo.example.com",
			"SESSION_SECRET": "super-secret-plant",
		},
	})
	require.Equal(t, http.StatusCreated, status, "body: %v", body)
	require.NotEmpty(t, body["candidate_id"])
	require.Equal(t, []any{"APP_DOMAIN"}, body["plain"])
	require.Equal(t, []any{"SESSION_SECRET"}, body["secret"])
	require.NotContains(t, body, "values")

	// Staged candidates are invisible in the summary until promoted.
	status, body = a.do("GET", "/v1/environments/"+envID+"/values", token, nil)
	require.Equal(t, http.StatusOK, status)
	require.Empty(t, body["values"])
}

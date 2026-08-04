package api

import (
	"context"
	"net/http"
	"testing"

	"github.com/google/uuid"
	"github.com/stretchr/testify/require"
)

// promoteCandidate flips a staged batch to current the way deploy promotion
// does, without running a full deployment.
func (a *testAPI) promoteCandidate(t *testing.T, envID, candidateID string) {
	t.Helper()
	ctx := context.Background()
	_, err := a.st.Pool.Exec(ctx, `UPDATE environment_secrets AS live SET state = 'superseded'
		WHERE live.environment_id = $1 AND live.state = 'current' AND live.name IN (
			SELECT staged.name FROM environment_secrets AS staged
			WHERE staged.environment_id = $1 AND staged.candidate_id = $2 AND staged.state = 'staged')`,
		uuid.MustParse(envID), uuid.MustParse(candidateID))
	require.NoError(t, err)
	_, err = a.st.Pool.Exec(ctx, `UPDATE environment_secrets SET state = 'current', candidate_id = NULL
		WHERE environment_id = $1 AND candidate_id = $2 AND state = 'staged'`,
		uuid.MustParse(envID), uuid.MustParse(candidateID))
	require.NoError(t, err)
}

// The value contract is derived from ${NAME} references alone; every value
// is secret and write-only.
const valuesManifest = `version: "1"
name: demo
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

	// Without a draft the value contract is unknown and submission is
	// refused: every name would be silently skipped otherwise.
	status, body = a.do("PUT", "/v1/environments/"+envID+"/values", token, map[string]any{
		"values": map[string]string{"APP_DOMAIN": "demo.example.com"},
	})
	require.Equal(t, http.StatusConflict, status)
	require.Equal(t, "conflict", errorCode(t, body))

	status, body = a.do("PUT", "/v1/projects/"+projectID+"/draft", token, map[string]any{"source": valuesManifest})
	require.Equal(t, http.StatusOK, status, "body: %v", body)

	// Unreferenced names are skipped and reported, never an error.
	status, body = a.do("PUT", "/v1/environments/"+envID+"/values", token, map[string]any{
		"values": map[string]string{
			"NOT_REFERENCED": "x",
			"SESSION_SECRET": "super-secret-plant",
		},
	})
	require.Equal(t, http.StatusCreated, status, "body: %v", body)
	require.NotEmpty(t, body["candidate_id"])
	require.Equal(t, []any{"SESSION_SECRET"}, body["staged"])
	require.Equal(t, []any{"NOT_REFERENCED"}, body["skipped"])
	require.NotContains(t, body, "values")

	// Staged candidates are invisible in the summary until promoted.
	status, body = a.do("GET", "/v1/environments/"+envID+"/values", token, nil)
	require.Equal(t, http.StatusOK, status)
	require.Empty(t, body["values"])
}

// Values are write-only end to end: listings carry names and versions
// alone, an empty string stores as a real value, and DELETE tombstones.
func TestEnvironmentValuesLifecycle(t *testing.T) {
	a := newTestAPI(t)
	a.createUser("cycle@example.com", "hunter2hunter2")
	token := a.login("cycle@example.com", "hunter2hunter2")

	status, body := a.do("POST", "/v1/projects", token, map[string]any{"name": "demo"})
	require.Equal(t, http.StatusCreated, status)
	projectID := body["project"].(map[string]any)["id"].(string)
	status, body = a.do("POST", "/v1/projects/"+projectID+"/environments", token, map[string]any{"name": "production"})
	require.Equal(t, http.StatusCreated, status)
	envID := body["environment"].(map[string]any)["id"].(string)
	status, _ = a.do("PUT", "/v1/projects/"+projectID+"/draft", token, map[string]any{"source": valuesManifest})
	require.Equal(t, http.StatusOK, status)

	// Stage both values; APP_DOMAIN is deliberately the empty string.
	status, body = a.do("PUT", "/v1/environments/"+envID+"/values", token, map[string]any{
		"values": map[string]string{
			"APP_DOMAIN":     "",
			"SESSION_SECRET": "lifecycle-plant-value",
		},
	})
	require.Equal(t, http.StatusCreated, status, "body: %v", body)
	require.Equal(t, []any{"APP_DOMAIN", "SESSION_SECRET"}, body["staged"])
	candidateID := body["candidate_id"].(string)
	a.promoteCandidate(t, envID, candidateID)

	// The listing carries names and versions alone; never a value, never a
	// secrecy class.
	status, body = a.do("GET", "/v1/environments/"+envID+"/values", token, nil)
	require.Equal(t, http.StatusOK, status)
	entries := body["values"].([]any)
	require.Len(t, entries, 2)
	first := entries[0].(map[string]any)
	require.Equal(t, "APP_DOMAIN", first["name"])
	require.Equal(t, float64(1), first["version"])
	require.NotContains(t, first, "value")
	require.NotContains(t, first, "secret")

	// DELETE tombstones the current value; a second DELETE finds nothing.
	status, _ = a.do("DELETE", "/v1/environments/"+envID+"/values/SESSION_SECRET", token, nil)
	require.Equal(t, http.StatusNoContent, status)
	status, body = a.do("GET", "/v1/environments/"+envID+"/values", token, nil)
	require.Equal(t, http.StatusOK, status)
	require.Len(t, body["values"].([]any), 1)
	status, body = a.do("DELETE", "/v1/environments/"+envID+"/values/SESSION_SECRET", token, nil)
	require.Equal(t, http.StatusNotFound, status)
	require.Equal(t, "not_found", errorCode(t, body))

	// An invalid name shape is rejected before touching the store.
	status, _ = a.do("DELETE", "/v1/environments/"+envID+"/values/not%20a%20name", token, nil)
	require.Equal(t, http.StatusBadRequest, status)
}

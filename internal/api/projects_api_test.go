package api

import (
	"net/http"
	"testing"

	"github.com/stretchr/testify/require"
)

const testManifest = `version: "1"
name: demo
applications:
  api:
    image: ghcr.io/example/api:1.0.0
    ports:
      http:
        port: 8080
        protocol: http
`

func TestProjectRoutesRequireAuth(t *testing.T) {
	a := newTestAPI(t)
	status, body := a.do("GET", "/v1/projects", "", nil)
	require.Equal(t, http.StatusUnauthorized, status)
	require.Equal(t, "invalid_token", errorCode(t, body))
}

func TestProjectLifecycle(t *testing.T) {
	a := newTestAPI(t)
	a.createUser("nick@example.com", "hunter2hunter2")
	token := a.login("nick@example.com", "hunter2hunter2")

	// Create and read back.
	status, body := a.do("POST", "/v1/projects", token, map[string]any{
		"name": "demo", "display_name": "Demo",
	})
	require.Equal(t, http.StatusCreated, status, "body: %v", body)
	proj := body["project"].(map[string]any)
	projectID := proj["id"].(string)
	require.Equal(t, "demo", proj["name"])
	require.Equal(t, "managed", proj["source_mode"])

	status, body = a.do("POST", "/v1/projects", token, map[string]any{"name": "demo"})
	require.Equal(t, http.StatusConflict, status)
	require.Equal(t, "conflict", errorCode(t, body))

	status, body = a.do("POST", "/v1/projects", token, map[string]any{"name": "Bad Name"})
	require.Equal(t, http.StatusBadRequest, status)
	require.Equal(t, "bad_request", errorCode(t, body))

	status, body = a.do("GET", "/v1/projects", token, nil)
	require.Equal(t, http.StatusOK, status)
	require.Len(t, body["projects"], 1)

	status, body = a.do("PATCH", "/v1/projects/"+projectID, token, map[string]any{
		"display_name": "Renamed", "source_mode": "file",
	})
	require.Equal(t, http.StatusOK, status)
	require.Equal(t, "Renamed", body["project"].(map[string]any)["display_name"])
	require.Equal(t, "file", body["project"].(map[string]any)["source_mode"])

	// Environments.
	status, body = a.do("POST", "/v1/projects/"+projectID+"/environments", token, map[string]any{
		"name": "production",
	})
	require.Equal(t, http.StatusCreated, status, "body: %v", body)
	envID := body["environment"].(map[string]any)["id"].(string)

	status, body = a.do("GET", "/v1/projects/"+projectID+"/environments", token, nil)
	require.Equal(t, http.StatusOK, status)
	require.Len(t, body["environments"], 1)

	status, body = a.do("GET", "/v1/environments/"+envID, token, nil)
	require.Equal(t, http.StatusOK, status)
	require.Equal(t, "production", body["environment"].(map[string]any)["name"])

	// Destructive deletes sit behind sudo mode.
	a.staleAllSessions()
	status, body = a.do("DELETE", "/v1/environments/"+envID, token, nil)
	require.Equal(t, http.StatusForbidden, status)
	require.Equal(t, "reauth_required", errorCode(t, body))

	_, err := a.st.Pool.Exec(a.t.Context(), "UPDATE sessions SET reauthenticated_at = now()")
	require.NoError(t, err)
	status, _ = a.do("DELETE", "/v1/environments/"+envID, token, nil)
	require.Equal(t, http.StatusNoContent, status)
	status, _ = a.do("DELETE", "/v1/projects/"+projectID, token, nil)
	require.Equal(t, http.StatusNoContent, status)

	status, _ = a.do("GET", "/v1/projects/"+projectID, token, nil)
	require.Equal(t, http.StatusNotFound, status)
}

func TestProjectListSummary(t *testing.T) {
	a := newTestAPI(t)
	a.createUser("nick@example.com", "hunter2hunter2")
	token := a.login("nick@example.com", "hunter2hunter2")

	status, body := a.do("POST", "/v1/projects", token, map[string]any{"name": "demo"})
	require.Equal(t, http.StatusCreated, status)
	projectID := body["project"].(map[string]any)["id"].(string)

	// Without the include the payload stays lean.
	status, body = a.do("GET", "/v1/projects", token, nil)
	require.Equal(t, http.StatusOK, status)
	require.NotContains(t, body["projects"].([]any)[0].(map[string]any), "summary")

	// A bare project: empty environments, zero counts.
	status, body = a.do("GET", "/v1/projects?include=summary", token, nil)
	require.Equal(t, http.StatusOK, status)
	summary := body["projects"].([]any)[0].(map[string]any)["summary"].(map[string]any)
	require.Empty(t, summary["environments"])
	counts := summary["service_counts"].(map[string]any)
	require.Equal(t, float64(0), counts["applications"])

	// With an environment and a draft the rollup fills in.
	status, body = a.do("POST", "/v1/projects/"+projectID+"/environments", token, map[string]any{"name": "production"})
	require.Equal(t, http.StatusCreated, status)
	envID := body["environment"].(map[string]any)["id"].(string)
	status, _ = a.do("PUT", "/v1/projects/"+projectID+"/draft", token, map[string]any{"source": testManifest})
	require.Equal(t, http.StatusOK, status)

	status, body = a.do("GET", "/v1/projects?include=summary", token, nil)
	require.Equal(t, http.StatusOK, status)
	summary = body["projects"].([]any)[0].(map[string]any)["summary"].(map[string]any)
	environments := summary["environments"].([]any)
	require.Len(t, environments, 1)
	env := environments[0].(map[string]any)
	require.Equal(t, envID, env["id"])
	require.Equal(t, "production", env["name"])
	require.Equal(t, "active", env["state"])
	require.Equal(t, "unknown", env["health"], "no services deployed yet")
	counts = summary["service_counts"].(map[string]any)
	require.Equal(t, float64(1), counts["applications"])
	require.Equal(t, float64(0), counts["databases"])
	require.Equal(t, float64(0), counts["buckets"])
}

func TestDraftSubmitAndConflicts(t *testing.T) {
	a := newTestAPI(t)
	a.createUser("nick@example.com", "hunter2hunter2")
	token := a.login("nick@example.com", "hunter2hunter2")

	status, body := a.do("POST", "/v1/projects", token, map[string]any{"name": "demo"})
	require.Equal(t, http.StatusCreated, status)
	projectID := body["project"].(map[string]any)["id"].(string)

	// No draft yet.
	status, _ = a.do("GET", "/v1/projects/"+projectID+"/draft", token, nil)
	require.Equal(t, http.StatusNotFound, status)

	// First submission.
	status, body = a.do("PUT", "/v1/projects/"+projectID+"/draft", token, map[string]any{
		"source": testManifest,
	})
	require.Equal(t, http.StatusOK, status, "body: %v", body)
	draft := body["draft"].(map[string]any)
	require.Equal(t, float64(1), draft["version"])
	require.Equal(t, "yaml", draft["format"])
	require.NotEmpty(t, draft["hash"])
	definition := draft["definition"].(map[string]any)
	require.Equal(t, "demo", definition["name"])

	// Stale version is a conflict with its own code.
	status, body = a.do("PUT", "/v1/projects/"+projectID+"/draft", token, map[string]any{
		"source": testManifest, "expected_version": 7,
	})
	require.Equal(t, http.StatusConflict, status)
	require.Equal(t, "version_conflict", errorCode(t, body))

	// Invalid manifests return positioned diagnostics, and nothing moves.
	status, body = a.do("PUT", "/v1/projects/"+projectID+"/draft", token, map[string]any{
		"source": testManifest + "unknownfield: true\n", "expected_version": 1,
	})
	require.Equal(t, http.StatusUnprocessableEntity, status)
	require.Equal(t, "invalid_manifest", errorCode(t, body))
	diagnostics := body["diagnostics"].([]any)
	require.NotEmpty(t, diagnostics)
	first := diagnostics[0].(map[string]any)
	require.NotEmpty(t, first["message"])
	require.NotZero(t, first["line"])

	status, body = a.do("GET", "/v1/projects/"+projectID+"/draft", token, nil)
	require.Equal(t, http.StatusOK, status)
	require.Equal(t, float64(1), body["draft"].(map[string]any)["version"])

	// A manifest whose name does not match the project is rejected.
	status, body = a.do("PUT", "/v1/projects/"+projectID+"/draft", token, map[string]any{
		"source": "version: \"1\"\nname: other\n", "expected_version": 1,
	})
	require.Equal(t, http.StatusUnprocessableEntity, status)
	require.Equal(t, "invalid_manifest", errorCode(t, body))
}

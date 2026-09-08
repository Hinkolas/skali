package api

import (
	"context"
	"github.com/stretchr/testify/require"
	"net/http"
	"testing"
)

func TestBackupWarningsSurviveNoopAndRollback(t *testing.T) {
	a := newTestAPI(t)
	a.createUser("warnings@example.com", "hunter2hunter2")
	token := a.login("warnings@example.com", "hunter2hunter2")
	projectID, envID := a.createEnvironment(t, token)
	manifest := deployAPIManifest + `backups:
  daily:
    schedule: "0 3 * * *"
    retention: 7d
    include:
      volumes: all
`
	def := a.submitDefinition(t, token, projectID, manifest)
	candidate := a.stageValues(t, token, envID, def, "first")
	status, body := a.do("POST", "/v1/environments/"+envID+"/plan", token, map[string]any{"definition_version_id": def, "candidate_id": candidate, "builds": buildsPayload()})
	require.Equal(t, http.StatusOK, status, "%v", body)
	require.Len(t, body["warnings"], 1)
	first := a.deployAndActivate(t, token, envID, def, candidate)
	for _, endpoint := range []string{"plan", "deployments"} {
		status, body = a.do("POST", "/v1/environments/"+envID+"/"+endpoint, token, map[string]any{"redeploy": true})
		require.Equal(t, http.StatusOK, status, "%v", body)
		require.Equal(t, true, body["up_to_date"])
		warnings := body["warnings"].([]any)
		require.Len(t, warnings, 1)
		require.Equal(t, "backup_policy_inactive", warnings[0].(map[string]any)["code"])
	}
	candidate = a.stageValues(t, token, envID, def, "second")
	a.deployAndActivate(t, token, envID, def, candidate)
	status, body = a.do("PUT", "/v1/environments/"+envID+"/target", token, map[string]any{"revision_id": first})
	require.Equal(t, http.StatusOK, status, "%v", body)
	var count int
	require.NoError(t, a.st.Pool.QueryRow(context.Background(), "SELECT count(*) FROM steps WHERE key = 'backup_policy_inactive'").Scan(&count))
	require.GreaterOrEqual(t, count, 3, "both deployments and rollback persist the warning")
}

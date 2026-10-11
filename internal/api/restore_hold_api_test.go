package api

import (
	"net/http"
	"testing"

	"github.com/google/uuid"
	"github.com/stretchr/testify/require"

	"github.com/Hinkolas/skali/internal/backup"
	"github.com/Hinkolas/skali/internal/store"
)

// An environment an incomplete restore holds shows the restore in its
// status and refuses deployments and rollbacks with restore_incomplete,
// naming both ways forward. Accepting its data takes the held restore's
// id, needs sudo mode, and frees the environment for the next deployment.
func TestRestoreHoldAPI(t *testing.T) {
	a := newTestAPI(t)
	ctx := t.Context()
	a.createUser("dev@example.com", "hunter2hunter2")
	token := a.login("dev@example.com", "hunter2hunter2")
	projectID, envID := a.createEnvironment(t, token)
	definitionVersion := a.submitDefinition(t, token, projectID, deployAPIManifest)
	candidate := a.stageValues(t, token, envID, definitionVersion, "hold-one-value")
	first := a.deployAndActivate(t, token, envID, definitionVersion, candidate)

	// A restore stopped the environment and failed halfway.
	environmentID, revisionID := uuid.MustParse(envID), uuid.MustParse(first)
	row, err := a.st.CreateBackup(ctx, store.CreateBackupParams{
		ID: uuid.New(), Kind: backup.KindRestore, EnvironmentID: environmentID, ProjectName: "demo",
		EnvironmentName: "production", RevisionID: &revisionID, Trigger: backup.TriggerManual,
		Strategy: backup.StrategyComplete,
	})
	require.NoError(t, err)
	require.NoError(t, a.st.SetBackupSnapshotKey(ctx, store.SetBackupSnapshotKeyParams{
		ID: row.ID, SnapshotKey: "skali/v1/demo/production/snapshots/snap.json",
	}))
	_, err = a.st.MarkEnvironmentRestoring(ctx, store.MarkEnvironmentRestoringParams{
		EnvironmentID: environmentID, RestoreID: row.ID, Components: []string{"db:data"},
	})
	require.NoError(t, err)
	failure := "copy failed halfway"
	for _, move := range [][2]string{{backup.StatusPending, backup.StatusRunning}, {backup.StatusRunning, backup.StatusFailed}} {
		_, err = a.st.SetBackupStatus(ctx, store.SetBackupStatusParams{ID: row.ID, FromStatus: move[0], ToStatus: move[1], Error: &failure})
		require.NoError(t, err)
	}

	status, body := a.do("GET", "/v1/environments/"+envID+"/status", token, nil)
	require.Equal(t, http.StatusOK, status, "%v", body)
	require.Equal(t, "down", body["state"])
	held := body["incomplete_restore"].(map[string]any)
	require.Equal(t, row.ID.String(), held["id"])
	require.Equal(t, "snap", held["snapshot_id"])
	require.Equal(t, "failed", held["status"])
	require.Equal(t, failure, held["error"])
	require.Equal(t, []any{"db:data"}, held["components"])

	status, body = a.do("POST", "/v1/environments/"+envID+"/deployments", token, map[string]any{
		"definition_version_id": definitionVersion, "builds": buildsPayload(),
	})
	require.Equal(t, http.StatusConflict, status, "%v", body)
	require.Equal(t, "restore_incomplete", errorCode(t, body))
	message := body["error"].(map[string]any)["message"].(string)
	require.Contains(t, message, row.ID.String())
	require.Contains(t, message, "skali backup restore")
	require.Contains(t, message, "skali backup accept")
	status, body = a.do("PUT", "/v1/environments/"+envID+"/target", token, map[string]string{"revision_id": first})
	require.Equal(t, http.StatusConflict, status, "%v", body)
	require.Equal(t, "restore_incomplete", errorCode(t, body))
	status, body = a.do("POST", "/v1/environments/"+envID+"/backups", token, nil)
	require.Equal(t, http.StatusConflict, status, "%v", body)
	require.Equal(t, "restore_incomplete", errorCode(t, body))

	status, body = a.do("POST", "/v1/environments/"+envID+"/restore/accept", token, map[string]string{"restore_id": "nope"})
	require.Equal(t, http.StatusBadRequest, status, "%v", body)
	status, body = a.do("POST", "/v1/environments/"+envID+"/restore/accept", token,
		map[string]string{"restore_id": uuid.NewString()})
	require.Equal(t, http.StatusConflict, status, "%v", body)
	require.Equal(t, "no_incomplete_restore", errorCode(t, body))
	a.staleAllSessions()
	status, body = a.do("POST", "/v1/environments/"+envID+"/restore/accept", token,
		map[string]string{"restore_id": row.ID.String()})
	require.Equal(t, http.StatusForbidden, status, "%v", body)
	require.Equal(t, "reauth_required", errorCode(t, body))

	token = a.login("dev@example.com", "hunter2hunter2")
	status, body = a.do("POST", "/v1/environments/"+envID+"/restore/accept", token,
		map[string]string{"restore_id": row.ID.String()})
	require.Equal(t, http.StatusOK, status, "%v", body)
	require.NotEmpty(t, body["run_id"])
	status, body = a.do("GET", "/v1/environments/"+envID+"/status", token, nil)
	require.Equal(t, http.StatusOK, status, "%v", body)
	require.Nil(t, body["incomplete_restore"])
	require.Equal(t, "down", body["state"], "accepting leaves the environment down")

	candidate = a.stageValues(t, token, envID, definitionVersion, "hold-two-value")
	a.deployAndActivate(t, token, envID, definitionVersion, candidate)
}

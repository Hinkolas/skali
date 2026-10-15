package api

import (
	"context"
	"net/http"
	"testing"

	"github.com/google/uuid"
	"github.com/stretchr/testify/require"
)

func TestTeardownEnvironment(t *testing.T) {
	a := newTestAPI(t)
	a.createUser("down@example.com", "hunter2hunter2")
	token := a.login("down@example.com", "hunter2hunter2")
	_, envID := a.createEnvironment(t, token)

	// A leftover running run (a rollout mid-flight) is cancelled so the
	// teardown run can start.
	oldRunID, _, _ := a.seedRun(envID)

	status, body := a.do("POST", "/v1/environments/"+envID+"/teardown", token,
		map[string]any{"purge": false})
	require.Equal(t, http.StatusAccepted, status, "%v", body)
	require.Equal(t, false, body["purge"])
	require.Equal(t, false, body["existing"])
	runID, err := uuid.Parse(body["run_id"].(string))
	require.NoError(t, err)

	run, err := a.st.GetRunByID(context.Background(), runID)
	require.NoError(t, err)
	require.Equal(t, "teardown", run.Kind)
	require.Equal(t, "running", run.Status)
	oldRun, err := a.st.GetRunByID(context.Background(), oldRunID)
	require.NoError(t, err)
	require.Equal(t, "cancelled", oldRun.Status)

	target := a.targetOf(t, envID)
	require.Equal(t, "down", target.State)
	require.Nil(t, target.TargetRevisionID)
	require.Nil(t, target.ActiveRevisionID)

	// The projection surfaces the state.
	status, body = a.do("GET", "/v1/environments/"+envID+"/status", token, nil)
	require.Equal(t, http.StatusOK, status)
	require.Equal(t, "down", body["state"])

	// Repeating the stop while it runs attaches to the same run.
	status, body = a.do("POST", "/v1/environments/"+envID+"/teardown", token,
		map[string]any{"purge": false})
	require.Equal(t, http.StatusAccepted, status, "%v", body)
	require.Equal(t, runID.String(), body["run_id"])
	require.Equal(t, true, body["existing"])

	// Purge escalates and replaces the stop's run; a later down is refused
	// because releasing is one-way, while repeating the purge stays
	// accepted and hands out the purge run already running.
	status, body = a.do("POST", "/v1/environments/"+envID+"/teardown", token,
		map[string]any{"purge": true})
	require.Equal(t, http.StatusAccepted, status, "%v", body)
	require.Equal(t, "releasing", a.targetOf(t, envID).State)
	require.Equal(t, false, body["existing"])
	purgeRunID := body["run_id"].(string)
	require.NotEqual(t, runID.String(), purgeRunID)
	run, err = a.st.GetRunByID(context.Background(), runID)
	require.NoError(t, err)
	require.Equal(t, "cancelled", run.Status, "the purge replaces the stop")

	status, body = a.do("POST", "/v1/environments/"+envID+"/teardown", token,
		map[string]any{"purge": false})
	require.Equal(t, http.StatusConflict, status)
	require.Equal(t, "conflict", errorCode(t, body))

	status, body = a.do("POST", "/v1/environments/"+envID+"/teardown", token,
		map[string]any{"purge": true})
	require.Equal(t, http.StatusAccepted, status, "%v", body)
	require.Equal(t, purgeRunID, body["run_id"])
	require.Equal(t, true, body["existing"])
	purgeRun, err := a.st.GetRunByID(context.Background(), uuid.MustParse(purgeRunID))
	require.NoError(t, err)
	require.Equal(t, "running", purgeRun.Status, "neither the refused stop nor the retry touched the purge run")
}

func TestTeardownRequiresFreshSession(t *testing.T) {
	a := newTestAPI(t)
	a.createUser("stale@example.com", "hunter2hunter2")
	token := a.login("stale@example.com", "hunter2hunter2")
	_, envID := a.createEnvironment(t, token)

	a.staleAllSessions()
	status, body := a.do("POST", "/v1/environments/"+envID+"/teardown", token, nil)
	require.Equal(t, http.StatusForbidden, status)
	require.Equal(t, "reauth_required", errorCode(t, body))

	// Reauthentication reopens the gate; an empty body means plain down.
	status, _ = a.do("POST", "/v1/auth/reauth", token, map[string]string{"password": "hunter2hunter2"})
	require.Equal(t, http.StatusNoContent, status)
	status, body = a.do("POST", "/v1/environments/"+envID+"/teardown", token, nil)
	require.Equal(t, http.StatusAccepted, status, "%v", body)
	require.Equal(t, false, body["purge"])
}

func TestTeardownUnknownEnvironment(t *testing.T) {
	a := newTestAPI(t)
	a.createUser("gone@example.com", "hunter2hunter2")
	token := a.login("gone@example.com", "hunter2hunter2")

	status, body := a.do("POST", "/v1/environments/"+uuid.NewString()+"/teardown", token, nil)
	require.Equal(t, http.StatusNotFound, status)
	require.Equal(t, "not_found", errorCode(t, body))
}

func TestTeardownRefusedWhileDeploymentPrepares(t *testing.T) {
	a := newTestAPI(t)
	a.createUser("busy@example.com", "hunter2hunter2")
	token := a.login("busy@example.com", "hunter2hunter2")
	projectID, envID := a.createEnvironment(t, token)

	// Open a deployment window: the client would now be building.
	definitionVersion := a.submitDefinition(t, token, projectID, deployAPIManifest)
	candidate := a.stageValues(t, token, envID, definitionVersion, "teardown-plant-value")
	status, body := a.do("POST", "/v1/environments/"+envID+"/deployments", token, map[string]any{
		"definition_version_id": definitionVersion,
		"candidate_id":          candidate,
		"builds":                buildsPayload(),
	})
	require.Equal(t, http.StatusCreated, status, "%v", body)

	status, body = a.do("POST", "/v1/environments/"+envID+"/teardown", token, nil)
	require.Equal(t, http.StatusConflict, status)
	require.Equal(t, "deployment_in_flight", errorCode(t, body))
}

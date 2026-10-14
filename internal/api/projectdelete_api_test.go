package api

import (
	"bufio"
	"context"
	"net/http"
	"testing"

	"github.com/google/uuid"
	"github.com/stretchr/testify/require"

	"github.com/Hinkolas/skali/internal/journal"
)

func TestDeleteProjectPurgesEnvironments(t *testing.T) {
	a := newTestAPI(t)
	a.createUser("nick@example.com", "hunter2hunter2")
	token := a.login("nick@example.com", "hunter2hunter2")
	projectID, productionID := a.createEnvironment(t, token)
	status, body := a.do("POST", "/v1/projects/"+projectID+"/environments", token, map[string]any{"name": "staging"})
	require.Equal(t, http.StatusCreated, status, "%v", body)
	stagingID := body["environment"].(map[string]any)["id"].(string)

	status, body = a.do("DELETE", "/v1/projects/"+projectID, token, nil)
	require.Equal(t, http.StatusAccepted, status, "%v", body)
	purges := body["purges"].([]any)
	require.Len(t, purges, 2)
	for _, raw := range purges {
		purge := raw.(map[string]any)
		require.Equal(t, "releasing", a.targetOf(t, purge["environment_id"].(string)).State)
		run, err := a.st.GetRunByID(context.Background(), uuid.MustParse(purge["run_id"].(string)))
		require.NoError(t, err)
		require.Equal(t, "purge", run.Kind)
		require.Equal(t, "running", run.Status)
	}

	// The project stays, marked, and accepts nothing new.
	status, body = a.do("GET", "/v1/projects/"+projectID, token, nil)
	require.Equal(t, http.StatusOK, status)
	require.NotEmpty(t, body["project"].(map[string]any)["deleting_at"])
	status, body = a.do("POST", "/v1/projects/"+projectID+"/environments", token, map[string]any{"name": "preview"})
	require.Equal(t, http.StatusConflict, status)
	require.Equal(t, "project_deleting", errorCode(t, body))

	// Repeating the delete resumes it without restarting running purges.
	status, body = a.do("DELETE", "/v1/projects/"+projectID, token, nil)
	require.Equal(t, http.StatusAccepted, status, "%v", body)
	for _, raw := range body["purges"].([]any) {
		require.Nil(t, raw.(map[string]any)["run_id"])
	}

	// The reconciler's purge epilogue: the project goes with its last
	// environment, not before.
	ctx := context.Background()
	require.NoError(t, a.deploy.DeleteEnvironment(ctx, uuid.MustParse(productionID)))
	status, _ = a.do("GET", "/v1/projects/"+projectID, token, nil)
	require.Equal(t, http.StatusOK, status)
	require.NoError(t, a.deploy.DeleteEnvironment(ctx, uuid.MustParse(stagingID)))
	status, _ = a.do("GET", "/v1/projects/"+projectID, token, nil)
	require.Equal(t, http.StatusNotFound, status)
}

func TestDeleteProjectWithoutEnvironmentsIsImmediate(t *testing.T) {
	a := newTestAPI(t)
	a.createUser("nick@example.com", "hunter2hunter2")
	token := a.login("nick@example.com", "hunter2hunter2")
	status, body := a.do("POST", "/v1/projects", token, map[string]any{"name": "empty"})
	require.Equal(t, http.StatusCreated, status)
	projectID := body["project"].(map[string]any)["id"].(string)

	status, _ = a.do("DELETE", "/v1/projects/"+projectID, token, nil)
	require.Equal(t, http.StatusNoContent, status)
	status, _ = a.do("DELETE", "/v1/projects/"+projectID, token, nil)
	require.Equal(t, http.StatusNotFound, status)
}

func TestDeleteProjectRefusedWhileDeploymentPrepares(t *testing.T) {
	a := newTestAPI(t)
	a.createUser("busy@example.com", "hunter2hunter2")
	token := a.login("busy@example.com", "hunter2hunter2")
	projectID, envID := a.createEnvironment(t, token)

	definitionVersion := a.submitDefinition(t, token, projectID, deployAPIManifest)
	candidate := a.stageValues(t, token, envID, definitionVersion, "delete-plant-value")
	status, body := a.do("POST", "/v1/environments/"+envID+"/deployments", token, map[string]any{
		"definition_version_id": definitionVersion,
		"candidate_id":          candidate,
		"builds":                buildsPayload(),
	})
	require.Equal(t, http.StatusCreated, status, "%v", body)

	status, body = a.do("DELETE", "/v1/projects/"+projectID, token, nil)
	require.Equal(t, http.StatusConflict, status)
	require.Equal(t, "deployment_in_flight", errorCode(t, body))

	// Refused before anything was decided.
	status, body = a.do("GET", "/v1/projects/"+projectID, token, nil)
	require.Equal(t, http.StatusOK, status)
	require.Nil(t, body["project"].(map[string]any)["deleting_at"])
	require.Equal(t, "active", a.targetOf(t, envID).State)
}

func TestDeployRefusedIntoDeletingProject(t *testing.T) {
	a := newTestAPI(t)
	a.createUser("nick@example.com", "hunter2hunter2")
	token := a.login("nick@example.com", "hunter2hunter2")
	projectID, envID := a.createEnvironment(t, token)
	definitionVersion := a.submitDefinition(t, token, projectID, deployAPIManifest)
	candidate := a.stageValues(t, token, envID, definitionVersion, "late-value")

	status, body := a.do("DELETE", "/v1/projects/"+projectID, token, nil)
	require.Equal(t, http.StatusAccepted, status, "%v", body)

	status, body = a.do("POST", "/v1/environments/"+envID+"/deployments", token, map[string]any{
		"definition_version_id": definitionVersion,
		"candidate_id":          candidate,
		"builds":                buildsPayload(),
	})
	require.Equal(t, http.StatusConflict, status, "%v", body)
	require.Equal(t, "project_deleting", errorCode(t, body))
}

func TestProjectStream(t *testing.T) {
	a := newTestAPI(t)
	a.createUser("nick@example.com", "hunter2hunter2")
	token := a.login("nick@example.com", "hunter2hunter2")
	projectID, productionID := a.createEnvironment(t, token)

	ctx := context.Background()
	req, err := http.NewRequestWithContext(ctx, "GET", a.srv.URL+"/v1/projects/"+projectID+"/stream", nil)
	require.NoError(t, err)
	req.Header.Set("Authorization", "Bearer "+token)
	res, err := a.srv.Client().Do(req)
	require.NoError(t, err)
	defer res.Body.Close()
	require.Equal(t, http.StatusOK, res.StatusCode)
	require.Equal(t, "text/event-stream", res.Header.Get("Content-Type"))
	readEvent := sseReader(a, bufio.NewReader(res.Body))

	event, _ := readEvent()
	require.Equal(t, "ready", event)

	// A new environment is a change, and its runs are followed from then on.
	status, body := a.do("POST", "/v1/projects/"+projectID+"/environments", token, map[string]any{"name": "staging"})
	require.Equal(t, http.StatusCreated, status, "%v", body)
	stagingID := uuid.MustParse(body["environment"].(map[string]any)["id"].(string))
	event, _ = readEvent()
	require.Equal(t, "changed", event)

	run, err := a.journal.BeginRun(ctx, journal.RunInput{
		Kind: "deployment", ProjectID: uuid.MustParse(projectID), EnvironmentID: stagingID, Actor: "tester",
	})
	require.NoError(t, err)
	event, _ = readEvent()
	require.Equal(t, "changed", event)
	require.NoError(t, a.journal.FinishRun(ctx, run.ID, journal.RunSucceeded))
	event, _ = readEvent()
	require.Equal(t, "changed", event)

	// Deleting the project ends the stream with "deleted" once the last
	// environment's purge concluded.
	status, body = a.do("DELETE", "/v1/projects/"+projectID, token, nil)
	require.Equal(t, http.StatusAccepted, status, "%v", body)
	event, _ = readEvent()
	require.Equal(t, "changed", event)
	require.NoError(t, a.deploy.DeleteEnvironment(ctx, uuid.MustParse(productionID)))
	event, _ = readEvent()
	require.Equal(t, "changed", event)
	require.NoError(t, a.deploy.DeleteEnvironment(ctx, stagingID))
	event, _ = readEvent()
	require.Equal(t, "deleted", event)
}

func TestProjectStreamFollowsValuesAndAccess(t *testing.T) {
	a := newTestAPI(t)
	a.createUser("owner@example.com", "hunter2hunter2")
	a.createUser("bob@example.com", "hunter2hunter2")
	owner := a.login("owner@example.com", "hunter2hunter2")
	bob := a.login("bob@example.com", "hunter2hunter2")
	projectID, envID := a.createEnvironment(t, owner)
	definitionVersion := a.submitDefinition(t, owner, projectID, deployAPIManifest)
	status, _ := a.do("PUT", "/v1/projects/"+projectID+"/members/bob@example.com", owner, map[string]string{"role": "read"})
	require.Equal(t, http.StatusOK, status)

	open := func(token string) func() (string, string) {
		req, err := http.NewRequestWithContext(t.Context(), "GET", a.srv.URL+"/v1/projects/"+projectID+"/stream", nil)
		require.NoError(t, err)
		req.Header.Set("Authorization", "Bearer "+token)
		res, err := a.srv.Client().Do(req)
		require.NoError(t, err)
		t.Cleanup(func() { _ = res.Body.Close() })
		require.Equal(t, http.StatusOK, res.StatusCode)
		readEvent := sseReader(a, bufio.NewReader(res.Body))
		event, _ := readEvent()
		require.Equal(t, "ready", event)
		return readEvent
	}
	ownerEvents := open(owner)
	bobEvents := open(bob)

	a.stageValues(t, owner, envID, definitionVersion, "stream-value")
	event, _ := ownerEvents()
	require.Equal(t, "changed", event)
	event, _ = bobEvents()
	require.Equal(t, "changed", event)

	// Losing the membership ends bob's stream; the owner sees a change.
	status, _ = a.do("DELETE", "/v1/projects/"+projectID+"/members/bob@example.com", owner, nil)
	require.Equal(t, http.StatusNoContent, status)
	event, _ = bobEvents()
	require.Equal(t, "revoked", event)
	event, _ = ownerEvents()
	require.Equal(t, "changed", event)
}

func TestProjectStreamUnknownProject(t *testing.T) {
	a := newTestAPI(t)
	a.createUser("nick@example.com", "hunter2hunter2")
	token := a.login("nick@example.com", "hunter2hunter2")

	status, body := a.do("GET", "/v1/projects/"+uuid.NewString()+"/stream", token, nil)
	require.Equal(t, http.StatusNotFound, status)
	require.Equal(t, "not_found", errorCode(t, body))
}

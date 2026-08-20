package api

import (
	"context"
	"net/http"
	"testing"

	"github.com/google/uuid"
	"github.com/stretchr/testify/require"
)

// A redeploy re-deploys the environment's own active revision: the same
// definition version and artifact set, re-rendered with the environment's
// current values. No bundle, no build machinery, deploy rung only.
func TestRedeployFlow(t *testing.T) {
	a := newTestAPI(t)
	a.createUser("redeploy@example.com", "hunter2hunter2")
	token := a.login("redeploy@example.com", "hunter2hunter2")
	projectID, envID := a.createEnvironment(t, token)

	definitionVersion := a.submitDefinition(t, token, projectID, deployAPIManifest)
	candidate := a.stageValues(t, token, envID, definitionVersion, "redeploy-one")
	first := a.deployAndActivate(t, token, envID, definitionVersion, candidate)

	// Exactly one selector: redeploy combines with nothing else.
	status, body := a.do("POST", "/v1/environments/"+envID+"/plan", token, map[string]any{
		"redeploy": true, "definition_version_id": definitionVersion,
	})
	require.Equal(t, http.StatusBadRequest, status, "%v", body)
	status, body = a.do("POST", "/v1/environments/"+envID+"/plan", token, map[string]any{
		"redeploy": true, "from_environment_id": envID,
	})
	require.Equal(t, http.StatusBadRequest, status, "%v", body)
	status, body = a.do("POST", "/v1/environments/"+envID+"/plan", token, map[string]any{
		"redeploy": true, "builds": buildsPayload(),
	})
	require.Equal(t, http.StatusBadRequest, status, "%v", body)

	// Nothing changed: the plan is up to date and open declines politely.
	status, body = a.do("POST", "/v1/environments/"+envID+"/plan", token, map[string]any{"redeploy": true})
	require.Equal(t, http.StatusOK, status, "%v", body)
	require.Equal(t, true, body["up_to_date"])
	require.Equal(t, "deploy", body["required_role"])
	status, body = a.do("POST", "/v1/environments/"+envID+"/deployments", token, map[string]any{"redeploy": true})
	require.Equal(t, http.StatusOK, status, "%v", body)
	require.Equal(t, true, body["up_to_date"])

	// The console flow: save a value applied immediately, so stored values
	// are what the next deployment resolves.
	status, body = a.do("PUT", "/v1/environments/"+envID+"/values", token, map[string]any{
		"values": map[string]string{"SESSION_SECRET": "redeploy-two"},
		"apply":  true,
	})
	require.Equal(t, http.StatusCreated, status, "%v", body)
	require.Equal(t, true, body["applied"])

	// The plan now shows the value moving and the redeploy is real.
	status, body = a.do("POST", "/v1/environments/"+envID+"/plan", token, map[string]any{"redeploy": true})
	require.Equal(t, http.StatusOK, status, "%v", body)
	require.Equal(t, false, body["up_to_date"])
	values := body["plan"].(map[string]any)["values"].([]any)
	require.Len(t, values, 1)
	require.Equal(t, "SESSION_SECRET", values[0].(map[string]any)["name"])
	require.Equal(t, "update", values[0].(map[string]any)["action"])

	// Open and complete: every action is a reuse by construction, exactly
	// like a promotion, and the target moves to a fresh revision.
	status, body = a.do("POST", "/v1/environments/"+envID+"/deployments", token, map[string]any{"redeploy": true})
	require.Equal(t, http.StatusCreated, status, "%v", body)
	for _, raw := range body["actions"].([]any) {
		require.Equal(t, "reuse", raw.(map[string]any)["action"], "%v", raw)
	}
	deployment := body["deployment"].(map[string]any)
	runID := deployment["run_id"].(string)
	status, body = a.do("POST", "/v1/deployments/"+deployment["id"].(string)+"/complete", token, nil)
	require.Equal(t, http.StatusOK, status, "%v", body)
	second := body["revision_id"].(string)
	require.NotEqual(t, first, second)
	require.Equal(t, second, a.targetOf(t, envID).TargetRevisionID.String())
	a.finishRun(t, runID)
	a.activate(t, envID)

	status, body = a.do("POST", "/v1/environments/"+envID+"/plan", token, map[string]any{"redeploy": true})
	require.Equal(t, http.StatusOK, status, "%v", body)
	require.Equal(t, true, body["up_to_date"])

	// An environment that runs nothing has nothing to redeploy.
	status, body = a.do("POST", "/v1/projects/"+projectID+"/environments", token,
		map[string]any{"name": "staging"})
	require.Equal(t, http.StatusCreated, status)
	emptyEnv := body["environment"].(map[string]any)["id"].(string)
	status, body = a.do("POST", "/v1/environments/"+emptyEnv+"/plan", token, map[string]any{"redeploy": true})
	require.Equal(t, http.StatusConflict, status, "%v", body)
	require.Equal(t, "conflict", errCode(body))
}

// A promote-only environment refuses direct deploys but admits redeploys:
// the definition and artifacts already passed the policy when they arrived,
// so a redeploy introduces no new code and the deploy rung suffices.
func TestRedeployPassesProtection(t *testing.T) {
	a := newTestAPI(t)
	a.createUser("owner@example.com", "hunter2hunter2")
	a.createMember("dev@example.com", "hunter2hunter2")
	owner := a.login("owner@example.com", "hunter2hunter2")
	dev := a.login("dev@example.com", "hunter2hunter2")
	projectID, envID := a.createEnvironment(t, owner)
	a.grantMember(t, projectID, "dev@example.com", "read")
	a.setCell(t, envID, "dev@example.com", "deploy")

	definitionVersion := a.submitDefinition(t, owner, projectID, deployAPIManifest)
	candidate := a.stageValues(t, owner, envID, definitionVersion, "protected-one")
	a.deployAndActivate(t, owner, envID, definitionVersion, candidate)
	a.setEnvironmentSettings(t, envID, map[string]any{"deploy_policy": "promote-only"})

	// The direct deploy is refused; the redeploy passes without any bypass,
	// on the deploy rung.
	status, body := a.do("POST", "/v1/environments/"+envID+"/plan", dev, map[string]any{
		"definition_version_id": definitionVersion, "builds": buildsPayload(),
	})
	require.Equal(t, http.StatusForbidden, status, "%v", body)
	require.Equal(t, "environment_protected", errCode(body))

	status, body = a.do("PUT", "/v1/environments/"+envID+"/values", owner, map[string]any{
		"values": map[string]string{"SESSION_SECRET": "protected-two"}, "apply": true,
	})
	require.Equal(t, http.StatusCreated, status, "%v", body)

	status, body = a.do("POST", "/v1/environments/"+envID+"/plan", dev, map[string]any{"redeploy": true})
	require.Equal(t, http.StatusOK, status, "%v", body)
	require.Equal(t, false, body["up_to_date"])
	require.Equal(t, "deploy", body["required_role"])
	require.Equal(t, false, body["bypass_protection"])
	status, body = a.do("POST", "/v1/environments/"+envID+"/deployments", dev, map[string]any{"redeploy": true})
	require.Equal(t, http.StatusCreated, status, "%v", body)
	runID := body["deployment"].(map[string]any)["run_id"].(string)
	status, body = a.do("POST", "/v1/runs/"+runID+"/cancel", dev, nil)
	require.Equal(t, http.StatusOK, status, "%v", body)
}

// A service restart stamps one application and hands rollout to the kernel
// under a run of kind "restart"; the unique running-run index serializes it
// against deployments and other restarts.
func TestRestartFlow(t *testing.T) {
	a := newTestAPI(t)
	a.createUser("restart@example.com", "hunter2hunter2")
	token := a.login("restart@example.com", "hunter2hunter2")
	projectID, envID := a.createEnvironment(t, token)

	definitionVersion := a.submitDefinition(t, token, projectID, deployAPIManifest)
	candidate := a.stageValues(t, token, envID, definitionVersion, "restart-one")
	a.deployAndActivate(t, token, envID, definitionVersion, candidate)
	before := a.targetOf(t, envID).UpdatedAt

	// An application the revision does not declare is 404.
	status, body := a.do("POST", "/v1/environments/"+envID+"/applications/nope/restart", token, nil)
	require.Equal(t, http.StatusNotFound, status, "%v", body)

	// The restart stamps the application, touches the target row (the
	// rollout-deadline clock), and starts a watchable run of kind restart.
	status, body = a.do("POST", "/v1/environments/"+envID+"/applications/web/restart", token, nil)
	require.Equal(t, http.StatusAccepted, status, "%v", body)
	runID := body["run_id"].(string)
	status, body = a.do("GET", "/v1/runs/"+runID, token, nil)
	require.Equal(t, http.StatusOK, status)
	run := body["run"].(map[string]any)
	require.Equal(t, "restart", run["kind"])
	require.Equal(t, "running", run["status"])

	envUUID, err := uuid.Parse(envID)
	require.NoError(t, err)
	stamps, err := a.st.ListEnvironmentRestarts(context.Background(), envUUID)
	require.NoError(t, err)
	require.Len(t, stamps, 1)
	require.Equal(t, "web", stamps[0].ApplicationKey)
	require.True(t, a.targetOf(t, envID).UpdatedAt.After(before))

	// A second restart while the run is in flight is refused.
	status, body = a.do("POST", "/v1/environments/"+envID+"/applications/web/restart", token, nil)
	require.Equal(t, http.StatusConflict, status, "%v", body)
	require.Equal(t, "deployment_in_flight", errCode(body))
	a.finishRun(t, runID)

	// After the run concludes, another restart bumps the same stamp.
	status, body = a.do("POST", "/v1/environments/"+envID+"/applications/web/restart", token, nil)
	require.Equal(t, http.StatusAccepted, status, "%v", body)
	a.finishRun(t, body["run_id"].(string))
	bumped, err := a.st.ListEnvironmentRestarts(context.Background(), envUUID)
	require.NoError(t, err)
	require.Len(t, bumped, 1)
	require.True(t, bumped[0].RestartedAt.After(stamps[0].RestartedAt))

	// The environment-wide form stamps the whole environment (the forced
	// deployment's stamp) under the same run kind.
	require.Nil(t, a.targetOf(t, envID).RestartedAt)
	status, body = a.do("POST", "/v1/environments/"+envID+"/restart", token, nil)
	require.Equal(t, http.StatusAccepted, status, "%v", body)
	allRunID := body["run_id"].(string)
	status, body = a.do("GET", "/v1/runs/"+allRunID, token, nil)
	require.Equal(t, http.StatusOK, status)
	require.Equal(t, "restart", body["run"].(map[string]any)["kind"])
	require.NotNil(t, a.targetOf(t, envID).RestartedAt)
	a.finishRun(t, allRunID)

	// An environment that runs nothing has nothing to restart.
	status, body = a.do("POST", "/v1/projects/"+projectID+"/environments", token,
		map[string]any{"name": "staging"})
	require.Equal(t, http.StatusCreated, status)
	emptyEnv := body["environment"].(map[string]any)["id"].(string)
	status, body = a.do("POST", "/v1/environments/"+emptyEnv+"/applications/web/restart", token, nil)
	require.Equal(t, http.StatusConflict, status, "%v", body)
	require.Equal(t, "conflict", errCode(body))
	status, body = a.do("POST", "/v1/environments/"+emptyEnv+"/restart", token, nil)
	require.Equal(t, http.StatusConflict, status, "%v", body)
	require.Equal(t, "conflict", errCode(body))
}

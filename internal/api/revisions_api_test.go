package api

import (
	"net/http"
	"testing"

	"github.com/google/uuid"
	"github.com/stretchr/testify/require"
)

// deployAndActivate drives one full deployment of the flow manifest into
// the environment and simulates the kernel activating it; returns the
// revision id.
func (a *testAPI) deployAndActivate(t *testing.T, token, envID, definitionVersion, candidate string) string {
	t.Helper()
	payload := map[string]any{
		"definition_version_id": definitionVersion,
		"builds":                buildsPayload(),
	}
	if candidate != "" {
		payload["candidate_id"] = candidate
	}
	status, body := a.do("POST", "/v1/environments/"+envID+"/deployments", token, payload)
	require.Equal(t, http.StatusCreated, status, "%v", body)
	deployment := body["deployment"].(map[string]any)
	deploymentID := deployment["id"].(string)
	runID := deployment["run_id"].(string)
	for _, raw := range body["actions"].([]any) {
		action := raw.(map[string]any)
		if action["action"].(string) == "reuse" {
			continue
		}
		require.Equal(t, "build", action["action"])
		a.registryHolds("skali/demo/web", webDigest)
		status, body = a.do("POST", "/v1/artifacts/"+action["artifact_id"].(string)+"/verify", token, map[string]any{
			"deployment_id": deploymentID, "digest": webDigest,
		})
		require.Equal(t, http.StatusOK, status, "%v", body)
	}
	status, body = a.do("POST", "/v1/deployments/"+deploymentID+"/complete", token, nil)
	require.Equal(t, http.StatusOK, status, "%v", body)
	revisionID := body["revision_id"].(string)
	a.finishRun(t, runID)
	a.activate(t, envID)
	return revisionID
}

// Rollback re-points the target at a stored revision under a watchable run
// of kind rollback; cancelling it falls the target back, and guards refuse
// the current target and foreign revisions.
func TestRollbackFlow(t *testing.T) {
	a := newTestAPI(t)
	a.createUser("rollback@example.com", "hunter2hunter2")
	token := a.login("rollback@example.com", "hunter2hunter2")
	projectID, envID := a.createEnvironment(t, token)

	definitionVersion := a.submitDefinition(t, token, projectID, deployAPIManifest)
	candidate := a.stageValues(t, token, envID, definitionVersion, "rollback-one-value")
	first := a.deployAndActivate(t, token, envID, definitionVersion, candidate)
	candidate = a.stageValues(t, token, envID, definitionVersion, "rollback-two-value")
	second := a.deployAndActivate(t, token, envID, definitionVersion, candidate)
	require.NotEqual(t, first, second)

	// Revision summaries are newest first and carry the definition version
	// (promotions resolve it from here).
	status, body := a.do("GET", "/v1/environments/"+envID+"/revisions", token, nil)
	require.Equal(t, http.StatusOK, status, "%v", body)
	revisions := body["revisions"].([]any)
	require.Len(t, revisions, 2)
	require.Equal(t, second, revisions[0].(map[string]any)["id"])
	require.Equal(t, definitionVersion, revisions[0].(map[string]any)["definition_version_id"])

	// Rolling back to the current target is refused.
	status, body = a.do("PUT", "/v1/environments/"+envID+"/target", token,
		map[string]any{"revision_id": second})
	require.Equal(t, http.StatusConflict, status)
	require.Equal(t, "conflict", errCode(body))

	// Rollback re-points the target and starts a run the client can watch.
	status, body = a.do("PUT", "/v1/environments/"+envID+"/target", token,
		map[string]any{"revision_id": first})
	require.Equal(t, http.StatusOK, status, "%v", body)
	require.Equal(t, first, body["target"].(map[string]any)["target_revision_id"])
	runID := body["run_id"].(string)
	status, body = a.do("GET", "/v1/runs/"+runID, token, nil)
	require.Equal(t, http.StatusOK, status)
	run := body["run"].(map[string]any)
	require.Equal(t, "rollback", run["kind"])
	require.Equal(t, "running", run["status"])

	// A second rollback while the run is in flight is refused.
	status, body = a.do("PUT", "/v1/environments/"+envID+"/target", token,
		map[string]any{"revision_id": second})
	require.Equal(t, http.StatusConflict, status)
	require.Equal(t, "deployment_in_flight", errCode(body))

	// Cancelling the rollback returns the target to the active revision.
	status, body = a.do("POST", "/v1/runs/"+runID+"/cancel", token, nil)
	require.Equal(t, http.StatusOK, status, "%v", body)
	require.Equal(t, "cancelled", body["status"])
	require.True(t, body["fallback"].(bool))
	require.Equal(t, second, a.targetOf(t, envID).TargetRevisionID.String())

	// A revision of another environment is refused.
	status, body = a.do("POST", "/v1/projects/"+projectID+"/environments", token,
		map[string]any{"name": "staging"})
	require.Equal(t, http.StatusCreated, status)
	otherEnv := body["environment"].(map[string]any)["id"].(string)
	status, body = a.do("PUT", "/v1/environments/"+otherEnv+"/target", token,
		map[string]any{"revision_id": first})
	require.Equal(t, http.StatusConflict, status)
	require.Equal(t, "conflict", errCode(body))
}

// A promotion re-deploys the source environment's active revision into
// another environment of the project: same definition and artifacts, the
// target's own values, no artifact window.
func TestPromotionFlowEndToEnd(t *testing.T) {
	a := newTestAPI(t)
	a.createUser("promote@example.com", "hunter2hunter2")
	token := a.login("promote@example.com", "hunter2hunter2")
	projectID, sourceEnv := a.createEnvironment(t, token)

	definitionVersion := a.submitDefinition(t, token, projectID, deployAPIManifest)
	candidate := a.stageValues(t, token, sourceEnv, definitionVersion, "promote-plant-value")
	sourceRevision := a.deployAndActivate(t, token, sourceEnv, definitionVersion, candidate)

	status, body := a.do("POST", "/v1/projects/"+projectID+"/environments", token,
		map[string]any{"name": "staging"})
	require.Equal(t, http.StatusCreated, status)
	targetEnv := body["environment"].(map[string]any)["id"].(string)

	// Exactly one of definition_version_id or from_environment_id, and no
	// build machinery alongside a promotion.
	status, body = a.do("POST", "/v1/environments/"+targetEnv+"/plan", token, map[string]any{
		"from_environment_id": sourceEnv, "definition_version_id": definitionVersion,
	})
	require.Equal(t, http.StatusBadRequest, status)
	require.Equal(t, "bad_request", errCode(body))
	status, _ = a.do("POST", "/v1/environments/"+targetEnv+"/plan", token, map[string]any{
		"from_environment_id": sourceEnv, "builds": buildsPayload(),
	})
	require.Equal(t, http.StatusBadRequest, status)

	// Guards: unknown source, source equals target, source without an
	// active revision.
	status, _ = a.do("POST", "/v1/environments/"+targetEnv+"/plan", token, map[string]any{
		"from_environment_id": uuid.NewString(),
	})
	require.Equal(t, http.StatusNotFound, status)
	status, _ = a.do("POST", "/v1/environments/"+targetEnv+"/plan", token, map[string]any{
		"from_environment_id": targetEnv,
	})
	require.Equal(t, http.StatusBadRequest, status)
	status, body = a.do("POST", "/v1/projects/"+projectID+"/environments", token,
		map[string]any{"name": "empty"})
	require.Equal(t, http.StatusCreated, status)
	emptyEnv := body["environment"].(map[string]any)["id"].(string)
	status, body = a.do("POST", "/v1/environments/"+targetEnv+"/plan", token, map[string]any{
		"from_environment_id": emptyEnv,
	})
	require.Equal(t, http.StatusConflict, status)
	require.Equal(t, "conflict", errCode(body))

	// The target environment's values are the promotion's values: missing
	// ones fail at plan time.
	status, body = a.do("POST", "/v1/environments/"+targetEnv+"/plan", token, map[string]any{
		"from_environment_id": sourceEnv,
	})
	require.Equal(t, http.StatusUnprocessableEntity, status)
	require.Equal(t, "invalid_values", errCode(body))

	// With values staged the plan is all reuse of the source's artifacts.
	targetCandidate := a.stageValues(t, token, targetEnv, definitionVersion, "staging-plant-value")
	status, body = a.do("POST", "/v1/environments/"+targetEnv+"/plan", token, map[string]any{
		"from_environment_id": sourceEnv, "candidate_id": targetCandidate,
	})
	require.Equal(t, http.StatusOK, status, "%v", body)
	require.False(t, body["up_to_date"].(bool))
	action := body["actions"].([]any)[0].(map[string]any)
	require.Equal(t, "reuse", action["action"])
	require.Equal(t, webDigest, action["digest"])

	// Open, then complete immediately: there is no artifact window.
	status, body = a.do("POST", "/v1/environments/"+targetEnv+"/deployments", token, map[string]any{
		"from_environment_id": sourceEnv, "candidate_id": targetCandidate,
	})
	require.Equal(t, http.StatusCreated, status, "%v", body)
	deployment := body["deployment"].(map[string]any)
	require.Equal(t, "reuse", body["actions"].([]any)[0].(map[string]any)["action"])
	status, body = a.do("POST", "/v1/deployments/"+deployment["id"].(string)+"/complete", token, nil)
	require.Equal(t, http.StatusOK, status, "%v", body)
	promoted := body["revision_id"].(string)
	require.NotEqual(t, sourceRevision, promoted)
	require.Equal(t, promoted, a.targetOf(t, targetEnv).TargetRevisionID.String())

	// The promoted revision pins the source's definition and artifacts
	// verbatim while carrying the target environment's identity.
	status, body = a.do("GET", "/v1/revisions/"+promoted, token, nil)
	require.Equal(t, http.StatusOK, status)
	document := body["revision"].(map[string]any)
	require.Equal(t, "staging", document["environment"])
	status, sourceBody := a.do("GET", "/v1/revisions/"+sourceRevision, token, nil)
	require.Equal(t, http.StatusOK, status)
	sourceDocument := sourceBody["revision"].(map[string]any)
	require.Equal(t, sourceDocument["definitionHash"], document["definitionHash"])
	require.Equal(t, sourceDocument["artifacts"], document["artifacts"])
	// Values resolve per environment: the secret pin references staging's
	// own version sequence, and the checksum carries the environment
	// identity even when the pinned version numbers coincide.
	require.NotEqual(t, sourceDocument["checksum"], document["checksum"])

	// After activation the same promotion is up to date.
	a.finishRun(t, deployment["run_id"].(string))
	a.activate(t, targetEnv)
	status, body = a.do("POST", "/v1/environments/"+targetEnv+"/plan", token, map[string]any{
		"from_environment_id": sourceEnv,
	})
	require.Equal(t, http.StatusOK, status, "%v", body)
	require.True(t, body["up_to_date"].(bool))

	// The recorded promotion surfaces on the project listing: the source
	// names the environment it was last promoted to, environments never
	// promoted from carry nothing.
	status, body = a.do("GET", "/v1/projects/"+projectID+"/environments", token, nil)
	require.Equal(t, http.StatusOK, status)
	lastTargets := map[string]any{}
	for _, raw := range body["environments"].([]any) {
		env := raw.(map[string]any)
		lastTargets[env["id"].(string)] = env["last_promotion_target"]
	}
	require.Equal(t, "staging", lastTargets[sourceEnv])
	require.Nil(t, lastTargets[targetEnv])
	require.Nil(t, lastTargets[emptyEnv])
}

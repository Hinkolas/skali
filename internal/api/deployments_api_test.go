package api

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"net/http"
	"strings"
	"testing"

	"github.com/google/uuid"
	"github.com/stretchr/testify/require"

	"github.com/Hinkolas/skali/internal/journal"
	"github.com/Hinkolas/skali/internal/registrytoken"
	"github.com/Hinkolas/skali/internal/store"
)

// decodeTokenAccessNames lists the repositories granted by the token in an
// exchange response body.
func decodeTokenAccessNames(t *testing.T, body map[string]any) []string {
	t.Helper()
	parts := strings.Split(body["token"].(string), ".")
	require.Len(t, parts, 3)
	claimsJSON, err := base64.RawURLEncoding.DecodeString(parts[1])
	require.NoError(t, err)
	var claims struct {
		Access []registrytoken.Access `json:"access"`
	}
	require.NoError(t, json.Unmarshal(claimsJSON, &claims))
	names := make([]string, 0, len(claims.Access))
	for _, access := range claims.Access {
		names = append(names, access.Name)
	}
	return names
}

// deployAPIManifest is the build-sourced flow manifest; the volume makes
// its removal a destructive change for the gate tests.
const deployAPIManifest = `version: "1"
name: demo
values:
  SESSION_SECRET:
    secret: true
applications:
  web:
    build:
      context: .
    ports:
      http:
        port: 8080
        protocol: http
    environment:
      SESSION_SECRET: "${SESSION_SECRET}"
    volumes:
      data:
        mountPath: /data
        size: 1GB
`

// deployAPIManifestNoVolume drops the volume: a destructive update.
const deployAPIManifestNoVolume = `version: "1"
name: demo
values:
  SESSION_SECRET:
    secret: true
applications:
  web:
    build:
      context: .
    ports:
      http:
        port: 8080
        protocol: http
    environment:
      SESSION_SECRET: "${SESSION_SECRET}"
`

const webInputHash = "1111111111111111111111111111111111111111111111111111111111111111"
const webDigest = "sha256:2222222222222222222222222222222222222222222222222222222222222222"

// submitDefinition stores a candidate definition and returns its id.
func (a *testAPI) submitDefinition(t *testing.T, token, projectID, manifest string) string {
	t.Helper()
	status, body := a.do("POST", "/v1/projects/"+projectID+"/definitions", token,
		map[string]any{"source": manifest})
	require.Equal(t, http.StatusOK, status, "%v", body)
	return body["definition_version_id"].(string)
}

// stageValues stages one secret value against the candidate definition.
func (a *testAPI) stageValues(t *testing.T, token, envID, definitionVersionID, secret string) string {
	t.Helper()
	status, body := a.do("PUT", "/v1/environments/"+envID+"/values", token, map[string]any{
		"values":                map[string]string{"SESSION_SECRET": secret},
		"definition_version_id": definitionVersionID,
	})
	require.Equal(t, http.StatusCreated, status, "%v", body)
	return body["candidate_id"].(string)
}

func buildsPayload() map[string]any {
	return map[string]any{
		"web": map[string]any{
			"input_hash":  webInputHash,
			"config_hash": "cafe",
			"platform":    "linux/arm64",
		},
	}
}

func (a *testAPI) finishRun(t *testing.T, runID string) {
	t.Helper()
	id, err := uuid.Parse(runID)
	require.NoError(t, err)
	require.NoError(t, a.journal.FinishRun(context.Background(), id, journal.RunSucceeded))
}

func (a *testAPI) activate(t *testing.T, envID string) {
	t.Helper()
	ctx := context.Background()
	id, err := uuid.Parse(envID)
	require.NoError(t, err)
	target, err := a.st.GetEnvironmentTarget(ctx, id)
	require.NoError(t, err)
	require.NotNil(t, target.TargetRevisionID)
	rows, err := a.st.SetEnvironmentActiveRevision(ctx, store.SetEnvironmentActiveRevisionParams{
		EnvironmentID: id, ActiveRevisionID: target.TargetRevisionID,
	})
	require.NoError(t, err)
	require.EqualValues(t, 1, rows)
}

func (a *testAPI) targetOf(t *testing.T, envID string) store.EnvironmentTarget {
	t.Helper()
	id, err := uuid.Parse(envID)
	require.NoError(t, err)
	target, err := a.st.GetEnvironmentTarget(context.Background(), id)
	require.NoError(t, err)
	return target
}

func TestDeploymentFlowEndToEnd(t *testing.T) {
	a := newTestAPI(t)
	a.createUser("deploy@example.com", "hunter2hunter2")
	token := a.login("deploy@example.com", "hunter2hunter2")
	projectID, envID := a.createEnvironment(t, token)

	definitionVersion := a.submitDefinition(t, token, projectID, deployAPIManifest)
	candidate := a.stageValues(t, token, envID, definitionVersion, "flow-plant-value")

	// Plan: an initial deployment creating the application, building web.
	status, body := a.do("POST", "/v1/environments/"+envID+"/plan", token, map[string]any{
		"definition_version_id": definitionVersion,
		"candidate_id":          candidate,
		"builds":                buildsPayload(),
	})
	require.Equal(t, http.StatusOK, status, "%v", body)
	require.False(t, body["up_to_date"].(bool))
	actions := body["actions"].([]any)
	require.Len(t, actions, 1)
	action := actions[0].(map[string]any)
	require.Equal(t, "build", action["action"])
	// Push refs travel the public push host, never the internal one.
	require.True(t, strings.HasPrefix(action["push_ref"].(string), testPushHost+"/skali/demo/web:"),
		"push_ref %q should start with %s", action["push_ref"], testPushHost)

	// The session token doubles as the docker login password: the realm
	// grants push on this project's repositories and nothing out of
	// contract.
	status, tokenBody := a.exchangeToken([]string{"repository:skali/demo/web:push,pull"},
		"deploy@example.com", token)
	require.Equal(t, http.StatusOK, status, "%v", tokenBody)
	require.Contains(t, decodeTokenAccessNames(t, tokenBody), "skali/demo/web")
	status, tokenBody = a.exchangeToken(
		[]string{"repository:skali/ghost/web:push,pull", "repository:admin/tools:pull"},
		"deploy@example.com", token)
	require.Equal(t, http.StatusOK, status)
	require.Empty(t, decodeTokenAccessNames(t, tokenBody))
	status, _ = a.exchangeToken([]string{"repository:skali/demo/web:push,pull"},
		"deploy@example.com", "not-a-session")
	require.Equal(t, http.StatusUnauthorized, status)

	// Open the artifact window.
	status, body = a.do("POST", "/v1/environments/"+envID+"/deployments", token, map[string]any{
		"definition_version_id": definitionVersion,
		"candidate_id":          candidate,
		"builds":                buildsPayload(),
	})
	require.Equal(t, http.StatusCreated, status, "%v", body)
	deployment := body["deployment"].(map[string]any)
	deploymentID := deployment["id"].(string)
	runID := deployment["run_id"].(string)
	require.Equal(t, "preparing", deployment["status"])
	action = body["actions"].([]any)[0].(map[string]any)
	artifactID := action["artifact_id"].(string)
	buildID := action["build_id"].(string)
	require.NotEmpty(t, artifactID)
	require.NotEmpty(t, buildID)

	// One preparing deployment per environment.
	status, body = a.do("POST", "/v1/environments/"+envID+"/deployments", token, map[string]any{
		"definition_version_id": definitionVersion,
		"candidate_id":          candidate,
		"builds":                buildsPayload(),
	})
	require.Equal(t, http.StatusConflict, status)
	require.Equal(t, "deployment_in_flight", errCode(body))

	// The client journals its build below the artifacts subtree.
	status, body = a.do("POST", "/v1/runs/"+runID+"/steps", token, map[string]any{
		"key": "artifacts.web", "title": "web", "parent_key": "artifacts",
	})
	require.Equal(t, http.StatusOK, status, "%v", body)
	status, body = a.do("POST", "/v1/runs/"+runID+"/steps", token, map[string]any{
		"key": "artifacts.web.build", "title": "Build locally", "parent_key": "artifacts.web",
	})
	require.Equal(t, http.StatusOK, status, "%v", body)
	buildStepID := body["id"].(string)
	status, body = a.do("POST", "/v1/steps/"+buildStepID+"/logs", token, map[string]any{
		"lines": []map[string]string{{"level": "info", "message": "#1 building"}},
	})
	require.Equal(t, http.StatusOK, status, "%v", body)
	status, body = a.do("POST", "/v1/builds/"+buildID+"/heartbeat", token, nil)
	require.Equal(t, http.StatusOK, status)
	require.True(t, body["alive"].(bool))
	status, body = a.do("PATCH", "/v1/steps/"+buildStepID, token, map[string]any{"status": "succeeded"})
	require.Equal(t, http.StatusOK, status, "%v", body)

	// Completion before verification is refused.
	status, body = a.do("POST", "/v1/deployments/"+deploymentID+"/complete", token, nil)
	require.Equal(t, http.StatusConflict, status)
	require.Equal(t, "artifacts_incomplete", errCode(body))

	// The registry, not the client, confirms the digest.
	a.registryHolds("skali/demo/web", webDigest)
	status, body = a.do("POST", "/v1/artifacts/"+artifactID+"/verify", token, map[string]any{
		"deployment_id": deploymentID, "digest": webDigest,
	})
	require.Equal(t, http.StatusOK, status, "%v", body)
	require.Equal(t, "verified", body["phase"])
	require.Equal(t, webDigest, body["digest"])

	// The stored artifact reference keeps the internal host: pods pull by
	// it, only pushes travel the public push host.
	artifactUUID, err := uuid.Parse(artifactID)
	require.NoError(t, err)
	artifact, err := a.st.GetArtifactByID(context.Background(), artifactUUID)
	require.NoError(t, err)
	require.Equal(t, a.registryHost+"/skali/demo/web", artifact.Reference)

	// Complete: revision, atomic promotion, rollout handoff.
	status, body = a.do("POST", "/v1/deployments/"+deploymentID+"/complete", token, nil)
	require.Equal(t, http.StatusOK, status, "%v", body)
	revisionID := body["revision_id"].(string)
	target := a.targetOf(t, envID)
	require.Equal(t, revisionID, target.TargetRevisionID.String())
	require.Nil(t, target.ActiveRevisionID)
	status, body = a.do("GET", "/v1/deployments/"+deploymentID, token, nil)
	require.Equal(t, http.StatusOK, status)
	require.Equal(t, "promoted", body["deployment"].(map[string]any)["status"])
	status, body = a.do("GET", "/v1/runs/"+runID, token, nil)
	require.Equal(t, http.StatusOK, status)
	require.Equal(t, "running", body["run"].(map[string]any)["status"],
		"the kernel, not completion, concludes the run")

	// Simulate the kernel concluding the rollout and activating.
	a.finishRun(t, runID)
	a.activate(t, envID)

	// The unchanged repeat: everything reuses, the checksum matches the
	// active revision, nothing is created.
	status, body = a.do("POST", "/v1/environments/"+envID+"/plan", token, map[string]any{
		"definition_version_id": definitionVersion,
		"builds":                buildsPayload(),
	})
	require.Equal(t, http.StatusOK, status, "%v", body)
	require.True(t, body["up_to_date"].(bool))
	require.Equal(t, "reuse", body["actions"].([]any)[0].(map[string]any)["action"])
	status, body = a.do("POST", "/v1/environments/"+envID+"/deployments", token, map[string]any{
		"definition_version_id": definitionVersion,
		"builds":                buildsPayload(),
	})
	require.Equal(t, http.StatusOK, status, "%v", body)
	require.True(t, body["up_to_date"].(bool))

	// A forced deployment bypasses up to date: the unchanged revision is
	// re-promoted with everything reused, and promotion stamps a workload
	// restart on the target.
	require.Nil(t, a.targetOf(t, envID).RestartedAt)
	status, body = a.do("POST", "/v1/environments/"+envID+"/deployments", token, map[string]any{
		"definition_version_id": definitionVersion,
		"builds":                buildsPayload(),
		"force":                 true,
	})
	require.Equal(t, http.StatusCreated, status, "%v", body)
	require.False(t, body["up_to_date"].(bool))
	require.Equal(t, "reuse", body["actions"].([]any)[0].(map[string]any)["action"])
	forcedDeployment := body["deployment"].(map[string]any)["id"].(string)
	forcedRun := body["deployment"].(map[string]any)["run_id"].(string)
	status, body = a.do("POST", "/v1/deployments/"+forcedDeployment+"/complete", token, nil)
	require.Equal(t, http.StatusOK, status, "%v", body)
	require.Equal(t, revisionID, body["revision_id"], "the unchanged revision is re-promoted")
	require.NotNil(t, a.targetOf(t, envID).RestartedAt)
	a.finishRun(t, forcedRun)
	a.activate(t, envID)

	// Rebuild discards artifact reuse: the plan wants the build again even
	// though a verified artifact matches the inputs.
	status, body = a.do("POST", "/v1/environments/"+envID+"/plan", token, map[string]any{
		"definition_version_id": definitionVersion,
		"builds":                buildsPayload(),
		"rebuild":               true,
	})
	require.Equal(t, http.StatusOK, status, "%v", body)
	require.False(t, body["up_to_date"].(bool))
	require.Equal(t, "build", body["actions"].([]any)[0].(map[string]any)["action"])

	// A values-only deploy reuses the artifact and completes with no
	// artifact window work.
	candidate = a.stageValues(t, token, envID, definitionVersion, "rotated-plant-value")
	status, body = a.do("POST", "/v1/environments/"+envID+"/deployments", token, map[string]any{
		"definition_version_id": definitionVersion,
		"candidate_id":          candidate,
		"builds":                buildsPayload(),
	})
	require.Equal(t, http.StatusCreated, status, "%v", body)
	require.Equal(t, "reuse", body["actions"].([]any)[0].(map[string]any)["action"])
	secondDeployment := body["deployment"].(map[string]any)["id"].(string)
	status, body = a.do("POST", "/v1/deployments/"+secondDeployment+"/complete", token, nil)
	require.Equal(t, http.StatusOK, status, "%v", body)
	require.NotEqual(t, revisionID, body["revision_id"])
}

func TestDeploymentDestructiveGate(t *testing.T) {
	a := newTestAPI(t)
	a.createUser("destructive@example.com", "hunter2hunter2")
	token := a.login("destructive@example.com", "hunter2hunter2")
	projectID, envID := a.createEnvironment(t, token)

	// Deploy and activate the volume-backed revision.
	definitionVersion := a.submitDefinition(t, token, projectID, deployAPIManifest)
	candidate := a.stageValues(t, token, envID, definitionVersion, "gate-plant-value")
	status, body := a.do("POST", "/v1/environments/"+envID+"/deployments", token, map[string]any{
		"definition_version_id": definitionVersion,
		"candidate_id":          candidate,
		"builds":                buildsPayload(),
	})
	require.Equal(t, http.StatusCreated, status, "%v", body)
	deploymentID := body["deployment"].(map[string]any)["id"].(string)
	runID := body["deployment"].(map[string]any)["run_id"].(string)
	artifactID := body["actions"].([]any)[0].(map[string]any)["artifact_id"].(string)
	a.registryHolds("skali/demo/web", webDigest)
	status, _ = a.do("POST", "/v1/artifacts/"+artifactID+"/verify", token, map[string]any{
		"deployment_id": deploymentID, "digest": webDigest,
	})
	require.Equal(t, http.StatusOK, status)
	status, _ = a.do("POST", "/v1/deployments/"+deploymentID+"/complete", token, nil)
	require.Equal(t, http.StatusOK, status)
	a.finishRun(t, runID)
	a.activate(t, envID)

	// Dropping the volume is destructive: visible in the plan, refused by
	// open without the explicit override.
	withoutVolume := a.submitDefinition(t, token, projectID, deployAPIManifestNoVolume)
	status, body = a.do("POST", "/v1/environments/"+envID+"/plan", token, map[string]any{
		"definition_version_id": withoutVolume,
		"builds":                buildsPayload(),
	})
	require.Equal(t, http.StatusOK, status, "%v", body)
	changes := body["plan"].(map[string]any)["changes"].([]any)
	require.NotEmpty(t, changes)
	destructive := false
	for _, raw := range changes {
		change := raw.(map[string]any)
		if change["destructive"] == true {
			destructive = true
			require.Contains(t, change["detail"], "persistent volumes")
		}
	}
	require.True(t, destructive)

	status, body = a.do("POST", "/v1/environments/"+envID+"/deployments", token, map[string]any{
		"definition_version_id": withoutVolume,
		"builds":                buildsPayload(),
	})
	require.Equal(t, http.StatusConflict, status)
	require.Equal(t, "destructive_change", errCode(body))

	// The explicit override admits it.
	status, body = a.do("POST", "/v1/environments/"+envID+"/deployments", token, map[string]any{
		"definition_version_id": withoutVolume,
		"allow_destructive":     true,
		"builds":                buildsPayload(),
	})
	require.Equal(t, http.StatusCreated, status, "%v", body)
}

// A failed verification and a failed deployment leave values, target, and
// active revision untouched (the transcript's atomicity contract).
func TestDeploymentFailureLeavesEnvironmentUntouched(t *testing.T) {
	a := newTestAPI(t)
	a.createUser("atomic@example.com", "hunter2hunter2")
	token := a.login("atomic@example.com", "hunter2hunter2")
	projectID, envID := a.createEnvironment(t, token)

	definitionVersion := a.submitDefinition(t, token, projectID, deployAPIManifest)
	candidate := a.stageValues(t, token, envID, definitionVersion, "atomic-plant-value")
	status, body := a.do("POST", "/v1/environments/"+envID+"/deployments", token, map[string]any{
		"definition_version_id": definitionVersion,
		"candidate_id":          candidate,
		"builds":                buildsPayload(),
	})
	require.Equal(t, http.StatusCreated, status, "%v", body)
	deploymentID := body["deployment"].(map[string]any)["id"].(string)
	artifactID := body["actions"].([]any)[0].(map[string]any)["artifact_id"].(string)

	// The registry does not hold the claimed digest.
	status, body = a.do("POST", "/v1/artifacts/"+artifactID+"/verify", token, map[string]any{
		"deployment_id": deploymentID, "digest": webDigest,
	})
	require.Equal(t, http.StatusConflict, status)
	require.Equal(t, "digest_mismatch", errCode(body))

	// The client reports its build failed; the window closes.
	status, _ = a.do("POST", "/v1/deployments/"+deploymentID+"/fail", token, nil)
	require.Equal(t, http.StatusOK, status)

	target := a.targetOf(t, envID)
	require.Nil(t, target.TargetRevisionID, "target untouched")
	require.Nil(t, target.ActiveRevisionID)
	status, body = a.do("GET", "/v1/environments/"+envID+"/values", token, nil)
	require.Equal(t, http.StatusOK, status)
	require.Empty(t, body["values"], "staged values were discarded, none promoted")
	status, body = a.do("GET", "/v1/deployments/"+deploymentID, token, nil)
	require.Equal(t, http.StatusOK, status)
	require.Equal(t, "failed", body["deployment"].(map[string]any)["status"])

	// The environment is free for the next deployment.
	candidate = a.stageValues(t, token, envID, definitionVersion, "retry-plant-value")
	status, _ = a.do("POST", "/v1/environments/"+envID+"/deployments", token, map[string]any{
		"definition_version_id": definitionVersion,
		"candidate_id":          candidate,
		"builds":                buildsPayload(),
	})
	require.Equal(t, http.StatusCreated, status)
}

func TestRunCancellationPolicy(t *testing.T) {
	a := newTestAPI(t)
	a.createUser("cancel@example.com", "hunter2hunter2")
	token := a.login("cancel@example.com", "hunter2hunter2")
	projectID, envID := a.createEnvironment(t, token)
	definitionVersion := a.submitDefinition(t, token, projectID, deployAPIManifest)

	deployAndPromote := func(secret string) (deploymentID, runID, revisionID string) {
		t.Helper()
		candidate := a.stageValues(t, token, envID, definitionVersion, secret)
		status, body := a.do("POST", "/v1/environments/"+envID+"/deployments", token, map[string]any{
			"definition_version_id": definitionVersion,
			"candidate_id":          candidate,
			"builds":                buildsPayload(),
		})
		require.Equal(t, http.StatusCreated, status, "%v", body)
		deploymentID = body["deployment"].(map[string]any)["id"].(string)
		runID = body["deployment"].(map[string]any)["run_id"].(string)
		if artifactID, ok := body["actions"].([]any)[0].(map[string]any)["artifact_id"].(string); ok {
			a.registryHolds("skali/demo/web", webDigest)
			status, verifyBody := a.do("POST", "/v1/artifacts/"+artifactID+"/verify", token, map[string]any{
				"deployment_id": deploymentID, "digest": webDigest,
			})
			// Reused artifacts are already verified; a conflict is fine.
			require.Contains(t, []int{http.StatusOK, http.StatusConflict}, status, "%v", verifyBody)
		}
		status, body = a.do("POST", "/v1/deployments/"+deploymentID+"/complete", token, nil)
		require.Equal(t, http.StatusOK, status, "%v", body)
		return deploymentID, runID, body["revision_id"].(string)
	}

	// Cancellation before promotion closes the window; nothing moved.
	candidate := a.stageValues(t, token, envID, definitionVersion, "cancel-plant-one")
	status, body := a.do("POST", "/v1/environments/"+envID+"/deployments", token, map[string]any{
		"definition_version_id": definitionVersion,
		"candidate_id":          candidate,
		"builds":                buildsPayload(),
	})
	require.Equal(t, http.StatusCreated, status, "%v", body)
	openRun := body["deployment"].(map[string]any)["run_id"].(string)
	openDeployment := body["deployment"].(map[string]any)["id"].(string)
	status, body = a.do("POST", "/v1/runs/"+openRun+"/cancel", token, nil)
	require.Equal(t, http.StatusOK, status, "%v", body)
	require.False(t, body["fallback"].(bool))
	require.Nil(t, a.targetOf(t, envID).TargetRevisionID)
	status, body = a.do("GET", "/v1/deployments/"+openDeployment, token, nil)
	require.Equal(t, http.StatusOK, status)
	require.Equal(t, "cancelled", body["deployment"].(map[string]any)["status"])

	// Revision A activates; revision B promotes and is cancelled mid
	// rollout: the target returns to A.
	_, runA, revA := deployAndPromote("cancel-plant-two")
	a.finishRun(t, runA)
	a.activate(t, envID)
	_, runB, revB := deployAndPromote("cancel-plant-three")
	require.NotEqual(t, revA, revB)
	status, body = a.do("POST", "/v1/runs/"+runB+"/cancel", token, nil)
	require.Equal(t, http.StatusOK, status, "%v", body)
	require.True(t, body["fallback"].(bool))
	target := a.targetOf(t, envID)
	require.Equal(t, revA, target.TargetRevisionID.String())
	require.Equal(t, revA, target.ActiveRevisionID.String())

	// A settled run refuses cancellation.
	status, body = a.do("POST", "/v1/runs/"+runB+"/cancel", token, nil)
	require.Equal(t, http.StatusConflict, status)
	require.Equal(t, "conflict", errCode(body))
}

func TestClientStepAuthorization(t *testing.T) {
	a := newTestAPI(t)
	a.createUser("owner@example.com", "hunter2hunter2")
	a.createUser("intruder@example.com", "hunter2hunter2")
	owner := a.login("owner@example.com", "hunter2hunter2")
	intruder := a.login("intruder@example.com", "hunter2hunter2")
	projectID, envID := a.createEnvironment(t, owner)
	definitionVersion := a.submitDefinition(t, owner, projectID, deployAPIManifest)
	candidate := a.stageValues(t, owner, envID, definitionVersion, "authz-plant-value")

	status, body := a.do("POST", "/v1/environments/"+envID+"/deployments", owner, map[string]any{
		"definition_version_id": definitionVersion,
		"candidate_id":          candidate,
		"builds":                buildsPayload(),
	})
	require.Equal(t, http.StatusCreated, status, "%v", body)
	runID := body["deployment"].(map[string]any)["run_id"].(string)

	// Another actor is shut out of the run entirely.
	status, body = a.do("POST", "/v1/runs/"+runID+"/steps", intruder, map[string]any{
		"key": "artifacts.web", "title": "web", "parent_key": "artifacts",
	})
	require.Equal(t, http.StatusForbidden, status)
	require.Equal(t, "forbidden", errCode(body))

	// The owner cannot leave the artifacts subtree.
	status, body = a.do("POST", "/v1/runs/"+runID+"/steps", owner, map[string]any{
		"key": "promote", "title": "Promote", "parent_key": "artifacts",
	})
	require.Equal(t, http.StatusForbidden, status, "%v", body)

	// Server-owned steps reject client writes.
	id, err := uuid.Parse(runID)
	require.NoError(t, err)
	validateStep, err := a.st.GetStepByRunAndKey(context.Background(), store.GetStepByRunAndKeyParams{
		RunID: id, Key: "validate",
	})
	require.NoError(t, err)
	status, body = a.do("PATCH", "/v1/steps/"+validateStep.ID.String(), owner, map[string]any{
		"status": "failed",
	})
	require.Equal(t, http.StatusForbidden, status, "%v", body)

	// Once the run settles, even the owner's writes are refused.
	status, _ = a.do("POST", "/v1/runs/"+runID+"/cancel", owner, nil)
	require.Equal(t, http.StatusOK, status)
	status, body = a.do("POST", "/v1/runs/"+runID+"/steps", owner, map[string]any{
		"key": "artifacts.web", "title": "web", "parent_key": "artifacts",
	})
	require.Equal(t, http.StatusConflict, status, "%v", body)
}

// The build-input requirement: a build-sourced application without its
// client hashes cannot plan or open.
func TestDeploymentRequiresBuildInputs(t *testing.T) {
	a := newTestAPI(t)
	a.createUser("inputs@example.com", "hunter2hunter2")
	token := a.login("inputs@example.com", "hunter2hunter2")
	projectID, envID := a.createEnvironment(t, token)
	definitionVersion := a.submitDefinition(t, token, projectID, deployAPIManifest)

	status, body := a.do("POST", "/v1/environments/"+envID+"/plan", token, map[string]any{
		"definition_version_id": definitionVersion,
	})
	require.Equal(t, http.StatusBadRequest, status)
	require.Contains(t, errMessage(body), "no build input hashes")
	_ = strings.TrimSpace("")
}

// The platform guard: a build that targets none of the observed cluster
// platforms is rejected at plan and open time; overlap or an unobserved
// cluster passes.
func TestDeploymentPlatformGuard(t *testing.T) {
	a := newTestAPI(t)
	a.createUser("platform@example.com", "hunter2hunter2")
	token := a.login("platform@example.com", "hunter2hunter2")
	projectID, envID := a.createEnvironment(t, token)
	definitionVersion := a.submitDefinition(t, token, projectID, deployAPIManifest)
	candidate := a.stageValues(t, token, envID, definitionVersion, "platform-secret")

	planWith := func(platform string) (int, map[string]any) {
		builds := buildsPayload()
		builds["web"].(map[string]any)["platform"] = platform
		return a.do("POST", "/v1/environments/"+envID+"/plan", token, map[string]any{
			"definition_version_id": definitionVersion,
			"candidate_id":          candidate,
			"builds":                builds,
		})
	}

	// No observed nodes: the guard skips, whatever the platform.
	status, body := planWith("linux/arm64")
	require.Equal(t, http.StatusOK, status, "%v", body)

	// An amd64-only cluster refuses an arm64-only build.
	a.observed.SetNodeArch("node-1", "amd64")
	status, body = planWith("linux/arm64")
	require.Equal(t, http.StatusUnprocessableEntity, status, "%v", body)
	require.Equal(t, "platform_mismatch", errCode(body))
	require.Contains(t, errMessage(body), "linux/amd64")
	require.Contains(t, errMessage(body), "upgrade the skali CLI")

	// A matching or covering build passes.
	status, body = planWith("linux/amd64")
	require.Equal(t, http.StatusOK, status, "%v", body)
	status, body = planWith("linux/amd64,linux/arm64")
	require.Equal(t, http.StatusOK, status, "%v", body)

	// Open enforces the same rule.
	builds := buildsPayload()
	status, body = a.do("POST", "/v1/environments/"+envID+"/deployments", token, map[string]any{
		"definition_version_id": definitionVersion,
		"candidate_id":          candidate,
		"build_executor":        "local",
		"builds":                builds,
	})
	require.Equal(t, http.StatusUnprocessableEntity, status, "%v", body)
	require.Equal(t, "platform_mismatch", errCode(body))
}

func errCode(body map[string]any) string {
	detail, _ := body["error"].(map[string]any)
	code, _ := detail["code"].(string)
	return code
}

func errMessage(body map[string]any) string {
	detail, _ := body["error"].(map[string]any)
	message, _ := detail["message"].(string)
	return message
}

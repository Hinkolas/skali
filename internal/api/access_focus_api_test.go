package api

import (
	"encoding/base64"
	"encoding/json"
	"net/http"
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/Hinkolas/skali/internal/registrytoken"
)

// tokenActions decodes the access claim into repository -> actions.
func tokenActions(t *testing.T, body map[string]any) map[string][]string {
	t.Helper()
	parts := splitJWT(t, body["token"].(string))
	var claims struct {
		Access []registrytoken.Access `json:"access"`
	}
	require.NoError(t, json.Unmarshal(parts, &claims))
	actions := map[string][]string{}
	for _, access := range claims.Access {
		actions[access.Name] = access.Actions
	}
	return actions
}

func splitJWT(t *testing.T, token string) []byte {
	t.Helper()
	segments := 0
	start := 0
	for i := 0; i < len(token); i++ {
		if token[i] == '.' {
			segments++
			if segments == 1 {
				start = i + 1
			} else if segments == 2 {
				claims, err := base64.RawURLEncoding.DecodeString(token[start:i])
				require.NoError(t, err)
				return claims
			}
		}
	}
	t.Fatal("malformed token")
	return nil
}

// Membership makes projects visible; non-members see nothing, and the
// project payload carries the caller's standing.
func TestProjectVisibilityAndAccessPayload(t *testing.T) {
	a := newTestAPI(t)
	a.createUser("owner@example.com", "hunter2hunter2")
	a.createMember("bob@example.com", "hunter2hunter2")
	a.createAdmin("root@example.com", "hunter2hunter2")
	owner := a.login("owner@example.com", "hunter2hunter2")
	bob := a.login("bob@example.com", "hunter2hunter2")
	root := a.login("root@example.com", "hunter2hunter2")
	projectID, prodID := a.createEnvironment(t, owner)
	status, body := a.do("POST", "/v1/projects/"+projectID+"/environments", owner, map[string]any{"name": "staging"})
	require.Equal(t, http.StatusCreated, status, "%v", body)

	// Bob is not a member: nothing listed, direct access is 404.
	status, body = a.do("GET", "/v1/projects", bob, nil)
	require.Equal(t, http.StatusOK, status)
	require.Empty(t, body["projects"])
	status, _ = a.do("GET", "/v1/projects/"+projectID, bob, nil)
	require.Equal(t, http.StatusNotFound, status)
	status, _ = a.do("GET", "/v1/environments/"+prodID, bob, nil)
	require.Equal(t, http.StatusNotFound, status)

	// The owner is the project's admin everywhere; the instance admin too.
	status, body = a.do("GET", "/v1/projects/"+projectID, owner, nil)
	require.Equal(t, http.StatusOK, status)
	access := body["project"].(map[string]any)["access"].(map[string]any)
	require.Equal(t, "admin", access["role"])
	require.Equal(t, map[string]any{"production": "admin", "staging": "admin"}, access["environments"])
	status, body = a.do("GET", "/v1/projects?include=summary", root, nil)
	require.Equal(t, http.StatusOK, status)
	require.Len(t, body["projects"], 1)

	// Bob as reader with a none cell on production: listed, production
	// locked in every shape.
	a.grantMember(t, projectID, "bob@example.com", "read")
	a.setCell(t, prodID, "bob@example.com", "none")
	status, body = a.do("GET", "/v1/projects?include=summary", bob, nil)
	require.Equal(t, http.StatusOK, status)
	projects := body["projects"].([]any)
	require.Len(t, projects, 1)
	project := projects[0].(map[string]any)
	access = project["access"].(map[string]any)
	require.Equal(t, "read", access["role"])
	require.Equal(t, map[string]any{"production": "none", "staging": "read"}, access["environments"])
	for _, raw := range project["summary"].(map[string]any)["environments"].([]any) {
		env := raw.(map[string]any)
		if env["name"] == "production" {
			require.Equal(t, "none", env["access"])
			require.Nil(t, env["state"], "a locked environment shows no state")
			require.Nil(t, env["health"])
		} else {
			require.Equal(t, "read", env["access"])
			require.NotNil(t, env["state"])
		}
	}

	status, body = a.do("GET", "/v1/projects/"+projectID+"/environments", bob, nil)
	require.Equal(t, http.StatusOK, status)
	for _, raw := range body["environments"].([]any) {
		env := raw.(map[string]any)
		if env["name"] == "production" {
			require.Equal(t, "none", env["access"])
			require.Nil(t, env["created_at"])
			require.Nil(t, env["settings"])
		} else {
			require.Equal(t, "read", env["access"])
			require.NotNil(t, env["settings"])
		}
	}
	status, body = a.do("GET", "/v1/environments/"+prodID, bob, nil)
	require.Equal(t, http.StatusOK, status)
	env := body["environment"].(map[string]any)
	require.Equal(t, "none", env["access"])
	require.Nil(t, env["settings"])
	status, body = a.do("GET", "/v1/environments/"+prodID+"/status", bob, nil)
	require.Equal(t, http.StatusForbidden, status)
	require.Equal(t, "forbidden", errorCode(t, body))
	require.Contains(t, errorMessage(t, body), "read on environment production required")
}

func errorMessage(t *testing.T, body map[string]any) string {
	t.Helper()
	errObj, ok := body["error"].(map[string]any)
	require.True(t, ok, "expected error envelope in %v", body)
	return errObj["message"].(string)
}

// Members and cells: managed by project admins (and environment admins for
// cells), addressed by id or email, cells need a membership, removing the
// member drops the cells, the ceiling caps inherited roles but not cells.
func TestMembersAndCellsAPI(t *testing.T) {
	a := newTestAPI(t)
	a.createUser("owner@example.com", "hunter2hunter2")
	a.createMember("bob@example.com", "hunter2hunter2")
	a.createMember("carol@example.com", "hunter2hunter2")
	owner := a.login("owner@example.com", "hunter2hunter2")
	bob := a.login("bob@example.com", "hunter2hunter2")
	projectID, prodID := a.createEnvironment(t, owner)
	status, body := a.do("POST", "/v1/projects/"+projectID+"/environments", owner, map[string]any{"name": "staging"})
	require.Equal(t, http.StatusCreated, status, "%v", body)
	stagingID := body["environment"].(map[string]any)["id"].(string)

	// A cell before membership is refused.
	status, body = a.do("PUT", "/v1/environments/"+stagingID+"/access/bob@example.com", owner, map[string]string{"role": "deploy"})
	require.Equal(t, http.StatusConflict, status, "%v", body)
	// Unknown users, bad roles.
	status, _ = a.do("PUT", "/v1/projects/"+projectID+"/members/nobody@example.com", owner, map[string]string{"role": "read"})
	require.Equal(t, http.StatusNotFound, status)
	status, _ = a.do("PUT", "/v1/projects/"+projectID+"/members/bob@example.com", owner, map[string]string{"role": "none"})
	require.Equal(t, http.StatusBadRequest, status)
	status, _ = a.do("PUT", "/v1/projects/"+projectID+"/members/bob@example.com", owner, map[string]string{"role": "owner"})
	require.Equal(t, http.StatusBadRequest, status)

	// Membership by email, then a cell by id.
	status, body = a.do("PUT", "/v1/projects/"+projectID+"/members/bob@example.com", owner, map[string]string{"role": "maintain"})
	require.Equal(t, http.StatusOK, status, "%v", body)
	member := body["member"].(map[string]any)
	require.Equal(t, "maintain", member["role"])
	bobID := member["user_id"].(string)
	status, body = a.do("PUT", "/v1/environments/"+prodID+"/access/"+bobID, owner, map[string]string{"role": "read"})
	require.Equal(t, http.StatusOK, status, "%v", body)
	require.Equal(t, "read", body["access"].(map[string]any)["role"])

	status, body = a.do("GET", "/v1/projects/"+projectID+"/members", bob, nil)
	require.Equal(t, http.StatusOK, status)
	require.Len(t, body["members"], 2)
	status, body = a.do("GET", "/v1/environments/"+prodID+"/access", bob, nil)
	require.Equal(t, http.StatusOK, status)
	require.Len(t, body["access"], 1)

	// Effective roles: maintain on staging, read on production (cell).
	status, body = a.do("GET", "/v1/projects/"+projectID, bob, nil)
	require.Equal(t, http.StatusOK, status)
	require.Equal(t, map[string]any{"production": "read", "staging": "maintain"},
		body["project"].(map[string]any)["access"].(map[string]any)["environments"])

	// Bob (maintain) cannot manage members; a maintainer may create
	// environments and becomes admin of what they create.
	status, _ = a.do("PUT", "/v1/projects/"+projectID+"/members/carol@example.com", bob, map[string]string{"role": "read"})
	require.Equal(t, http.StatusForbidden, status)
	status, body = a.do("POST", "/v1/projects/"+projectID+"/environments", bob, map[string]any{"name": "feat-x"})
	require.Equal(t, http.StatusCreated, status, "%v", body)
	featID := body["environment"].(map[string]any)["id"].(string)
	require.Equal(t, "admin", body["environment"].(map[string]any)["access"])
	status, body = a.do("GET", "/v1/environments/"+featID+"/access", bob, nil)
	require.Equal(t, http.StatusOK, status)
	require.Len(t, body["access"], 1)
	// ... and can manage cells there but not on production.
	a.grantMember(t, projectID, "carol@example.com", "read")
	status, _ = a.do("PUT", "/v1/environments/"+featID+"/access/carol@example.com", bob, map[string]string{"role": "deploy"})
	require.Equal(t, http.StatusOK, status)
	status, _ = a.do("PUT", "/v1/environments/"+prodID+"/access/carol@example.com", bob, map[string]string{"role": "deploy"})
	require.Equal(t, http.StatusForbidden, status)

	// The ceiling caps inherited roles, not cells: carol (read) with a
	// deploy cell on feat-x keeps deploy under a read ceiling; bob
	// (maintain) drops to read on staging under it.
	a.setEnvironmentSettings(t, featID, map[string]any{"max_role": "read"})
	a.setEnvironmentSettings(t, stagingID, map[string]any{"max_role": "read"})
	status, body = a.do("GET", "/v1/projects/"+projectID, bob, nil)
	require.Equal(t, http.StatusOK, status)
	require.Equal(t, "read", body["project"].(map[string]any)["access"].(map[string]any)["environments"].(map[string]any)["staging"])
	carol := a.login("carol@example.com", "hunter2hunter2")
	status, body = a.do("GET", "/v1/projects/"+projectID, carol, nil)
	require.Equal(t, http.StatusOK, status)
	require.Equal(t, "deploy", body["project"].(map[string]any)["access"].(map[string]any)["environments"].(map[string]any)["feat-x"])

	// Removing the member drops the cells.
	status, _ = a.do("DELETE", "/v1/projects/"+projectID+"/members/bob@example.com", owner, nil)
	require.Equal(t, http.StatusNoContent, status)
	status, body = a.do("GET", "/v1/environments/"+prodID+"/access", owner, nil)
	require.Equal(t, http.StatusOK, status)
	require.Empty(t, body["access"])
	status, _ = a.do("GET", "/v1/projects/"+projectID, bob, nil)
	require.Equal(t, http.StatusNotFound, status)
	status, _ = a.do("DELETE", "/v1/projects/"+projectID+"/members/bob@example.com", owner, nil)
	require.Equal(t, http.StatusNotFound, status)
}

// Project creation needs create_projects (or the instance role); the creator
// becomes admin. Environment settings: priority high is instance-admin only,
// creation defaults follow the priority.
func TestProjectCreationAndEnvironmentSettings(t *testing.T) {
	a := newTestAPI(t)
	a.createAdmin("root@example.com", "hunter2hunter2")
	a.createMember("bob@example.com", "hunter2hunter2")
	root := a.login("root@example.com", "hunter2hunter2")
	bob := a.login("bob@example.com", "hunter2hunter2")

	status, body := a.do("POST", "/v1/projects", bob, map[string]any{"name": "tools"})
	require.Equal(t, http.StatusForbidden, status, "%v", body)

	status, body = a.do("GET", "/v1/auth/session", bob, nil)
	require.Equal(t, http.StatusOK, status)
	require.Equal(t, false, body["user"].(map[string]any)["create_projects"])
	bobID := body["user"].(map[string]any)["id"].(string)
	status, body = a.do("PATCH", "/v1/users/"+bobID, root, map[string]any{"create_projects": true})
	require.Equal(t, http.StatusOK, status, "%v", body)
	require.Equal(t, true, body["user"].(map[string]any)["create_projects"])
	status, body = a.do("GET", "/v1/auth/session", bob, nil)
	require.Equal(t, http.StatusOK, status)
	require.Equal(t, true, body["user"].(map[string]any)["create_projects"])

	status, body = a.do("POST", "/v1/projects", bob, map[string]any{"name": "tools"})
	require.Equal(t, http.StatusCreated, status, "%v", body)
	projectID := body["project"].(map[string]any)["id"].(string)
	require.Equal(t, "admin", body["project"].(map[string]any)["access"].(map[string]any)["role"])

	// High priority needs an instance admin, on create and on raise.
	status, body = a.do("POST", "/v1/projects/"+projectID+"/environments", bob, map[string]any{"name": "production", "priority": "high"})
	require.Equal(t, http.StatusForbidden, status, "%v", body)
	status, body = a.do("POST", "/v1/projects/"+projectID+"/environments", bob, map[string]any{"name": "production", "priority": "urgent"})
	require.Equal(t, http.StatusBadRequest, status, "%v", body)
	status, body = a.do("POST", "/v1/projects/"+projectID+"/environments", bob, map[string]any{"name": "production"})
	require.Equal(t, http.StatusCreated, status, "%v", body)
	env := body["environment"].(map[string]any)
	envID := env["id"].(string)
	settings := env["settings"].(map[string]any)
	require.Equal(t, "admin", settings["max_role"])
	require.Equal(t, "direct", settings["deploy_policy"])
	require.Equal(t, []any{}, settings["promote_from"])
	require.Equal(t, "normal", settings["priority"])

	status, body = a.do("PATCH", "/v1/environments/"+envID, bob, map[string]any{"priority": "high"})
	require.Equal(t, http.StatusForbidden, status, "%v", body)
	status, body = a.do("PATCH", "/v1/environments/"+envID, bob, map[string]any{})
	require.Equal(t, http.StatusBadRequest, status, "%v", body)
	status, body = a.do("PATCH", "/v1/environments/"+envID, bob, map[string]any{"max_role": "owner"})
	require.Equal(t, http.StatusBadRequest, status, "%v", body)
	status, body = a.do("PATCH", "/v1/environments/"+envID, bob, map[string]any{"promote_from": []string{"nope"}})
	require.Equal(t, http.StatusBadRequest, status, "%v", body)
	status, body = a.do("PATCH", "/v1/environments/"+envID, bob, map[string]any{"max_role": "read", "deploy_policy": "promote-only"})
	require.Equal(t, http.StatusOK, status, "%v", body)
	settings = body["environment"].(map[string]any)["settings"].(map[string]any)
	require.Equal(t, "read", settings["max_role"])
	require.Equal(t, "promote-only", settings["deploy_policy"])
	status, body = a.do("PATCH", "/v1/environments/"+envID, root, map[string]any{"priority": "high"})
	require.Equal(t, http.StatusOK, status, "%v", body)
	require.Equal(t, "high", body["environment"].(map[string]any)["settings"].(map[string]any)["priority"])
	// Lowering is the environment admin's.
	status, body = a.do("PATCH", "/v1/environments/"+envID, bob, map[string]any{"priority": "normal"})
	require.Equal(t, http.StatusOK, status, "%v", body)

	// A high environment created by an admin starts read-capped with
	// promote-only suggested by the client; only the ceiling is a default.
	status, body = a.do("POST", "/v1/projects/"+projectID+"/environments", root, map[string]any{"name": "prod2", "priority": "high"})
	require.Equal(t, http.StatusCreated, status, "%v", body)
	settings = body["environment"].(map[string]any)["settings"].(map[string]any)
	require.Equal(t, "read", settings["max_role"])
	require.Equal(t, "high", settings["priority"])
}

// The deploy/maintain boundary on plan and open: an unchanged definition is
// a deploy, a changed or first one, staged values, and pruning are
// maintain; promotion needs read on the source.
func TestDeployRequiredRole(t *testing.T) {
	a := newTestAPI(t)
	a.createUser("owner@example.com", "hunter2hunter2")
	a.createMember("dev@example.com", "hunter2hunter2")
	owner := a.login("owner@example.com", "hunter2hunter2")
	dev := a.login("dev@example.com", "hunter2hunter2")
	projectID, prodID := a.createEnvironment(t, owner)
	status, body := a.do("POST", "/v1/projects/"+projectID+"/environments", owner, map[string]any{"name": "staging"})
	require.Equal(t, http.StatusCreated, status, "%v", body)
	stagingID := body["environment"].(map[string]any)["id"].(string)
	a.grantMember(t, projectID, "dev@example.com", "deploy")
	a.setCell(t, stagingID, "dev@example.com", "none")

	definitionVersion := a.submitDefinition(t, owner, projectID, deployAPIManifest)
	candidate := a.stageValues(t, owner, prodID, definitionVersion, "required-role-secret")

	// First deploy: maintain.
	status, body = a.do("POST", "/v1/environments/"+prodID+"/plan", dev, map[string]any{
		"definition_version_id": definitionVersion, "builds": buildsPayload(),
	})
	require.Equal(t, http.StatusForbidden, status, "%v", body)
	require.Contains(t, errorMessage(t, body), "maintain on environment production required: this is the first deploy")
	status, body = a.do("POST", "/v1/environments/"+prodID+"/plan", owner, map[string]any{
		"definition_version_id": definitionVersion, "candidate_id": candidate, "builds": buildsPayload(),
	})
	require.Equal(t, http.StatusOK, status, "%v", body)
	require.Equal(t, "maintain", body["required_role"])
	a.deployAndActivate(t, owner, prodID, definitionVersion, candidate)

	// Unchanged definition: deploy suffices (plan and open).
	status, body = a.do("POST", "/v1/environments/"+prodID+"/plan", dev, map[string]any{
		"definition_version_id": definitionVersion, "builds": buildsPayload(),
	})
	require.Equal(t, http.StatusOK, status, "%v", body)
	require.Equal(t, "deploy", body["required_role"])
	status, body = a.do("POST", "/v1/environments/"+prodID+"/deployments", dev, map[string]any{
		"definition_version_id": definitionVersion, "builds": buildsPayload(),
	})
	require.Contains(t, []int{http.StatusOK, http.StatusCreated}, status, "%v", body)
	require.Equal(t, "deploy", body["required_role"])
	if status == http.StatusCreated {
		runID := body["deployment"].(map[string]any)["run_id"].(string)
		status, _ = a.do("POST", "/v1/runs/"+runID+"/cancel", dev, nil)
		require.Equal(t, http.StatusOK, status)
	}

	// Staged values and pruning raise to maintain.
	status, body = a.do("POST", "/v1/environments/"+prodID+"/plan", dev, map[string]any{
		"definition_version_id": definitionVersion, "candidate_id": candidate, "builds": buildsPayload(),
	})
	require.Equal(t, http.StatusForbidden, status, "%v", body)
	require.Contains(t, errorMessage(t, body), "stages values")
	status, body = a.do("POST", "/v1/environments/"+prodID+"/plan", dev, map[string]any{
		"definition_version_id": definitionVersion, "builds": buildsPayload(), "prune_values": true,
	})
	require.Equal(t, http.StatusForbidden, status, "%v", body)
	require.Contains(t, errorMessage(t, body), "prunes values")

	// A changed definition raises to maintain.
	changed := a.submitDefinition(t, owner, projectID, deployAPIManifestNoVolume)
	status, body = a.do("POST", "/v1/environments/"+prodID+"/plan", dev, map[string]any{
		"definition_version_id": changed, "builds": buildsPayload(),
	})
	require.Equal(t, http.StatusForbidden, status, "%v", body)
	require.Contains(t, errorMessage(t, body), "changes the definition")

	// Promotion from a locked source is refused; from a readable one it is
	// a deploy.
	status, body = a.do("POST", "/v1/environments/"+prodID+"/plan", dev, map[string]any{
		"from_environment_id": stagingID,
	})
	require.Equal(t, http.StatusForbidden, status, "%v", body)
	require.Contains(t, errorMessage(t, body), "read on environment staging required")
	a.setCell(t, stagingID, "dev@example.com", "read")
	status, body = a.do("POST", "/v1/environments/"+prodID+"/plan", dev, map[string]any{
		"from_environment_id": stagingID,
	})
	// Past the role gate; staging has nothing active, which is the next
	// gate's verdict.
	require.Equal(t, http.StatusConflict, status, "%v", body)
	require.Contains(t, errorMessage(t, body), "no active revision")
	// A source outside the grant does not exist.
	status, _ = a.do("POST", "/v1/environments/"+prodID+"/plan", dev, map[string]any{
		"from_environment_id": "11111111-1111-4111-8111-111111111111",
	})
	require.Equal(t, http.StatusNotFound, status)
}

// Registry scope follows membership: deployers push, members pull,
// non-members see no repository, the cache follows deployer-anywhere.
func TestRegistryScopeFollowsMembership(t *testing.T) {
	a := newTestAPI(t)
	a.createUser("owner@example.com", "hunter2hunter2")
	a.createMember("reader@example.com", "hunter2hunter2")
	a.createMember("deployer@example.com", "hunter2hunter2")
	a.createMember("stranger@example.com", "hunter2hunter2")
	owner := a.login("owner@example.com", "hunter2hunter2")
	reader := a.login("reader@example.com", "hunter2hunter2")
	deployer := a.login("deployer@example.com", "hunter2hunter2")
	stranger := a.login("stranger@example.com", "hunter2hunter2")
	projectID, envID := a.createEnvironment(t, owner)
	a.grantMember(t, projectID, "reader@example.com", "read")
	a.grantMember(t, projectID, "deployer@example.com", "read")
	a.setCell(t, envID, "deployer@example.com", "deploy")

	scopes := []string{"repository:skali/demo/web:push,pull", "repository:cache/docker.io/library/nginx:push,pull"}
	status, body := a.exchangeToken(scopes, "owner@example.com", owner)
	require.Equal(t, http.StatusOK, status)
	require.Equal(t, map[string][]string{
		"skali/demo/web":                {"push", "pull"},
		"cache/docker.io/library/nginx": {"push", "pull"},
	}, tokenActions(t, body))

	status, body = a.exchangeToken(scopes, "deployer@example.com", deployer)
	require.Equal(t, http.StatusOK, status)
	require.Equal(t, map[string][]string{
		"skali/demo/web":                {"push", "pull"},
		"cache/docker.io/library/nginx": {"push", "pull"},
	}, tokenActions(t, body))

	status, body = a.exchangeToken(scopes, "reader@example.com", reader)
	require.Equal(t, http.StatusOK, status)
	require.Equal(t, map[string][]string{"skali/demo/web": {"pull"}}, tokenActions(t, body))

	status, body = a.exchangeToken(scopes, "stranger@example.com", stranger)
	require.Equal(t, http.StatusOK, status)
	require.Empty(t, tokenActions(t, body))
}

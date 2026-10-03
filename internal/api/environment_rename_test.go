package api

import (
	"net/http"
	"testing"

	"github.com/stretchr/testify/require"
)

func TestEnvironmentRenameAPI(t *testing.T) {
	a := newTestAPI(t)
	a.createUser("owner@example.com", "hunter2hunter2")
	a.createMember("member@example.com", "hunter2hunter2")
	owner := a.login("owner@example.com", "hunter2hunter2")
	member := a.login("member@example.com", "hunter2hunter2")
	projectID, envID := a.createEnvironment(t, owner)
	status, body := a.do("PUT", "/v1/projects/"+projectID+"/members/member@example.com", owner, map[string]string{"role": "read"})
	require.Equal(t, http.StatusOK, status, "%v", body)
	status, body = a.do("PATCH", "/v1/environments/"+envID, member, map[string]string{"name": "kilohertz"})
	require.Equal(t, http.StatusForbidden, status, "%v", body)
	status, body = a.do("PATCH", "/v1/environments/"+envID, owner, map[string]string{"name": "kilohertz", "priority": "normal"})
	require.Equal(t, http.StatusBadRequest, status, "%v", body)
	status, body = a.do("PATCH", "/v1/environments/"+envID, owner, map[string]string{"name": "kilohertz"})
	require.Equal(t, http.StatusOK, status, "%v", body)
	env := body["environment"].(map[string]any)
	require.Equal(t, envID, env["id"])
	require.Equal(t, "kilohertz", env["name"])
	require.Equal(t, []any{"production"}, env["previous_names"])
	status, body = a.do("POST", "/v1/projects/"+projectID+"/environments", owner, map[string]string{"name": "production"})
	require.Equal(t, http.StatusConflict, status, "%v", body)
}

package api

import (
	"context"
	"net/http"
	"testing"

	"github.com/stretchr/testify/require"
)

// corruptRevisionSchema rewrites a stored revision as if an incompatible
// build had written it: foreign schema_version column and document stamp.
func (a *testAPI) corruptRevisionSchema(t *testing.T, revisionID string) {
	t.Helper()
	_, err := a.st.Pool.Exec(context.Background(),
		`UPDATE revisions SET schema_version = '99',
		 document = jsonb_set(document, '{schemaVersion}', '"99"')
		 WHERE id = $1`, revisionID)
	require.NoError(t, err)
}

// Stored documents written under another schema version fail loudly with
// unsupported_schema instead of opaque 500s, on every read and pointer-move
// surface.
func TestStaleSchemaFailsLoudly(t *testing.T) {
	a := newTestAPI(t)
	a.createUser("schema@example.com", "hunter2hunter2")
	token := a.login("schema@example.com", "hunter2hunter2")
	projectID, envID := a.createEnvironment(t, token)

	definitionVersion := a.submitDefinition(t, token, projectID, deployAPIManifest)
	candidate := a.stageValues(t, token, envID, definitionVersion, "schema-one-value")
	first := a.deployAndActivate(t, token, envID, definitionVersion, candidate)
	candidate = a.stageValues(t, token, envID, definitionVersion, "schema-two-value")
	second := a.deployAndActivate(t, token, envID, definitionVersion, candidate)
	_ = projectID

	// A stale non-target revision: reading it and targeting it both refuse.
	a.corruptRevisionSchema(t, first)
	status, body := a.do("GET", "/v1/revisions/"+first, token, nil)
	require.Equal(t, http.StatusConflict, status, "%v", body)
	require.Equal(t, "unsupported_schema", errCode(body))
	status, body = a.do("PUT", "/v1/environments/"+envID+"/target", token,
		map[string]any{"revision_id": first})
	require.Equal(t, http.StatusConflict, status, "%v", body)
	require.Equal(t, "unsupported_schema", errCode(body))
	require.Contains(t, errMessage(body), "redeploy the environment")

	// The healthy target keeps the status endpoint working.
	status, body = a.do("GET", "/v1/environments/"+envID+"/status", token, nil)
	require.Equal(t, http.StatusOK, status, "%v", body)

	// A stale target revision turns status into a clear 409, never a 500.
	a.corruptRevisionSchema(t, second)
	status, body = a.do("GET", "/v1/environments/"+envID+"/status", token, nil)
	require.Equal(t, http.StatusConflict, status, "%v", body)
	require.Equal(t, "unsupported_schema", errCode(body))
}

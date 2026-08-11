package api

import (
	"context"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/go-chi/chi/v5"
	"github.com/google/uuid"
	"github.com/stretchr/testify/require"

	"github.com/Hinkolas/skali/internal/claim"
	"github.com/Hinkolas/skali/internal/dbstore"
)

const appEnvManifest = `version: "1"
name: demo
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
      GREETING: "hello"
      DATABASE_URL: "{{ databases.data.url }}"
      DB_HOST: "{{ databases.data.host }}"
databases:
  data:
    engine: postgres
    version: 17
`

func TestApplicationEnvironmentResolved(t *testing.T) {
	a := newTestAPI(t)
	a.createUser("appenv@example.com", "hunter2hunter2")
	token := a.login("appenv@example.com", "hunter2hunter2")
	projectID, environmentID := a.createEnvironment(t, token)
	ctx := context.Background()
	path := "/v1/environments/" + environmentID + "/applications/web/environment"

	// No active revision yet: 409.
	status, _ := a.do("GET", path, token, nil)
	require.Equal(t, http.StatusConflict, status)

	definitionVersion := a.submitDefinition(t, token, projectID, appEnvManifest)
	candidate := a.stageValues(t, token, environmentID, definitionVersion, "appenv-plant-value")
	a.deployAndActivate(t, token, environmentID, definitionVersion, candidate)

	// Unknown application key: 404. Bad audience: 400. No session: 401.
	status, _ = a.do("GET", "/v1/environments/"+environmentID+"/applications/ghost/environment", token, nil)
	require.Equal(t, http.StatusNotFound, status)
	status, _ = a.do("GET", path+"?audience=public", token, nil)
	require.Equal(t, http.StatusBadRequest, status)
	status, _ = a.do("GET", path, "", nil)
	require.Equal(t, http.StatusUnauthorized, status)

	// Unprovisioned database: the literal and value resolve, the two
	// database variables are omitted with warnings.
	status, body := a.do("GET", path, token, nil)
	require.Equal(t, http.StatusOK, status, "%v", body)
	values := body["values"].(map[string]any)
	require.Equal(t, "hello", values["GREETING"])
	require.Equal(t, "appenv-plant-value", values["SESSION_SECRET"])
	require.NotContains(t, values, "DATABASE_URL")
	warnings := body["warnings"].([]any)
	require.NotEmpty(t, warnings)
	require.Contains(t, warnings[0], "databases.data")

	// Provision the claim with a tenant on a pool holding a loopback port.
	db := dbstore.New(a.st)
	owner := dbstore.ServiceOwner(uuid.MustParse(projectID), uuid.MustParse(environmentID),
		"demo", "production", "data")
	row, err := db.EnsureClaim(ctx, owner, dbstore.ClaimSpec{
		Engine: "postgres", Major: 17, Isolation: "shared", Availability: "single",
	})
	require.NoError(t, err)
	pool, err := db.CreateCluster(ctx, dbstore.ClusterInput{
		Name: "pg17-shared", Engine: "postgres", Major: 17, Class: dbstore.ClassShared,
		Instances: 1, StorageBytes: 1 << 30, Image: "img",
	})
	require.NoError(t, err)
	_, err = db.BindClaim(ctx, row.ID, pool.ID)
	require.NoError(t, err)
	_, err = db.RecordTenant(ctx, dbstore.TenantInput{
		ClaimID: row.ID, ClusterID: pool.ID,
		DatabaseName: "db_data", RoleName: "u_data",
		CredentialSecret: "dbcred-x", Host: "pg17-shared-rw.skali-platform.svc", Port: 5432,
	})
	require.NoError(t, err)
	_, err = db.TransitionClaim(ctx, row.ID, claim.PhaseProvisioned)
	require.NoError(t, err)
	nodePort, err := db.AllocateClusterNodePort(ctx, pool.ID, 30501, 30509)
	require.NoError(t, err)
	require.Equal(t, 30501, nodePort)

	// Internal audience: cluster addresses, credentials from the fake read.
	status, body = a.do("GET", path, token, nil)
	require.Equal(t, http.StatusOK, status, "%v", body)
	values = body["values"].(map[string]any)
	require.Equal(t, "pg17-shared-rw.skali-platform.svc", values["DB_HOST"])
	require.Equal(t,
		"postgresql://u_dbcred-x:test-password-dbcred-x@pg17-shared-rw.skali-platform.svc:5432/db_data",
		values["DATABASE_URL"])
	require.Empty(t, body["warnings"])

	// Local audience: loopback rewrite through the identity mapping.
	status, body = a.do("GET", path+"?audience=local", token, nil)
	require.Equal(t, http.StatusOK, status, "%v", body)
	values = body["values"].(map[string]any)
	require.Equal(t, "127.0.0.1", values["DB_HOST"])
	require.Equal(t,
		"postgresql://u_dbcred-x:test-password-dbcred-x@127.0.0.1:30501/db_data",
		values["DATABASE_URL"])

	// An overridden port base shifts the host port by the same offset.
	status, body = a.do("GET", path+"?audience=local&port_base=45000", token, nil)
	require.Equal(t, http.StatusOK, status, "%v", body)
	values = body["values"].(map[string]any)
	require.Equal(t,
		"postgresql://u_dbcred-x:test-password-dbcred-x@127.0.0.1:45000/db_data",
		values["DATABASE_URL"])
}

// The local audience is a local-platform capability; managed installations
// refuse it before touching any state.
func TestApplicationEnvironmentLocalRejectedOnManaged(t *testing.T) {
	t.Parallel()
	handler := &appEnvHandlers{managed: true}
	router := chi.NewRouter()
	router.Get("/v1/environments/{id}/applications/{key}/environment", handler.resolved)
	recorder := httptest.NewRecorder()
	router.ServeHTTP(recorder, httptest.NewRequest("GET",
		"/v1/environments/"+uuid.NewString()+"/applications/web/environment?audience=local", nil))
	require.Equal(t, http.StatusConflict, recorder.Code)
}

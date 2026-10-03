package api

import (
	"context"
	"net/http"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/stretchr/testify/require"

	"github.com/Hinkolas/skali/internal/claim"
	"github.com/Hinkolas/skali/internal/dbstore"
)

func TestDatabaseConnectionAndReveal(t *testing.T) {
	a := newTestAPI(t)
	a.createUser("db@example.com", "hunter2hunter2")
	token := a.login("db@example.com", "hunter2hunter2")
	projectID, environmentID := a.createEnvironment(t, token)
	ctx := context.Background()

	// No live claim: 404.
	status, _ := a.do("GET", "/v1/environments/"+environmentID+"/databases/data/connection", token, nil)
	require.Equal(t, http.StatusNotFound, status)

	// A pending claim projects its phase without connection identity.
	db := dbstore.New(a.st)
	owner := dbstore.ServiceOwner(uuid.MustParse(projectID), uuid.MustParse(environmentID),
		"demo", "production", "data")
	row, err := db.EnsureClaim(ctx, owner, dbstore.ClaimSpec{
		Engine: "postgres", Major: 17, Isolation: "shared", Availability: "single",
	})
	require.NoError(t, err)
	status, body := a.do("GET", "/v1/environments/"+environmentID+"/databases/data/connection", token, nil)
	require.Equal(t, http.StatusOK, status)
	require.Equal(t, "pending", body["phase"])
	require.Nil(t, body["host"])

	// Reveal refuses before provisioning.
	status, _ = a.do("POST", "/v1/environments/"+environmentID+"/databases/data/credentials/reveal", token, nil)
	require.Equal(t, http.StatusConflict, status)

	// Provisioned: connection identity from rows, credentials from the
	// (fake) Secret read.
	pool, err := db.CreateCluster(ctx, dbstore.ClusterInput{
		Name: "pg17-shared", Engine: "postgres", Major: 17, Class: dbstore.ClassShared,
		Instances: 1, StorageBytes: 1 << 30, Image: "img",
	})
	require.NoError(t, err)
	_, err = db.BindClaim(ctx, row.ID, pool.ID)
	require.NoError(t, err)
	tenant, err := db.RecordTenant(ctx, dbstore.TenantInput{
		ClaimID: row.ID, ClusterID: pool.ID,
		DatabaseName: "db_data", RoleName: "u_data",
		CredentialSecret: "dbcred-x", Host: "pg17-shared-rw.skali-platform.svc", Port: 5432,
	})
	require.NoError(t, err)
	_, err = db.TransitionClaim(ctx, row.ID, claim.PhaseProvisioned)
	require.NoError(t, err)

	status, body = a.do("GET", "/v1/environments/"+environmentID+"/databases/data/connection", token, nil)
	require.Equal(t, http.StatusOK, status)
	require.Equal(t, "provisioned", body["phase"])
	require.Equal(t, tenant.Host, body["host"])
	require.Equal(t, "db_data", body["database"])
	require.EqualValues(t, 1, body["credential_version"])

	status, body = a.do("POST", "/v1/environments/"+environmentID+"/databases/data/credentials/reveal", token, nil)
	require.Equal(t, http.StatusOK, status)
	require.Equal(t, "u_dbcred-x", body["username"])
	require.Equal(t, "test-password-dbcred-x", body["password"])
	require.Contains(t, body["url"], "postgresql://u_dbcred-x:test-password-dbcred-x@")

	// Both endpoints require a session.
	status, _ = a.do("GET", "/v1/environments/"+environmentID+"/databases/data/connection", "", nil)
	require.Equal(t, http.StatusUnauthorized, status)
	status, _ = a.do("POST", "/v1/environments/"+environmentID+"/databases/data/credentials/reveal", "", nil)
	require.Equal(t, http.StatusUnauthorized, status)
}

// Rotation: refused without a live claim and before provisioning, with a
// window outside its bounds, and while the previous login role of an
// earlier rotation is still retiring; accepted as a journaled run on a
// provisioned database; sudo-gated like reveal.
func TestDatabaseCredentialRotation(t *testing.T) {
	a := newTestAPI(t)
	a.createUser("dbrotate@example.com", "hunter2hunter2")
	token := a.login("dbrotate@example.com", "hunter2hunter2")
	projectID, environmentID := a.createEnvironment(t, token)
	ctx := context.Background()
	rotate := "/v1/environments/" + environmentID + "/databases/data/credentials/rotate"
	connection := "/v1/environments/" + environmentID + "/databases/data/connection"

	status, body := a.do("POST", rotate, token, map[string]any{})
	require.Equal(t, http.StatusNotFound, status)
	require.Equal(t, "not_found", errorCode(t, body))

	db := dbstore.New(a.st)
	owner := dbstore.ServiceOwner(uuid.MustParse(projectID), uuid.MustParse(environmentID),
		"demo", "production", "data")
	row, err := db.EnsureClaim(ctx, owner, dbstore.ClaimSpec{
		Engine: "postgres", Major: 17, Isolation: "shared", Availability: "single",
	})
	require.NoError(t, err)
	status, body = a.do("POST", rotate, token, map[string]any{})
	require.Equal(t, http.StatusConflict, status)
	require.Equal(t, "database_not_provisioned", errorCode(t, body))

	pool, err := db.CreateCluster(ctx, dbstore.ClusterInput{
		Name: "pg17-shared", Engine: "postgres", Major: 17, Class: dbstore.ClassShared,
		Instances: 1, StorageBytes: 1 << 30, Image: "img",
	})
	require.NoError(t, err)
	_, err = db.BindClaim(ctx, row.ID, pool.ID)
	require.NoError(t, err)
	tenant, err := db.RecordTenant(ctx, dbstore.TenantInput{
		ClaimID: row.ID, ClusterID: pool.ID,
		DatabaseName: "db_data", RoleName: "u_data",
		CredentialSecret: "dbcred-x", Host: "pg17-shared-rw.skali-platform.svc", Port: 5432,
	})
	require.NoError(t, err)
	_, err = db.TransitionClaim(ctx, row.ID, claim.PhaseProvisioned)
	require.NoError(t, err)

	// The window has bounds.
	status, body = a.do("POST", rotate, token, map[string]any{"retire_after_seconds": 30})
	require.Equal(t, http.StatusBadRequest, status)
	require.Equal(t, "bad_request", errorCode(t, body))
	status, body = a.do("POST", rotate, token, map[string]any{"retire_after_seconds": 8 * 24 * 3600})
	require.Equal(t, http.StatusBadRequest, status)
	require.Equal(t, "bad_request", errorCode(t, body))

	// Accepted: a running run of kind rotation in the environment.
	status, body = a.do("POST", rotate, token, map[string]any{"retire_after_seconds": 3600})
	require.Equal(t, http.StatusAccepted, status, "body: %v", body)
	runID := body["run_id"].(string)
	status, body = a.do("GET", "/v1/runs/"+runID, token, nil)
	require.Equal(t, http.StatusOK, status)
	run := body["run"].(map[string]any)
	require.Equal(t, "rotation", run["kind"])
	require.Equal(t, "running", run["status"])

	// While it runs the slot is held.
	status, body = a.do("POST", rotate, token, nil)
	require.Equal(t, http.StatusConflict, status)
	require.Equal(t, "run_in_flight", errorCode(t, body))

	// The connection projection carries the overlap deadline once the
	// worker has taken the new login role, and nothing before.
	status, body = a.do("GET", connection, token, nil)
	require.Equal(t, http.StatusOK, status)
	require.Nil(t, body["credential_retire_at"])
	retireAt := time.Now().Add(time.Hour).UTC().Truncate(time.Second)
	set, err := db.SetTenantPendingCredential(ctx, tenant.ID, "u_data_v2", "dbcred-x-v2", retireAt)
	require.NoError(t, err)
	require.True(t, set)
	status, body = a.do("GET", connection, token, nil)
	require.Equal(t, http.StatusOK, status)
	require.EqualValues(t, 1, body["credential_version"])
	require.Nil(t, body["credential_retire_at"], "a pending role is not a retiring one")
	took, err := db.BeginTenantCredentialRotation(ctx, tenant.ID)
	require.NoError(t, err)
	require.True(t, took)
	status, body = a.do("GET", connection, token, nil)
	require.Equal(t, http.StatusOK, status)
	require.EqualValues(t, 2, body["credential_version"])
	projected, err := time.Parse(time.RFC3339, body["credential_retire_at"].(string))
	require.NoError(t, err)
	require.True(t, projected.Equal(retireAt), "got %s", projected)

	// Sudo-gated: a stale session is refused before any validation.
	a.staleAllSessions()
	status, body = a.do("POST", rotate, token, map[string]any{})
	require.Equal(t, http.StatusForbidden, status)
	require.Equal(t, "reauth_required", errorCode(t, body))
	status, _ = a.do("POST", rotate, "", nil)
	require.Equal(t, http.StatusUnauthorized, status)
}

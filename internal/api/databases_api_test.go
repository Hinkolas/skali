package api

import (
	"context"
	"net/http"
	"testing"

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

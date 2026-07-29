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

func TestBucketConnectionAndReveal(t *testing.T) {
	a := newTestAPI(t)
	a.createUser("bucket@example.com", "hunter2hunter2")
	token := a.login("bucket@example.com", "hunter2hunter2")
	projectID, environmentID := a.createEnvironment(t, token)
	ctx := context.Background()

	// No live claim: 404.
	status, _ := a.do("GET", "/v1/environments/"+environmentID+"/buckets/files/connection", token, nil)
	require.Equal(t, http.StatusNotFound, status)

	// A pending claim projects its phase without connection identity.
	db := dbstore.New(a.st)
	owner := dbstore.ServiceOwner(uuid.MustParse(projectID), uuid.MustParse(environmentID),
		"demo", "production", "files")
	row, err := db.EnsureBucketClaim(ctx, owner, dbstore.BucketSpec{
		Visibility: "private", StorageQuotaBytes: 1 << 30, Versioning: "disabled",
	})
	require.NoError(t, err)
	status, body := a.do("GET", "/v1/environments/"+environmentID+"/buckets/files/connection", token, nil)
	require.Equal(t, http.StatusOK, status)
	require.Equal(t, "pending", body["phase"])
	require.Nil(t, body["endpoint"])

	// Reveal refuses before provisioning.
	status, _ = a.do("POST", "/v1/environments/"+environmentID+"/buckets/files/credentials/reveal", token, nil)
	require.Equal(t, http.StatusConflict, status)

	// Provisioned: connection identity from rows, credentials from the
	// (fake) Secret read.
	sw, err := db.CreateObjectStore(ctx, dbstore.StoreInput{
		Name: "seaweed", Masters: 1, VolumeServers: 1,
		Replication: "000", VolumeStorageBytes: 1 << 30, Image: "img",
	})
	require.NoError(t, err)
	allocation, err := db.RecordAllocation(ctx, dbstore.AllocationInput{
		ClaimID: row.ID, StoreID: sw.ID, BucketName: "b-files-01",
		AccessKeyID: "AKFILES", CredentialSecret: "s3cred-x",
		Endpoint: "http://seaweed-s3.skali-platform.svc.cluster.local:8333",
		Region:   "us-east-1",
	})
	require.NoError(t, err)
	_, err = db.TransitionBucketClaim(ctx, row.ID, claim.PhaseProvisioned)
	require.NoError(t, err)

	status, body = a.do("GET", "/v1/environments/"+environmentID+"/buckets/files/connection", token, nil)
	require.Equal(t, http.StatusOK, status)
	require.Equal(t, "provisioned", body["phase"])
	require.Equal(t, allocation.Endpoint, body["endpoint"])
	require.Equal(t, "b-files-01", body["bucket"])
	require.Equal(t, "us-east-1", body["region"])
	require.EqualValues(t, 1, body["credential_version"])

	status, body = a.do("POST", "/v1/environments/"+environmentID+"/buckets/files/credentials/reveal", token, nil)
	require.Equal(t, http.StatusOK, status)
	require.Equal(t, "AKs3cred-x", body["access_key"])
	require.Equal(t, "sk-s3cred-x", body["secret_key"])

	// Both endpoints require a session.
	status, _ = a.do("GET", "/v1/environments/"+environmentID+"/buckets/files/connection", "", nil)
	require.Equal(t, http.StatusUnauthorized, status)
	status, _ = a.do("POST", "/v1/environments/"+environmentID+"/buckets/files/credentials/reveal", "", nil)
	require.Equal(t, http.StatusUnauthorized, status)
}

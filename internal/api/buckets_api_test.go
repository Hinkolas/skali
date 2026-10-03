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

// Rotation: refused without a live claim, before provisioning, and with a
// window outside its bounds; accepted as a journaled run on a provisioned
// bucket; sudo-gated like reveal.
func TestBucketCredentialRotation(t *testing.T) {
	a := newTestAPI(t)
	a.createUser("bucket@example.com", "hunter2hunter2")
	token := a.login("bucket@example.com", "hunter2hunter2")
	projectID, environmentID := a.createEnvironment(t, token)
	ctx := context.Background()
	rotate := "/v1/environments/" + environmentID + "/buckets/files/credentials/rotate"

	status, body := a.do("POST", rotate, token, map[string]any{})
	require.Equal(t, http.StatusNotFound, status)
	require.Equal(t, "not_found", errorCode(t, body))

	db := dbstore.New(a.st)
	owner := dbstore.ServiceOwner(uuid.MustParse(projectID), uuid.MustParse(environmentID),
		"demo", "production", "files")
	row, err := db.EnsureBucketClaim(ctx, owner, dbstore.BucketSpec{
		Visibility: "private", StorageQuotaBytes: 1 << 30, Versioning: "disabled",
	})
	require.NoError(t, err)
	status, body = a.do("POST", rotate, token, map[string]any{})
	require.Equal(t, http.StatusConflict, status)
	require.Equal(t, "bucket_not_provisioned", errorCode(t, body))

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

	// The window has bounds.
	status, body = a.do("POST", rotate, token, map[string]any{"retire_after_seconds": 30})
	require.Equal(t, http.StatusBadRequest, status)
	require.Equal(t, "bad_request", errorCode(t, body))
	status, body = a.do("POST", rotate, token, map[string]any{"retire_after_seconds": 8 * 24 * 3600})
	require.Equal(t, http.StatusBadRequest, status)
	require.Equal(t, "bad_request", errorCode(t, body))

	// A fenced bucket refuses.
	require.NoError(t, db.FenceAllocation(ctx, allocation.ID))
	status, body = a.do("POST", rotate, token, map[string]any{"retire_after_seconds": 3600})
	require.Equal(t, http.StatusConflict, status)
	require.Equal(t, "bucket_fenced", errorCode(t, body))
	require.NoError(t, db.UnfenceAllocation(ctx, allocation.ID))

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
	// substrate commits it.
	retireAt := time.Now().Add(time.Hour).UTC().Truncate(time.Second)
	committed, err := db.BeginAllocationCredentialRotation(ctx, allocation.ID, "AKNEW", retireAt)
	require.NoError(t, err)
	require.True(t, committed)
	status, body = a.do("GET", "/v1/environments/"+environmentID+"/buckets/files/connection", token, nil)
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

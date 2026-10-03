package substrate

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"reflect"
	"testing"
	"time"

	"github.com/minio/minio-go/v7"
	"github.com/minio/minio-go/v7/pkg/cors"
	"github.com/minio/minio-go/v7/pkg/credentials"
	"github.com/minio/minio-go/v7/pkg/lifecycle"
	"github.com/stretchr/testify/require"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"

	"github.com/Hinkolas/skali/internal/claim"
	"github.com/Hinkolas/skali/internal/dbstore"
	"github.com/Hinkolas/skali/internal/kubetest"
	"github.com/Hinkolas/skali/internal/store"
	"github.com/Hinkolas/skali/internal/substrate/seaweed"
)

// runBucketPermissions measures what a bucket's generated
// identity can do on the pinned engine, through signed S3 requests over a
// port-forward (the API server's proxy rewrites Host and path, which a
// SigV4 signature covers). It establishes the object-access boundary
// (own bucket only, nothing anonymous) and records the four facts the
// S3 hardening series (#68) builds on:
//
//  1. Bucket administration is separated from object access by the
//     policy Skali owns: the identity's Write stores objects, and its
//     attempts at policy, CORS, versioning, and lifecycle are refused
//     (on the pin the legacy Write action would admit them all).
//  2. Settings changed by another principal are drift: the next
//     configuration pass resets them and reports what it reset.
//  3. The filer's path read-only flag (the storage-quota mechanism)
//     blocks writes but leaves deletes open, so a bucket over quota can
//     be freed by its own application.
//  4. Removing the identity invalidates already-issued presigned URLs;
//     re-creating it with the same keypair revives them.
//
// Requires TEST_KUBECONFIG and TEST_DATABASE_URL.
func runBucketPermissions(t *testing.T, fixture *bucketLiveFixture) {
	ctx := context.Background()
	config, client, dbSvc := fixture.config, fixture.client, fixture.db
	controller := fixture.controller(nil)
	proj, env := fixture.newEnvironment(t, controller)

	// Two buckets in one environment: the boundary under test is between
	// them.
	files := driveLiveBucket(t, controller, dbSvc, proj, env, "files")
	other := driveLiveBucket(t, controller, dbSvc, proj, env, "other")

	credential, err := client.Clientset.CoreV1().Secrets(Namespace).
		Get(ctx, files.CredentialSecret, metav1.GetOptions{})
	require.NoError(t, err)
	accessKey, secretKey := string(credential.Data["access_key"]), string(credential.Data["secret_key"])

	endpoint := kubetest.PortForward(t, config, Namespace, "app="+seaweed.AllInOneApp, seaweed.S3Port)
	s3, err := minio.New(endpoint, &minio.Options{
		Transport:    liveS3Transport(t),
		Creds:        credentials.NewStaticV4(accessKey, secretKey, ""),
		Region:       seaweed.Region,
		BucketLookup: minio.BucketLookupPath,
	})
	require.NoError(t, err)
	anonymous := &http.Client{Timeout: 30 * time.Second}
	t.Cleanup(anonymous.CloseIdleConnections)
	objectURL := func(bucket, key string) string {
		return "http://" + endpoint + "/" + bucket + "/" + key
	}
	bucket, foreign := files.BucketName, other.BucketName
	payload := []byte("boundary")

	// The object-access boundary: own bucket read/write, the sibling
	// bucket refused, anonymous refused.
	_, err = s3.PutObject(ctx, bucket, "own", bytes.NewReader(payload), int64(len(payload)), minio.PutObjectOptions{})
	require.NoError(t, err)
	object, err := s3.GetObject(ctx, bucket, "own", minio.GetObjectOptions{})
	require.NoError(t, err)
	t.Cleanup(func() { _ = object.Close() })
	read, err := io.ReadAll(object)
	require.NoError(t, err)
	require.Equal(t, payload, read)
	_, err = s3.PutObject(ctx, foreign, "own", bytes.NewReader(payload), int64(len(payload)), minio.PutObjectOptions{})
	require.Equal(t, "AccessDenied", minio.ToErrorResponse(err).Code, "cross-bucket write: %v", err)
	foreignObject, err := s3.GetObject(ctx, foreign, "own", minio.GetObjectOptions{})
	if err == nil {
		// GetObject is lazy; the first read carries the verdict.
		t.Cleanup(func() { _ = foreignObject.Close() })
		_, err = io.ReadAll(foreignObject)
	}
	require.Equal(t, "AccessDenied", minio.ToErrorResponse(err).Code, "cross-bucket read: %v", err)
	require.Equal(t, http.StatusForbidden, status(t, anonymous, http.MethodGet, objectURL(bucket, "own"), nil, nil),
		"anonymous reads of a private bucket are refused")
	require.Equal(t, http.StatusForbidden, status(t, anonymous, http.MethodGet, "http://"+endpoint+"/"+bucket+"?list-type=2", nil, nil),
		"anonymous listing is refused")

	// Fact 3: the read-only path flag the quota probe sets. Writes must
	// stop; whether deletes stop too decides how "read-only until space
	// is freed" can be honoured.
	readOnly, err := controller.enforceBucketQuota(ctx, 1, bucket, 2, false)
	require.NoError(t, err)
	require.True(t, readOnly)
	requireEventually(t, time.Minute, func() bool {
		_, err := s3.PutObject(ctx, bucket, "quota", bytes.NewReader(payload), int64(len(payload)), minio.PutObjectOptions{})
		return err != nil
	}, "the read-only flag never blocked writes")
	require.NoError(t, s3.RemoveObject(ctx, bucket, "own", minio.RemoveObjectOptions{}),
		"fact 3: the read-only flag must leave deletes open, or space could never be freed")
	_, err = s3.StatObject(ctx, bucket, "own", minio.StatObjectOptions{})
	require.Equal(t, "NoSuchKey", minio.ToErrorResponse(err).Code, "the delete must have landed: %v", err)
	_, err = s3.PutObject(ctx, bucket, "own", bytes.NewReader(payload), int64(len(payload)), minio.PutObjectOptions{})
	require.Error(t, err, "writes stay blocked after a delete")
	_, err = controller.enforceBucketQuota(ctx, 0, bucket, 2, false)
	require.NoError(t, err)
	requireEventually(t, time.Minute, func() bool {
		_, err := s3.PutObject(ctx, bucket, "own", bytes.NewReader(payload), int64(len(payload)), minio.PutObjectOptions{})
		return err == nil
	}, "clearing the read-only flag never reopened writes")

	// Fact 4: a presigned URL is only as alive as the identity behind it.
	signed, err := s3.PresignedPutObject(ctx, bucket, "presigned", 10*time.Minute)
	require.NoError(t, err)
	require.Equal(t, http.StatusOK, status(t, anonymous, http.MethodPut, signed.String(), nil, payload))
	require.NoError(t, controller.deps.Seaweed.DeleteIdentity(ctx, bucket))
	requireEventually(t, time.Minute, func() bool {
		return status(t, anonymous, http.MethodPut, signed.String(), nil, payload) == http.StatusForbidden
	}, "removing the identity never invalidated its presigned URL")
	require.NoError(t, controller.deps.Seaweed.EnsureIdentity(ctx, seaweed.Identity{
		Name:        bucket,
		Credentials: []seaweed.Credential{{AccessKey: accessKey, SecretKey: secretKey}},
		Actions:     seaweed.BucketActions(bucket),
	}))
	requireEventually(t, time.Minute, func() bool {
		return status(t, anonymous, http.MethodPut, signed.String(), nil, payload) == http.StatusOK
	}, "restoring the identity with its keypair never revived the presigned URL")

	// Bucket administration is Skali's, not the identity's: the policy the
	// substrate owns denies every configuration write to the bucket's own
	// principal while the identity's Write keeps storing objects. The
	// legacy Write action would admit all of these on the pin.
	adminDenied := func(t *testing.T, what string, err error) {
		t.Helper()
		require.Equal(t, "AccessDenied", minio.ToErrorResponse(err).Code, "%s must be refused: %v", what, err)
	}
	adminDenied(t, "versioning", s3.SetBucketVersioning(ctx, bucket, minio.BucketVersioningConfiguration{Status: "Enabled"}))
	adminDenied(t, "cors", s3.SetBucketCors(ctx, bucket, cors.NewConfig([]cors.Rule{{
		AllowedOrigin: []string{"https://app.example"}, AllowedMethod: []string{"GET"},
	}})))
	rules := lifecycle.NewConfiguration()
	rules.Rules = []lifecycle.Rule{{ID: "abort", Status: "Enabled",
		AbortIncompleteMultipartUpload: lifecycle.AbortIncompleteMultipartUpload{DaysAfterInitiation: 1}}}
	adminDenied(t, "lifecycle", s3.SetBucketLifecycle(ctx, bucket, rules))
	publicRead := fmt.Sprintf(`{"Version":"2012-10-17","Statement":[{"Effect":"Allow","Principal":"*",
		"Action":["s3:GetObject"],"Resource":["arn:aws:s3:::%s/*"]}]}`, bucket)
	adminDenied(t, "policy", s3.SetBucketPolicy(ctx, bucket, publicRead))
	require.Equal(t, http.StatusForbidden, status(t, anonymous, http.MethodGet, objectURL(bucket, "own"), nil, nil),
		"the bucket stays private")
	_, err = s3.PutObject(ctx, bucket, "still-mine", bytes.NewReader(payload), int64(len(payload)), minio.PutObjectOptions{})
	require.NoError(t, err, "object access is untouched by the owned policy")
	policy, err := s3.GetBucketPolicy(ctx, bucket)
	require.NoError(t, err)
	require.True(t, samePolicyDocument(t, policy, seaweed.BucketPolicy(bucket)), "the identity reads the policy skali owns")

	// Drift repair: a setting changed by any other principal (here the
	// platform identity itself stands in for a misbehaving operator) is
	// reset on the next pass and reported.
	platform, err := client.Clientset.CoreV1().Secrets(Namespace).Get(ctx, PlatformCredentialSecret, metav1.GetOptions{})
	require.NoError(t, err)
	admin, err := minio.New(endpoint, &minio.Options{
		Transport: liveS3Transport(t),
		Creds:     credentials.NewStaticV4(string(platform.Data["access_key"]), string(platform.Data["secret_key"]), ""),
		Region:    seaweed.Region, BucketLookup: minio.BucketLookupPath,
	})
	require.NoError(t, err)
	require.NoError(t, admin.SetBucketCors(ctx, bucket, cors.NewConfig([]cors.Rule{{
		AllowedOrigin: []string{"https://app.example"}, AllowedMethod: []string{"GET"},
	}})))
	require.NoError(t, admin.SetBucketPolicy(ctx, bucket, publicRead))
	requireEventually(t, time.Minute, func() bool {
		return status(t, anonymous, http.MethodGet, objectURL(bucket, "own"), nil, nil) == http.StatusOK
	}, "the drifted policy never took effect (the probe would then have nothing to repair)")
	repaired, err := controller.deps.Seaweed.EnsureBucketConfiguration(ctx, bucket, seaweed.BucketPolicy(bucket), nil)
	require.NoError(t, err)
	require.ElementsMatch(t, []string{"policy", "cors"}, repaired)
	requireEventually(t, time.Minute, func() bool {
		return status(t, anonymous, http.MethodGet, objectURL(bucket, "own"), nil, nil) == http.StatusForbidden
	}, "repairing the policy never closed the bucket again")
	corsConfig, err := admin.GetBucketCors(ctx, bucket)
	require.NoError(t, err)
	require.True(t, corsConfig == nil || len(corsConfig.CORSRules) == 0, "the CORS drift was removed")
	repaired, err = controller.deps.Seaweed.EnsureBucketConfiguration(ctx, bucket, seaweed.BucketPolicy(bucket), nil)
	require.NoError(t, err)
	require.Empty(t, repaired, "a converged bucket is a read-only pass")
}

func samePolicyDocument(t *testing.T, a, b string) bool {
	t.Helper()
	var x, y any
	require.NoError(t, json.Unmarshal([]byte(a), &x))
	require.NoError(t, json.Unmarshal([]byte(b), &y))
	return reflect.DeepEqual(x, y)
}

// driveLiveBucket creates one service bucket claim and reconciles it (with
// the store and the metadata claim) until provisioned, returning the live
// allocation.
func driveLiveBucket(t *testing.T, controller *Controller, dbSvc *dbstore.Service,
	proj *store.Project, env *store.Environment, service string) *store.BucketAllocation {
	t.Helper()
	return driveLiveBucketWithQuota(t, controller, dbSvc, proj, env, service, 1<<30)
}

func driveLiveBucketWithQuota(t *testing.T, controller *Controller, dbSvc *dbstore.Service,
	proj *store.Project, env *store.Environment, service string, quota int64) *store.BucketAllocation {
	t.Helper()
	ctx := context.Background()
	owner := dbstore.ServiceOwner(proj.ID, env.ID, proj.Name, env.Name, service)
	created, err := dbSvc.EnsureBucketClaim(ctx, owner, dbstore.BucketSpec{
		Visibility: "private", StorageQuotaBytes: quota, Versioning: "disabled",
	})
	require.NoError(t, err)
	driveLive(t, "bucket provisioning "+service, 10*time.Minute, func(ctx context.Context) (bool, error) {
		return bucketClaimPass(ctx, controller, dbSvc, created.ID, claim.PhaseProvisioned, true)
	})
	allocation, err := dbSvc.LiveAllocation(ctx, created.ID)
	require.NoError(t, err)
	return allocation
}

// status performs one raw request (anonymous unless the URL is signed) and
// returns the status code.
func status(t *testing.T, client *http.Client, method, rawURL string, header http.Header, body []byte) int {
	t.Helper()
	var reader io.Reader
	if body != nil {
		reader = bytes.NewReader(body)
	}
	request, err := http.NewRequest(method, rawURL, reader)
	require.NoError(t, err)
	for name, values := range header {
		request.Header[name] = values
	}
	response, err := client.Do(request)
	require.NoError(t, err)
	defer response.Body.Close()
	_, _ = io.Copy(io.Discard, response.Body)
	return response.StatusCode
}

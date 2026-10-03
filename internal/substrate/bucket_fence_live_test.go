package substrate

import (
	"bytes"
	"context"
	"net/http"
	"strings"
	"testing"
	"time"

	"github.com/minio/minio-go/v7"
	"github.com/minio/minio-go/v7/pkg/credentials"
	"github.com/stretchr/testify/require"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"

	"github.com/Hinkolas/skali/internal/kubetest"
	"github.com/Hinkolas/skali/internal/store"
	"github.com/Hinkolas/skali/internal/substrate/seaweed"
)

// runBucketRestoreFence proves the restore fence against a real
// store: fencing deletes the bucket's identity (its keys and an already
// issued presigned URL are refused), lifts the quota flag, and leaves the
// platform identity writing; provisioning keeps the identity absent while
// fenced; the probe leaves a fence alone while the environment is down and
// lifts it once the environment moves on; unfencing brings the keypair
// back (which revives the presigned URL: the same key signs it). Requires
// TEST_KUBECONFIG and TEST_DATABASE_URL.
func runBucketRestoreFence(t *testing.T, fixture *bucketLiveFixture) {
	ctx := context.Background()
	config, client, dbSvc := fixture.config, fixture.client, fixture.db
	controller := fixture.controller(nil)
	proj, env := fixture.newEnvironment(t, controller)
	st := fixture.st

	files := driveLiveBucket(t, controller, dbSvc, proj, env, "files")
	bucket := files.BucketName
	claimRow, err := dbSvc.LiveServiceBucketClaim(ctx, env.ID, "files")
	require.NoError(t, err)

	credential, err := client.Clientset.CoreV1().Secrets(Namespace).
		Get(ctx, files.CredentialSecret, metav1.GetOptions{})
	require.NoError(t, err)
	endpoint := kubetest.PortForward(t, config, Namespace, "app="+seaweed.AllInOneApp, seaweed.S3Port)
	newClient := func(accessKey, secretKey string) *minio.Client {
		s3, err := minio.New(endpoint, &minio.Options{
			Transport:    liveS3Transport(t),
			Creds:        credentials.NewStaticV4(accessKey, secretKey, ""),
			Region:       seaweed.Region,
			BucketLookup: minio.BucketLookupPath,
		})
		require.NoError(t, err)
		return s3
	}
	app := newClient(string(credential.Data["access_key"]), string(credential.Data["secret_key"]))
	web := &http.Client{Timeout: 30 * time.Second}
	t.Cleanup(web.CloseIdleConnections)

	// The application wrote before the restore and holds a presigned URL.
	_, err = app.PutObject(ctx, bucket, "before", strings.NewReader("before"), 6, minio.PutObjectOptions{})
	require.NoError(t, err)
	signed, err := app.PresignedPutObject(ctx, bucket, "presigned", 10*time.Minute)
	require.NoError(t, err)
	require.Equal(t, http.StatusOK, status(t, web, http.MethodPut, signed.String(), nil, []byte("x")))
	// The bucket is full: the quota flag is up.
	readOnly, err := controller.enforceBucketQuota(ctx, 1, bucket, 2, false)
	require.NoError(t, err)
	require.True(t, readOnly)
	_, err = app.PutObject(ctx, bucket, "refused", strings.NewReader("x"), 1, minio.PutObjectOptions{})
	require.Error(t, err, "a full bucket refuses the application's writes")

	// A restore takes the environment down and fences the bucket.
	marked, err := st.MarkEnvironmentDown(ctx, env.ID)
	require.NoError(t, err)
	require.EqualValues(t, 1, marked)
	require.NoError(t, controller.FenceBucket(ctx, env.ID, "files"))
	fenced, err := dbSvc.LiveAllocation(ctx, claimRow.ID)
	require.NoError(t, err)
	require.NotNil(t, fenced.FencedAt)
	identities, err := controller.deps.Seaweed.Identities(ctx)
	require.NoError(t, err)
	require.Nil(t, identities.Find(bucket), "the fence deletes the bucket identity")
	require.Equal(t, http.StatusForbidden, status(t, web, http.MethodPut, signed.String(), nil, []byte("y")),
		"a presigned URL issued before the fence is refused during it")
	_, err = app.PutObject(ctx, bucket, "during", strings.NewReader("x"), 1, minio.PutObjectOptions{})
	require.Error(t, err, "the mirrored keys are refused during the fence")
	conf, err := controller.deps.Seaweed.Conf(ctx)
	require.NoError(t, err)
	if entry := conf.Find(seaweed.BucketsPrefix + bucket + "/"); entry != nil {
		require.False(t, entry.ReadOnly, "the fence lifts the quota flag for the restore's writes")
	}

	// The platform identity restores through the fence: it can clear and
	// rewrite the bucket the way the restore does.
	access, err := controller.PlatformBucketAccess(ctx, env.ID, "files")
	require.NoError(t, err)
	require.Equal(t, bucket, access.Bucket)
	require.Equal(t, InternalBucketEndpoint(), access.Endpoint)
	platform := newClient(access.AccessKey, access.SecretKey)
	require.NoError(t, platform.RemoveObject(ctx, bucket, "before", minio.RemoveObjectOptions{}))
	_, err = platform.PutObject(ctx, bucket, "restored", bytes.NewReader([]byte("restored")), 8,
		minio.PutObjectOptions{ContentType: "text/plain", UserMetadata: map[string]string{"Origin": "snapshot"}})
	require.NoError(t, err)

	// Provisioning passes while fenced leave the identity absent, and the
	// probe leaves the fence alone while the environment is down.
	_, err = controller.reconcileBucketClaim(ctx, claimRow.ID)
	require.NoError(t, err)
	identities, err = controller.deps.Seaweed.Identities(ctx)
	require.NoError(t, err)
	require.Nil(t, identities.Find(bucket), "provisioning must not recreate a fenced identity")
	require.NoError(t, controller.liftStaleFence(ctx, *claimRow, fenced))
	still, err := dbSvc.LiveAllocation(ctx, claimRow.ID)
	require.NoError(t, err)
	require.NotNil(t, still.FencedAt, "a fence on a down environment is not stale")

	// The restore completes: the identity is back with its keypair, the
	// fence is gone, and the presigned URL signed before it works again.
	require.NoError(t, controller.UnfenceBucket(ctx, env.ID, "files"))
	lifted, err := dbSvc.LiveAllocation(ctx, claimRow.ID)
	require.NoError(t, err)
	require.Nil(t, lifted.FencedAt)
	identities, err = controller.deps.Seaweed.Identities(ctx)
	require.NoError(t, err)
	require.NotNil(t, identities.Find(bucket))
	object, err := app.GetObject(ctx, bucket, "restored", minio.GetObjectOptions{})
	require.NoError(t, err)
	t.Cleanup(func() { _ = object.Close() })
	info, err := object.Stat()
	require.NoError(t, err)
	require.Equal(t, "text/plain", info.ContentType)
	require.Equal(t, http.StatusOK, status(t, web, http.MethodPut, signed.String(), nil, []byte("z")),
		"the same keypair signs the URL, so lifting the fence revives it")

	// A restore that failed leaves the fence up; a deploy that brings the
	// environment back finds the probe lifting it.
	require.NoError(t, controller.FenceBucket(ctx, env.ID, "files"))
	_, err = st.SetEnvironmentTarget(ctx, store.SetEnvironmentTargetParams{EnvironmentID: env.ID})
	require.NoError(t, err)
	fenced, err = dbSvc.LiveAllocation(ctx, claimRow.ID)
	require.NoError(t, err)
	require.NotNil(t, fenced.FencedAt)
	require.NoError(t, controller.liftStaleFence(ctx, *claimRow, fenced))
	lifted, err = dbSvc.LiveAllocation(ctx, claimRow.ID)
	require.NoError(t, err)
	require.Nil(t, lifted.FencedAt, "a fence whose environment is no longer down is lifted")
	identities, err = controller.deps.Seaweed.Identities(ctx)
	require.NoError(t, err)
	require.NotNil(t, identities.Find(bucket))
}

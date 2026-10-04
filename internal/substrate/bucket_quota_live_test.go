package substrate

import (
	"bytes"
	"context"
	"crypto/rand"
	"testing"
	"time"

	"github.com/minio/minio-go/v7"
	"github.com/minio/minio-go/v7/pkg/credentials"
	"github.com/stretchr/testify/require"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"

	"github.com/Hinkolas/skali/internal/kubetest"
	"github.com/Hinkolas/skali/internal/module"
	"github.com/Hinkolas/skali/internal/substrate/seaweed"
)

// runBucketQuota proves the storage quota's documented guarantees on
// a real store: an upload past the quota flips the bucket to refuse
// uploads (single and multipart) while deletes keep working, the usage
// reported is the live footprint with an entry count, and deleting the
// object reopens the bucket on the next observation without waiting for
// a vacuum. Requires TEST_KUBECONFIG and TEST_DATABASE_URL.
func runBucketQuota(t *testing.T, fixture *bucketLiveFixture) {
	ctx := context.Background()
	config, client, dbSvc := fixture.config, fixture.client, fixture.db
	controller := fixture.controller(nil)
	proj, env := fixture.newEnvironment(t, controller)

	const quota = int64(256 << 10)
	files := driveLiveBucketWithQuota(t, controller, dbSvc, proj, env, "files", quota)
	bucket := files.BucketName

	credential, err := client.Clientset.CoreV1().Secrets(Namespace).
		Get(ctx, files.CredentialSecret, metav1.GetOptions{})
	require.NoError(t, err)
	endpoint := kubetest.PortForward(t, config, Namespace, "app="+seaweed.AllInOneApp, seaweed.S3Port)
	app, err := minio.New(endpoint, &minio.Options{
		Transport:    liveS3Transport(t),
		Creds:        credentials.NewStaticV4(string(credential.Data["access_key"]), string(credential.Data["secret_key"]), ""),
		Region:       seaweed.Region,
		BucketLookup: minio.BucketLookupPath,
	})
	require.NoError(t, err)

	// Upkeep and probe are driven by hand: one upkeep pass enforces the
	// flag, the probe then reads the store and returns the bucket
	// projection.
	probe := controller.SeaweedProbe()
	observeBucket := func() *module.BucketStatus {
		controller.maintainStorage(ctx)
		objects, err := probe(ctx)
		require.NoError(t, err)
		for _, object := range objects {
			if object.Kind == module.KindBucket && object.Ref.Name == bucket {
				return object.Bucket
			}
		}
		return nil
	}
	awaitBucket := func(what string, want func(*module.BucketStatus) bool) *module.BucketStatus {
		t.Helper()
		var last *module.BucketStatus
		requireEventually(t, 2*time.Minute, func() bool {
			last = observeBucket()
			return last != nil && want(last)
		}, what)
		return last
	}

	// Under quota: open, live usage reported.
	small := make([]byte, 16<<10)
	_, _ = rand.Read(small)
	_, err = app.PutObject(ctx, bucket, "small", bytes.NewReader(small), int64(len(small)), minio.PutObjectOptions{})
	require.NoError(t, err)
	status := awaitBucket("the small object never showed in usage", func(b *module.BucketStatus) bool {
		return b.UsedBytes >= int64(len(small))
	})
	require.False(t, status.ReadOnly)
	require.EqualValues(t, quota, status.QuotaBytes)
	require.GreaterOrEqual(t, status.EntryCount, int64(1))
	require.GreaterOrEqual(t, status.DiskBytes, status.UsedBytes)

	// Over quota: the next observation refuses uploads, deletes stay open.
	big := make([]byte, 300<<10)
	_, _ = rand.Read(big)
	_, err = app.PutObject(ctx, bucket, "big", bytes.NewReader(big), int64(len(big)), minio.PutObjectOptions{})
	require.NoError(t, err, "the overshoot lands: enforcement is by observation, not per request")
	status = awaitBucket("the quota flag never rose", func(b *module.BucketStatus) bool { return b.ReadOnly })
	require.GreaterOrEqual(t, status.UsedBytes, quota)
	requireEventually(t, time.Minute, func() bool {
		_, err := app.PutObject(ctx, bucket, "refused", bytes.NewReader(small), int64(len(small)), minio.PutObjectOptions{})
		return err != nil
	}, "uploads were never refused")
	core := &minio.Core{Client: app}
	uploadID, err := core.NewMultipartUpload(ctx, bucket, "parts", minio.PutObjectOptions{})
	if err == nil {
		_, err = core.PutObjectPart(ctx, bucket, "parts", uploadID, 1, bytes.NewReader(small), int64(len(small)), minio.PutObjectPartOptions{})
		_ = core.AbortMultipartUpload(ctx, bucket, "parts", uploadID)
	}
	require.Error(t, err, "multipart uploads must be refused too")
	object, err := app.GetObject(ctx, bucket, "small", minio.GetObjectOptions{})
	require.NoError(t, err)
	t.Cleanup(func() { _ = object.Close() })
	_, err = object.Stat()
	require.NoError(t, err, "reads keep working")
	require.NoError(t, app.RemoveObject(ctx, bucket, "big", minio.RemoveObjectOptions{}), "deletes keep working")

	// Freed: the deleted bytes stop counting on the store's next heartbeat
	// and the flag lifts, no vacuum needed. The disk footprint may still
	// carry the garbage.
	status = awaitBucket("the quota flag never lifted after the delete", func(b *module.BucketStatus) bool { return !b.ReadOnly })
	require.Less(t, status.UsedBytes, quota)
	require.GreaterOrEqual(t, status.DiskBytes, status.UsedBytes)
	requireEventually(t, time.Minute, func() bool {
		_, err := app.PutObject(ctx, bucket, "again", bytes.NewReader(small), int64(len(small)), minio.PutObjectOptions{})
		return err == nil
	}, "uploads never reopened after space was freed")
}

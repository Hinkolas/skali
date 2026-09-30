package substrate

import (
	"bytes"
	"context"
	"net/http"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/minio/minio-go/v7"
	"github.com/minio/minio-go/v7/pkg/credentials"
	"github.com/stretchr/testify/require"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"

	"github.com/Hinkolas/skali/internal/claim"
	"github.com/Hinkolas/skali/internal/dbstore"
	"github.com/Hinkolas/skali/internal/kube"
	"github.com/Hinkolas/skali/internal/kubernetes"
	"github.com/Hinkolas/skali/internal/kubetest"
	"github.com/Hinkolas/skali/internal/module"
	"github.com/Hinkolas/skali/internal/observe"
	"github.com/Hinkolas/skali/internal/project"
	"github.com/Hinkolas/skali/internal/store"
	"github.com/Hinkolas/skali/internal/substrate/seaweed"
	"github.com/Hinkolas/skali/internal/testdb"
)

// TestLiveBucketCORSAndUploadCleanup proves the two declarative additions
// on a real store: a bucket declaring CORS answers a preflight from a
// listed origin with the declared policy and refuses a foreign origin,
// changing the declaration re-converges the bucket, and a multipart
// upload older than the bucket's abort threshold is swept by the probe
// while a fresh one survives. Requires TEST_KUBECONFIG and
// TEST_DATABASE_URL.
func TestLiveBucketCORSAndUploadCleanup(t *testing.T) {
	config := kubetest.Config(t)
	pool := testdb.New(t)
	ctx := context.Background()

	client, err := kube.NewFromConfig(config)
	require.NoError(t, err)
	installOperator(t, client)

	st := store.NewStore(pool)
	dbSvc := dbstore.New(st)
	projects := project.New(st)

	suffix := uuid.Must(uuid.NewV7()).String()[24:]
	proj, err := projects.Create(ctx, "demo"+suffix, "", uuid.Nil)
	require.NoError(t, err)
	env, err := projects.CreateEnvironment(ctx, proj.ID, "production", project.EnvironmentOptions{})
	require.NoError(t, err)

	controller := New(Deps{
		DB:       dbSvc,
		Cluster:  KubeCluster{Client: client},
		Observed: observe.NewStore(nil),
		Seaweed:  seaweed.NewClient(client, Namespace),
		Enqueue:  func(uuid.UUID) {},
	}, Config{Managed: false})
	cleanupPlatform(t, client)

	namespace := kubernetes.RenderNamespace(proj.Name, "production", env.ID.String())
	_, err = client.Apply(ctx, namespace, false)
	require.NoError(t, err)
	t.Cleanup(func() { deleteNamespace(t, client, namespace.Name) })

	// The claim declares CORS for one origin and a three-second abort
	// threshold for abandoned uploads.
	owner := dbstore.ServiceOwner(proj.ID, env.ID, proj.Name, env.Name, "files")
	spec := dbstore.BucketSpec{
		Visibility: "private", StorageQuotaBytes: 1 << 30, Versioning: "disabled",
		AbortUploadsAfterSeconds: 3,
		CORS: []byte(`{"allowedOrigins":["https://app.example"],"allowedMethods":["GET","PUT"],` +
			`"allowedHeaders":["content-type"],"exposeHeaders":["ETag"],"maxAgeSeconds":600}`),
	}
	created, err := dbSvc.EnsureBucketClaim(ctx, owner, spec)
	require.NoError(t, err)
	deadline := time.Now().Add(10 * time.Minute)
	for {
		require.False(t, time.Now().After(deadline), "bucket claim not provisioned; last wait: %s", controller.WaitingReason(created.ID))
		if _, err := controller.reconcileBucketClaim(ctx, created.ID); err != nil {
			t.Logf("bucket claim (retrying): %v", err)
		}
		if _, err := controller.reconcileObjectStore(ctx); err != nil {
			t.Logf("object store (retrying): %v", err)
		}
		if metadata, err := dbSvc.LiveSystemClaim(ctx, MetadataClaimKey); err == nil {
			_, _ = controller.reconcileClaim(ctx, metadata.ID)
		}
		current, err := dbSvc.GetBucketClaim(ctx, created.ID)
		require.NoError(t, err)
		if claim.Phase(current.Phase) == claim.PhaseProvisioned {
			break
		}
		time.Sleep(2 * time.Second)
	}
	allocation, err := dbSvc.LiveAllocation(ctx, created.ID)
	require.NoError(t, err)
	bucket := allocation.BucketName

	credential, err := client.Clientset.CoreV1().Secrets(Namespace).Get(ctx, allocation.CredentialSecret, metav1.GetOptions{})
	require.NoError(t, err)
	endpoint := kubetest.PortForward(t, config, Namespace, "app="+seaweed.AllInOneApp, seaweed.S3Port)
	app, err := minio.New(endpoint, &minio.Options{
		Creds:        credentials.NewStaticV4(string(credential.Data["access_key"]), string(credential.Data["secret_key"]), ""),
		Region:       seaweed.Region,
		BucketLookup: minio.BucketLookupPath,
	})
	require.NoError(t, err)
	_, err = app.PutObject(ctx, bucket, "hello", bytes.NewReader([]byte("hi")), 2, minio.PutObjectOptions{})
	require.NoError(t, err)

	// The browser's preflight against a presigned URL: the listed origin
	// gets the declared policy, a foreign one is refused.
	signed, err := app.PresignedPutObject(ctx, bucket, "hello", 10*time.Minute)
	require.NoError(t, err)
	web := &http.Client{Timeout: 30 * time.Second}
	preflight := func(origin string) *http.Response {
		t.Helper()
		request, err := http.NewRequestWithContext(ctx, http.MethodOptions, signed.String(), nil)
		require.NoError(t, err)
		request.Header.Set("Origin", origin)
		request.Header.Set("Access-Control-Request-Method", http.MethodPut)
		request.Header.Set("Access-Control-Request-Headers", "content-type")
		response, err := web.Do(request)
		require.NoError(t, err)
		response.Body.Close()
		return response
	}
	requireEventually(t, time.Minute, func() bool {
		return preflight("https://foreign.example").StatusCode == http.StatusForbidden
	}, "a foreign origin's preflight was never refused")
	listed := preflight("https://app.example")
	require.Equal(t, http.StatusOK, listed.StatusCode)
	require.Equal(t, "https://app.example", listed.Header.Get("Access-Control-Allow-Origin"))
	require.Equal(t, "GET, PUT", listed.Header.Get("Access-Control-Allow-Methods"))
	require.Equal(t, "content-type", listed.Header.Get("Access-Control-Allow-Headers"))
	require.Equal(t, "ETag", listed.Header.Get("Access-Control-Expose-Headers"))
	require.Equal(t, "600", listed.Header.Get("Access-Control-Max-Age"))

	// A changed declaration (a second origin) re-converges on the next
	// pass and reports the reset as drift; a converged bucket is quiet.
	spec.CORS = []byte(`{"allowedOrigins":["https://app.example","https://admin.example"],"allowedMethods":["GET","PUT"]}`)
	_, err = dbSvc.EnsureBucketClaim(ctx, owner, spec)
	require.NoError(t, err)
	probe := controller.SeaweedProbe()
	observeBucket := func() *module.BucketStatus {
		objects, err := probe(ctx)
		require.NoError(t, err)
		for _, object := range objects {
			if object.Kind == module.KindBucket && object.Ref.Name == bucket {
				return object.Bucket
			}
		}
		return nil
	}
	status := observeBucket()
	require.NotNil(t, status)
	require.Equal(t, []string{"cors"}, status.ConfigurationDrift)
	requireEventually(t, time.Minute, func() bool {
		return preflight("https://admin.example").StatusCode == http.StatusOK
	}, "the added origin never took effect")
	require.Equal(t, http.StatusForbidden, preflight("https://foreign.example").StatusCode)
	require.Empty(t, observeBucket().ConfigurationDrift, "a converged bucket is a read-only pass")

	// Abandoned multipart uploads: one started before the threshold is
	// swept by the probe, one started just now survives it.
	core := &minio.Core{Client: app}
	stale, err := core.NewMultipartUpload(ctx, bucket, "stale", minio.PutObjectOptions{})
	require.NoError(t, err)
	part := bytes.Repeat([]byte("p"), 8<<10)
	_, err = core.PutObjectPart(ctx, bucket, "stale", stale, 1, bytes.NewReader(part), int64(len(part)), minio.PutObjectPartOptions{})
	require.NoError(t, err)
	time.Sleep(5 * time.Second)
	fresh, err := core.NewMultipartUpload(ctx, bucket, "fresh", minio.PutObjectOptions{})
	require.NoError(t, err)
	_ = observeBucket()
	uploads, err := core.ListMultipartUploads(ctx, bucket, "", "", "", "", 100)
	require.NoError(t, err)
	ids := map[string]bool{}
	for _, upload := range uploads.Uploads {
		ids[upload.UploadID] = true
	}
	require.False(t, ids[stale], "the stale upload must be aborted by the sweep")
	require.True(t, ids[fresh], "a fresh upload survives the sweep")
	require.NoError(t, core.AbortMultipartUpload(ctx, bucket, "fresh", fresh))
}

package substrate

import (
	"context"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"

	"github.com/Hinkolas/skali/internal/claim"
	"github.com/Hinkolas/skali/internal/dbstore"
	"github.com/Hinkolas/skali/internal/kubernetes"
)

// runBucketDestructiveRemoval executes the persisted destructive
// decision against a real cluster: identity gone, bucket metadata gone,
// collection data freed, Secrets deleted, claim released; the store itself
// survives and keeps running (the always-on dev substrate). Requires
// TEST_KUBECONFIG and TEST_DATABASE_URL.
func runBucketDestructiveRemoval(t *testing.T, fixture *bucketLiveFixture) {
	ctx := context.Background()
	client, dbSvc := fixture.client, fixture.db
	controller := fixture.controller(nil)
	proj, env := fixture.newEnvironment(t, controller)
	namespace := kubernetes.RenderNamespace(proj.Name, env.Name, env.ID.String())

	owner := dbstore.ServiceOwner(proj.ID, env.ID, proj.Name, "production", "files")
	created, err := dbSvc.EnsureBucketClaim(ctx, owner, dbstore.BucketSpec{
		Visibility: "private", StorageQuotaBytes: 1 << 30, Versioning: "disabled",
	})
	require.NoError(t, err)

	drive := func(target claim.Phase, deadline time.Duration, withStore bool) {
		t.Helper()
		driveLive(t, "bucket "+string(target), deadline, func(ctx context.Context) (bool, error) {
			return bucketClaimPass(ctx, controller, dbSvc, created.ID, target, withStore)
		})
	}
	drive(claim.PhaseProvisioned, 10*time.Minute, true)

	allocation, err := dbSvc.LiveAllocation(ctx, created.ID)
	require.NoError(t, err)

	// Write one object so the collection holds real volume data.
	writeFilerObject(t, client, allocation.BucketName, "doomed.txt", "bytes that must die")

	// The destructive decision, then teardown to released.
	_, err = dbSvc.ReleaseBucketClaim(ctx, created.ID)
	require.NoError(t, err)
	drive(claim.PhaseReleased, 5*time.Minute, false)

	// The bucket, its identity, and its Secrets are gone.
	exists, err := controller.deps.Seaweed.BucketExists(ctx, allocation.BucketName)
	require.NoError(t, err)
	require.False(t, exists, "the bucket metadata must be gone")
	identities, err := controller.deps.Seaweed.Identities(ctx)
	require.NoError(t, err)
	require.Nil(t, identities.Find(allocation.BucketName), "the identity must be gone")
	_, err = client.Clientset.CoreV1().Secrets(Namespace).
		Get(ctx, allocation.CredentialSecret, metav1.GetOptions{})
	require.True(t, apierrors.IsNotFound(err), "the credential secret must be gone")
	_, err = client.Clientset.CoreV1().Secrets(namespace.Name).
		Get(ctx, kubernetes.OutputSecretName("buckets", "files"), metav1.GetOptions{})
	require.True(t, apierrors.IsNotFound(err), "the output mirror must be gone")

	// The store survives the bucket's death and keeps running: the dev
	// substrate is always on (owner decision 2026-07-31).
	sw, err := dbSvc.LiveObjectStore(ctx)
	require.NoError(t, err)
	require.Equal(t, dbstore.StateActive, sw.State)
	_, err = controller.reconcileObjectStore(ctx)
	require.NoError(t, err)
	current, err := dbSvc.LiveObjectStore(ctx)
	require.NoError(t, err)
	require.Equal(t, dbstore.StateActive, current.State,
		"an idle store must stay active, never stop")
}

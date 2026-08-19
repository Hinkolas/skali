package substrate

import (
	"context"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/stretchr/testify/require"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"

	"github.com/Hinkolas/skali/internal/claim"
	"github.com/Hinkolas/skali/internal/dbstore"
	"github.com/Hinkolas/skali/internal/kube"
	"github.com/Hinkolas/skali/internal/kubernetes"
	"github.com/Hinkolas/skali/internal/kubetest"
	"github.com/Hinkolas/skali/internal/observe"
	"github.com/Hinkolas/skali/internal/project"
	"github.com/Hinkolas/skali/internal/store"
	"github.com/Hinkolas/skali/internal/substrate/seaweed"
	"github.com/Hinkolas/skali/internal/testdb"
)

// TestLiveBucketDestructiveRemoval executes the persisted destructive
// decision against a real cluster: identity gone, bucket metadata gone,
// collection data freed, Secrets deleted, claim released; the store itself
// survives and keeps running (the always-on dev substrate). Requires
// TEST_KUBECONFIG and TEST_DATABASE_URL.
func TestLiveBucketDestructiveRemoval(t *testing.T) {
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
	}, Config{Managed: false})

	cleanupPlatform(t, client)

	namespace := kubernetes.RenderNamespace(proj.Name, "production", env.ID.String())
	_, err = client.Apply(ctx, namespace, false)
	require.NoError(t, err)
	t.Cleanup(func() { deleteNamespace(t, client, namespace.Name) })

	owner := dbstore.ServiceOwner(proj.ID, env.ID, proj.Name, "production", "files")
	created, err := dbSvc.EnsureBucketClaim(ctx, owner, dbstore.BucketSpec{
		Visibility: "private", StorageQuotaBytes: 1 << 30, Versioning: "disabled",
	})
	require.NoError(t, err)

	drive := func(target claim.Phase, deadline time.Duration, withStore bool) {
		t.Helper()
		limit := time.Now().Add(deadline)
		for {
			require.False(t, time.Now().After(limit),
				"claim never reached %s; last wait: %s", target, controller.WaitingReason(created.ID))
			requeue, err := controller.reconcileBucketClaim(ctx, created.ID)
			if withStore {
				_, _ = controller.reconcileObjectStore(ctx)
			}
			if metadata, err := dbSvc.LiveSystemClaim(ctx, MetadataClaimKey); err == nil {
				_, _ = controller.reconcileClaim(ctx, metadata.ID)
			}
			current, getErr := dbSvc.GetBucketClaim(ctx, created.ID)
			require.NoError(t, getErr)
			if claim.Phase(current.Phase) == target {
				return
			}
			stepClaim(t, "bucket claim", requeue, err, claim.Phase(current.Phase), controller.WaitingReason(created.ID))
			time.Sleep(2 * time.Second)
		}
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

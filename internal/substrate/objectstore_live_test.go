package substrate

import (
	"context"
	"net/http"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/stretchr/testify/require"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"

	"github.com/Hinkolas/skali/internal/claim"
	"github.com/Hinkolas/skali/internal/dbstore"
	"github.com/Hinkolas/skali/internal/kube"
	"github.com/Hinkolas/skali/internal/kubetest"
	"github.com/Hinkolas/skali/internal/observe"
	"github.com/Hinkolas/skali/internal/project"
	"github.com/Hinkolas/skali/internal/store"
	"github.com/Hinkolas/skali/internal/substrate/seaweed"
	"github.com/Hinkolas/skali/internal/testdb"
)

// TestLiveObjectStoreBoot drives the physical SeaweedFS system from nothing
// to ready against a real k3d cluster: the visible metadata-claim wait, the
// filer store Secret derived from the claim's outputs, the all-in-one dev
// shape, and an answering S3 port through the network fence. Requires
// TEST_KUBECONFIG and TEST_DATABASE_URL.
func TestLiveObjectStoreBoot(t *testing.T) {
	config := kubetest.Config(t)
	pool := testdb.New(t)
	ctx := context.Background()

	client, err := kube.NewFromConfig(config)
	require.NoError(t, err)
	installOperator(t, client)

	st := store.NewStore(pool)
	dbSvc := dbstore.New(st)
	projects := project.New(st)
	controller := New(Deps{
		DB:       dbSvc,
		Cluster:  KubeCluster{Client: client},
		Observed: observe.NewStore(nil),
		Seaweed:  seaweed.NewClient(client, Namespace),
	}, Config{Managed: false})

	cleanupPlatform(t, client)

	// The dev store is lazy AND demand-driven: without a live bucket claim
	// the quiet lifecycle stops it immediately, so the boot walk holds one
	// pending claim as demand (it is never driven; the store alone is under
	// test).
	suffix := uuid.Must(uuid.NewV7()).String()[24:]
	proj, err := projects.Create(ctx, "demo"+suffix, "")
	require.NoError(t, err)
	env, err := projects.CreateEnvironment(ctx, proj.ID, "production")
	require.NoError(t, err)
	owner := dbstore.ServiceOwner(proj.ID, env.ID, proj.Name, "production", "files")
	_, err = dbSvc.EnsureBucketClaim(ctx, owner, dbstore.BucketSpec{
		Visibility: "private", Versioning: "disabled",
	})
	require.NoError(t, err)

	// Dev creates the store lazily; the first bucket claim calls this.
	row, err := controller.ensureObjectStoreRow(ctx)
	require.NoError(t, err)
	require.Equal(t, dbstore.StateActive, row.State)
	require.EqualValues(t, 1, row.Masters)

	// The first reconcile pass cannot finish: the metadata database claim is
	// the visible first gate of the REWORK 10.5 chain.
	requeue, err := controller.reconcileObjectStore(ctx)
	require.NoError(t, err)
	require.Equal(t, requeueWait, requeue, "the store visibly waits before its metadata database exists")
	metadata, err := dbSvc.LiveSystemClaim(ctx, MetadataClaimKey)
	require.NoError(t, err)
	require.NotEqual(t, string(claim.PhaseProvisioned), metadata.Phase)

	// Drive the store and its metadata claim like the workers would.
	deadline := time.Now().Add(8 * time.Minute)
	for {
		require.False(t, time.Now().After(deadline),
			"object store not ready before deadline; metadata wait: %s", controller.WaitingReason(metadata.ID))
		if _, err := controller.reconcileClaim(ctx, metadata.ID); err != nil {
			t.Logf("metadata claim (retrying): %v", err)
		}
		requeue, err := controller.reconcileObjectStore(ctx)
		if err != nil {
			t.Logf("object store (retrying): %v", err)
		} else if requeue == 0 {
			break
		}
		time.Sleep(2 * time.Second)
	}

	// The metadata database provisioned through the ordinary system-claim
	// path; the filer store Secret carries its outputs, password included,
	// Secret-to-Secret only.
	outputs, err := controller.EnsureSystemClaim(ctx, MetadataClaimKey, metadataClaimSpec())
	require.NoError(t, err)
	require.True(t, outputs.Provisioned)
	filerStore, err := client.Clientset.CoreV1().Secrets(Namespace).Get(ctx, seaweed.FilerStoreSecret, metav1.GetOptions{})
	require.NoError(t, err)
	require.Equal(t, outputs.Host, string(filerStore.Data["WEED_POSTGRES2_HOSTNAME"]))
	require.Equal(t, outputs.Database, string(filerStore.Data["WEED_POSTGRES2_DATABASE"]))
	require.NotEmpty(t, filerStore.Data["WEED_POSTGRES2_PASSWORD"])

	// The dev all-in-one answers on the S3 port through the fence: auth is
	// on from process start (the bootstrap deny identity), so an anonymous
	// request is rejected, not served.
	data, status, err := client.ServiceProxyDo(ctx, http.MethodGet, Namespace,
		seaweed.S3Service, seaweed.S3Port, "/", nil, nil)
	require.NoError(t, err)
	require.Equal(t, http.StatusForbidden, status, "anonymous S3 must be denied, got %d: %s", status, data)

	// The admin channel works end to end: the bootstrap identity is the
	// only one, kept so the identity list can never go empty.
	identities, err := controller.deps.Seaweed.Identities(ctx)
	require.NoError(t, err)
	require.NotNil(t, identities.Find(seaweed.BootstrapIdentityName))

	// Idempotence: a settled store reconciles to a no-op.
	requeue, err = controller.reconcileObjectStore(ctx)
	require.NoError(t, err)
	require.Zero(t, requeue)
}

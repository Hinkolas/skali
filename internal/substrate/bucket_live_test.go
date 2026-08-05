package substrate

import (
	"context"
	"fmt"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/stretchr/testify/require"
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
	"github.com/Hinkolas/skali/internal/utils"
)

// TestLiveBucketClaimProvisioning drives one bucket claim from pending to
// provisioned against a real k3d cluster: the lazily created dev store, the
// allocation, the credential Secret, the SeaweedFS bucket and identity, and
// the output mirror in the environment namespace. Requires TEST_KUBECONFIG
// and TEST_DATABASE_URL.
func TestLiveBucketClaimProvisioning(t *testing.T) {
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
	proj, err := projects.Create(ctx, "demo"+suffix, "")
	require.NoError(t, err)
	env, err := projects.CreateEnvironment(ctx, proj.ID, "production")
	require.NoError(t, err)

	var poked []uuid.UUID
	controller := New(Deps{
		DB:       dbSvc,
		Cluster:  KubeCluster{Client: client},
		Observed: observe.NewStore(nil),
		Seaweed:  seaweed.NewClient(client, Namespace),
		Enqueue:  func(id uuid.UUID) { poked = append(poked, id) },
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

	// Drive the claim, the store, and the metadata claim like the workers
	// would; the store's whole dependency chain (metadata tenant -> filer ->
	// s3 -> bucket) is exercised on the way.
	deadline := time.Now().Add(10 * time.Minute)
	for {
		require.False(t, time.Now().After(deadline),
			"bucket claim not provisioned before deadline; last wait: %s", controller.WaitingReason(created.ID))
		requeue, err := controller.reconcileBucketClaim(ctx, created.ID)
		if err != nil {
			t.Logf("bucket claim (retrying): %v", err)
		}
		if _, err := controller.reconcileObjectStore(ctx); err != nil {
			t.Logf("object store (retrying): %v", err)
		}
		if metadata, err := dbSvc.LiveSystemClaim(ctx, MetadataClaimKey); err == nil {
			mRequeue, mErr := controller.reconcileClaim(ctx, metadata.ID)
			if mErr != nil {
				t.Logf("metadata claim (retrying): %v", mErr)
			} else if fresh, freshErr := dbSvc.GetClaim(ctx, metadata.ID); freshErr == nil {
				stepClaim(t, "metadata claim", mRequeue, mErr,
					claim.Phase(fresh.Phase), controller.WaitingReason(metadata.ID))
			}
		}
		current, getErr := dbSvc.GetBucketClaim(ctx, created.ID)
		require.NoError(t, getErr)
		if claim.Phase(current.Phase) == claim.PhaseProvisioned {
			break
		}
		stepClaim(t, "bucket claim", requeue, err, claim.Phase(current.Phase), controller.WaitingReason(created.ID))
		time.Sleep(2 * time.Second)
	}

	// The allocation carries the generated identity and the in-cluster
	// endpoint.
	allocation, err := dbSvc.LiveAllocation(ctx, created.ID)
	require.NoError(t, err)
	require.Regexp(t, `^b-files-[0-9a-f]{8}$`, allocation.BucketName)
	require.Equal(t, "s3cred-"+utils.ShortID(created.ID), allocation.CredentialSecret)
	require.Equal(t, "http://seaweed-s3.skali-platform.svc.cluster.local:8333", allocation.Endpoint)

	// The credential Secret and the output mirror agree on the keypair,
	// which never touched a database row.
	credential, err := client.Clientset.CoreV1().Secrets(Namespace).
		Get(ctx, allocation.CredentialSecret, metav1.GetOptions{})
	require.NoError(t, err)
	accessKey := string(credential.Data["access_key"])
	secretKey := string(credential.Data["secret_key"])
	require.Equal(t, allocation.AccessKeyID, accessKey)
	require.Len(t, secretKey, 40)
	mirror, err := client.Clientset.CoreV1().Secrets(namespace.Name).
		Get(ctx, kubernetes.OutputSecretName("buckets", "files"), metav1.GetOptions{})
	require.NoError(t, err)
	require.Equal(t, allocation.Endpoint, string(mirror.Data["endpoint"]))
	require.Equal(t, allocation.BucketName, string(mirror.Data["name"]))
	require.Equal(t, seaweed.Region, string(mirror.Data["region"]))
	require.Equal(t, accessKey, string(mirror.Data["access_key"]))
	require.Equal(t, secretKey, string(mirror.Data["secret_key"]))

	// The bucket and its scoped identity exist on the store.
	exists, err := controller.deps.Seaweed.BucketExists(ctx, allocation.BucketName)
	require.NoError(t, err)
	require.True(t, exists)
	identities, err := controller.deps.Seaweed.Identities(ctx)
	require.NoError(t, err)
	identity := identities.Find(allocation.BucketName)
	require.NotNil(t, identity)
	require.Contains(t, identity.Actions, "Write:"+allocation.BucketName)
	require.NotNil(t, identities.Find(seaweed.BootstrapIdentityName),
		"the deny identity survives every reconcile")

	// The kernel was poked so the waiting application unblocks.
	require.Contains(t, poked, env.ID)

	// The credential leak audit: the generated secret key exists only in
	// Kubernetes Secrets. Scan every durable text-ish column for it.
	columns, err := pool.Query(ctx, `
		SELECT table_name, column_name FROM information_schema.columns
		WHERE table_schema = 'public'
		  AND data_type IN ('text', 'jsonb', 'character varying')`)
	require.NoError(t, err)
	type column struct{ table, name string }
	var scan []column
	for columns.Next() {
		var c column
		require.NoError(t, columns.Scan(&c.table, &c.name))
		scan = append(scan, c)
	}
	columns.Close()
	require.NotEmpty(t, scan)
	for _, c := range scan {
		var count int
		require.NoError(t, pool.QueryRow(ctx, fmt.Sprintf(
			`SELECT count(*) FROM %q WHERE %q::text LIKE '%%' || $1 || '%%'`,
			c.table, c.name), secretKey).Scan(&count))
		require.Zero(t, count, "secret key leaked into %s.%s", c.table, c.name)
	}

	// Re-running is a no-op: same identity, same keypair, still
	// provisioned.
	_, err = controller.reconcileBucketClaim(ctx, created.ID)
	require.NoError(t, err)
	again, err := dbSvc.LiveAllocation(ctx, created.ID)
	require.NoError(t, err)
	require.Equal(t, allocation.ID, again.ID)
	repeat, err := client.Clientset.CoreV1().Secrets(Namespace).
		Get(ctx, allocation.CredentialSecret, metav1.GetOptions{})
	require.NoError(t, err)
	require.Equal(t, secretKey, string(repeat.Data["secret_key"]))
}

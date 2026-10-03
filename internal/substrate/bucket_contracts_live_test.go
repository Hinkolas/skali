package substrate

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"os"
	"slices"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/minio/minio-go/v7"
	"github.com/stretchr/testify/require"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/client-go/rest"

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

type bucketLiveFixture struct {
	config   *rest.Config
	client   *kube.Client
	pool     *pgxpool.Pool
	st       *store.Store
	db       *dbstore.Service
	control  *Controller
	poisoned bool
}

func liveS3Transport(t *testing.T) *http.Transport {
	t.Helper()
	transport, err := minio.DefaultTransport(false)
	require.NoError(t, err)
	t.Cleanup(transport.CloseIdleConnections)
	return transport
}

// TestLiveBucketContracts shares only physical infrastructure and its control
// database. Children are serial, have distinct owners, and release all claims.
// No wrapper tests: -run selects a child without running it a second time.
func TestLiveBucketContracts(t *testing.T) {
	defer livePhase(t, "scenario")()
	cases := []struct {
		name string
		run  func(*testing.T, *bucketLiveFixture)
	}{
		{"Permissions", runBucketPermissions},
		{"CORSAndUploads", runBucketCORSAndUploads},
		{"Quota", runBucketQuota},
		{"RestoreFence", runBucketRestoreFence},
		{"CredentialRotation", runBucketCredentialRotation},
		{"DestructiveRemoval", runBucketDestructiveRemoval},
	}
	order := os.Getenv("TEST_BUCKET_ORDER")
	if order != "" && order != "normal" && order != "reverse" {
		t.Fatalf("TEST_BUCKET_ORDER must be normal or reverse, got %q", order)
	}
	if order == "reverse" {
		slices.Reverse(cases)
	}
	var fixture *bucketLiveFixture
	poisoned := false
	for _, tc := range cases {
		t.Run(tc.name, func(child *testing.T) {
			// Gate before marking setup poisoned: skips are not fixture failures.
			config := kubetest.Config(child)
			if os.Getenv("TEST_DATABASE_URL") == "" {
				child.Skip("set TEST_DATABASE_URL to run bucket contracts")
			}
			if fixture == nil {
				poisoned = true
				fixture = newBucketLiveFixture(child, t, config)
				poisoned = false
			}
			if err := fixture.healthy(); err != nil {
				fixture.poisoned = true
				child.Fatalf("shared bucket fixture is not clean: %v", err)
			}
			defer livePhase(child, "scenario "+tc.name)()
			tc.run(child, fixture)
		})
		if poisoned || (fixture != nil && fixture.poisoned) {
			t.Errorf("shared fixture isolation could not be restored; remaining bucket scenarios stopped")
			break
		}
	}
}

func newBucketLiveFixture(t, scope *testing.T, config *rest.Config) *bucketLiveFixture {
	t.Helper()
	defer livePhase(t, "shared bucket setup")()
	f := &bucketLiveFixture{config: config, pool: testdb.NewScoped(t, scope)}
	var err error
	f.client, err = kube.NewFromConfig(config)
	require.NoError(t, err)
	scope.Cleanup(func() { deleteNamespace(scope, f.client, Namespace) })
	require.NoError(t, cleanLiveNamespace(f.client, Namespace), "clean leftover platform")
	installOperator(t, f.client)
	f.st = store.NewStore(f.pool)
	f.db = dbstore.New(f.st)
	f.control = f.controller(nil)
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	_, err = f.control.ensureObjectStoreRow(ctx)
	require.NoError(t, err)
	driveLive(t, "shared store provisioning", 10*time.Minute, func(ctx context.Context) (bool, error) { return objectStorePass(ctx, f.control, f.db) })
	return f
}

func (f *bucketLiveFixture) controller(enqueue func(uuid.UUID)) *Controller {
	if enqueue == nil {
		enqueue = func(uuid.UUID) {}
	}
	return New(Deps{DB: f.db, Cluster: KubeCluster{Client: f.client}, Observed: observe.NewStore(nil), Seaweed: seaweed.NewClient(f.client, Namespace), Enqueue: enqueue}, Config{Managed: false})
}

func (f *bucketLiveFixture) healthy() error {
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	claims, err := f.db.ListLiveBucketClaims(ctx)
	if err != nil {
		return err
	}
	if len(claims) > 0 {
		return fmt.Errorf("%d live bucket claims left by previous scenario", len(claims))
	}
	row, err := f.db.LiveObjectStore(ctx)
	if err != nil {
		return err
	}
	ready, reason, err := f.control.objectStoreReady(ctx, *row)
	if err != nil {
		return err
	}
	if !ready {
		return fmt.Errorf("store not ready: %s", reason)
	}
	outputs, err := f.control.EnsureSystemClaim(ctx, MetadataClaimKey, metadataClaimSpec())
	if err != nil {
		return err
	}
	if !outputs.Provisioned {
		return fmt.Errorf("metadata tenant not ready: %s", outputs.Waiting)
	}
	return nil
}

// newEnvironment registers cleanup before the namespace apply. Later SQL
// connections and port-forwards register after it and therefore close first.
func (f *bucketLiveFixture) newEnvironment(t *testing.T, c *Controller) (*store.Project, *store.Environment) {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	projects := project.New(f.st)
	proj, err := projects.Create(ctx, "contract"+uuid.NewString()[:8], "", uuid.Nil)
	require.NoError(t, err)
	var env *store.Environment
	t.Cleanup(func() {
		defer livePhase(t, "bucket case cleanup")()
		// The admin S3 client also caches a tunnel on kube.Client. Close the
		// scenario's tunnel now and any new tunnel used by release afterward.
		f.client.ForgetServiceAddress(Namespace, seaweed.S3Service, seaweed.S3Port)
		defer f.client.ForgetServiceAddress(Namespace, seaweed.S3Service, seaweed.S3Port)
		// Fresh callbacks must not send into a scenario's undrained test channel.
		c.deps.Enqueue = func(uuid.UUID) {}
		var releaseErr, namespaceErr, recordsErr error
		if env != nil {
			releaseErr = f.releaseEnvironment(c, env.ID)
			namespaceErr = cleanLiveNamespace(f.client, kubernetes.NamespaceName(env.ID.String()))
		}
		if releaseErr == nil && namespaceErr == nil {
			recordsErr = f.removeProjectRecords(proj.ID)
		}
		healthErr := f.healthy()
		if err := errors.Join(releaseErr, namespaceErr, recordsErr, healthErr); err != nil {
			f.poisoned = true
			t.Errorf("bucket fixture cleanup: %v", err)
		}
	})
	env, err = projects.CreateEnvironment(ctx, proj.ID, "production", project.EnvironmentOptions{})
	require.NoError(t, err)
	namespace := kubernetes.RenderNamespace(proj.Name, env.Name, env.ID.String())
	_, err = f.client.Apply(ctx, namespace, false)
	require.NoError(t, err)
	return proj, env
}

func (f *bucketLiveFixture) releaseEnvironment(c *Controller, envID uuid.UUID) error {
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Minute)
	defer cancel()
	claims, err := f.db.ListEnvironmentBucketClaims(ctx, envID)
	if err != nil {
		return err
	}
	var errs []error
	for _, row := range claims {
		allocation, allocErr := f.db.LiveAllocation(ctx, row.ID)
		if allocErr != nil && !errors.Is(allocErr, dbstore.ErrNotFound) {
			errs = append(errs, allocErr)
			continue
		}
		if _, err := f.db.ReleaseBucketClaim(ctx, row.ID); err != nil {
			errs = append(errs, err)
			continue
		}
		if err := pollLive(ctx, 2*time.Second, func(ctx context.Context) (bool, error) {
			return bucketClaimPass(ctx, c, f.db, row.ID, claim.PhaseReleased, false)
		}); err != nil {
			errs = append(errs, err)
			continue
		}
		if _, err := f.db.LiveAllocation(ctx, row.ID); !errors.Is(err, dbstore.ErrNotFound) {
			errs = append(errs, fmt.Errorf("allocation for %s survived release: %v", row.ID, err))
		}
		if allocation != nil {
			exists, err := c.deps.Seaweed.BucketExists(ctx, allocation.BucketName)
			if err != nil {
				errs = append(errs, err)
			} else if exists {
				errs = append(errs, fmt.Errorf("bucket %s survived release", allocation.BucketName))
			}
			identities, err := c.deps.Seaweed.Identities(ctx)
			if err != nil {
				errs = append(errs, err)
			} else if identities.Find(allocation.BucketName) != nil {
				errs = append(errs, fmt.Errorf("bucket identity %s survived release", allocation.BucketName))
			}
			for _, secret := range []struct{ namespace, name string }{
				{Namespace, allocation.CredentialSecret},
				{kubernetes.NamespaceName(envID.String()), kubernetes.OutputSecretName("buckets", row.ServiceKey)},
			} {
				_, err := f.client.Clientset.CoreV1().Secrets(secret.namespace).Get(ctx, secret.name, metav1.GetOptions{})
				if !apierrors.IsNotFound(err) {
					errs = append(errs, fmt.Errorf("secret %s/%s survived release: %v", secret.namespace, secret.name, err))
				}
			}
		}
	}
	return errors.Join(errs...)
}

// Only discard scenario records after normal release and Kubernetes cleanup.
// Keep failed cleanup evidence until parent teardown; never mask a live claim
// by deleting its rows. The shared store and metadata records are untouched.
func (f *bucketLiveFixture) removeProjectRecords(projectID uuid.UUID) error {
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	tx, err := f.pool.Begin(ctx)
	if err != nil {
		return err
	}
	defer tx.Rollback(ctx)
	var live bool
	if err := tx.QueryRow(ctx, `SELECT EXISTS (
		SELECT 1 FROM bucket_claims WHERE project_id=$1 AND phase <> 'released'
		UNION ALL SELECT 1 FROM bucket_allocations a JOIN bucket_claims c ON c.id=a.claim_id
		WHERE c.project_id=$1 AND a.released_at IS NULL)`, projectID).Scan(&live); err != nil {
		return err
	}
	if live {
		return fmt.Errorf("project %s still owns live bucket records", projectID)
	}
	for _, statement := range []string{
		`DELETE FROM bucket_allocations WHERE claim_id IN (SELECT id FROM bucket_claims WHERE project_id=$1)`,
		`DELETE FROM bucket_claims WHERE project_id=$1`,
		`DELETE FROM projects WHERE id=$1`,
	} {
		if _, err := tx.Exec(ctx, statement, projectID); err != nil {
			return err
		}
	}
	return tx.Commit(ctx)
}

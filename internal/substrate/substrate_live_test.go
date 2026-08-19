package substrate

import (
	"context"
	"fmt"
	"sync"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/stretchr/testify/require"
	corev1 "k8s.io/api/core/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"

	"github.com/Hinkolas/skali/internal/bundle"
	"github.com/Hinkolas/skali/internal/claim"
	"github.com/Hinkolas/skali/internal/dbstore"
	"github.com/Hinkolas/skali/internal/kube"
	"github.com/Hinkolas/skali/internal/kubernetes"
	"github.com/Hinkolas/skali/internal/kubetest"
	"github.com/Hinkolas/skali/internal/module"
	"github.com/Hinkolas/skali/internal/observe"
	"github.com/Hinkolas/skali/internal/project"
	"github.com/Hinkolas/skali/internal/store"
	"github.com/Hinkolas/skali/internal/substrate/cnpg"
	"github.com/Hinkolas/skali/internal/testdb"
	"github.com/Hinkolas/skali/internal/utils"
)

// TestLiveClaimProvisioning drives one claim from pending to provisioned
// against a real k3d cluster: shared pool creation, CNPG Cluster and
// Database objects, the credential Secret, and the output mirror in the
// environment namespace. Requires TEST_KUBECONFIG and TEST_DATABASE_URL.
func TestLiveClaimProvisioning(t *testing.T) {
	config := kubetest.Config(t)
	pool := testdb.New(t)
	ctx := context.Background()

	client, err := kube.NewFromConfig(config)
	require.NoError(t, err)
	installOperator(t, client)

	st := store.NewStore(pool)
	dbSvc := dbstore.New(st)
	projects := project.New(st)

	// Unique project name per run so environment namespaces never collide
	// on a shared test cluster; the platform namespace is cleaned up below.
	suffix := uuid.Must(uuid.NewV7()).String()[24:]
	proj, err := projects.Create(ctx, "demo"+suffix, "", uuid.Nil)
	require.NoError(t, err)
	env, err := projects.CreateEnvironment(ctx, proj.ID, "production", project.EnvironmentOptions{})
	require.NoError(t, err)

	var poked []uuid.UUID
	controller := New(Deps{
		DB:       dbSvc,
		Cluster:  KubeCluster{Client: client},
		Observed: observe.NewStore(nil),
		Enqueue:  func(id uuid.UUID) { poked = append(poked, id) },
	}, Config{Managed: false})

	cleanupPlatform(t, client)

	// The environment namespace normally comes from the environment
	// reconciler; the mirror write depends on it.
	namespace := kubernetes.RenderNamespace(proj.Name, "production", env.ID.String())
	_, err = client.Apply(ctx, namespace, false)
	require.NoError(t, err)
	t.Cleanup(func() { deleteNamespace(t, client, namespace.Name) })

	owner := dbstore.ServiceOwner(proj.ID, env.ID, proj.Name, "production", "data")
	created, err := dbSvc.EnsureClaim(ctx, owner, dbstore.ClaimSpec{
		Engine: "postgres", Major: 17,
		Isolation: "project", Availability: "single",
		Extensions: []string{"pg_trgm"},
	})
	require.NoError(t, err)

	// Drive the claim like the worker would: errors and waits retry until
	// the deadline (first pool bring-up pulls the postgres image).
	deadline := time.Now().Add(5 * time.Minute)
	for {
		require.False(t, time.Now().After(deadline),
			"claim not provisioned before deadline; last wait: %s", controller.WaitingReason(created.ID))
		requeue, err := controller.reconcileClaim(ctx, created.ID)
		if err != nil {
			t.Logf("reconcile (retrying): %v", err)
		}
		current, getErr := dbSvc.GetClaim(ctx, created.ID)
		require.NoError(t, getErr)
		if claim.Phase(current.Phase) == claim.PhaseProvisioned {
			break
		}
		stepClaim(t, "claim", requeue, err, claim.Phase(current.Phase), controller.WaitingReason(created.ID))
		time.Sleep(2 * time.Second)
	}

	// Local dev collapse: the project-isolated claim landed on the shared
	// dev pool anyway.
	devPool, err := dbSvc.LiveSharedCluster(ctx, "postgres", 17)
	require.NoError(t, err)
	require.Equal(t, "pg17-shared", devPool.Name)

	tenant, err := dbSvc.LiveTenant(ctx, created.ID)
	require.NoError(t, err)
	require.Equal(t, "pg17-shared-rw.skali-platform.svc.cluster.local", tenant.Host)

	// The CNPG objects exist with the rendered shape.
	cluster, err := client.Dynamic.Resource(cnpg.ClusterGVR).Namespace(Namespace).
		Get(ctx, devPool.Name, metav1.GetOptions{})
	require.NoError(t, err)
	require.Equal(t, "off", cluster.GetAnnotations()[cnpg.HibernationAnnotation])
	database, err := client.Dynamic.Resource(cnpg.DatabaseGVR).Namespace(Namespace).
		Get(ctx, "db-"+utils.ShortID(created.ID), metav1.GetOptions{})
	require.NoError(t, err)
	require.Equal(t, "databases.data", database.GetLabels()[kubernetes.LabelService])

	// Credential Secret and output mirror agree on the password, which
	// never touched a database row.
	credential, err := client.Clientset.CoreV1().Secrets(Namespace).
		Get(ctx, tenant.CredentialSecret, metav1.GetOptions{})
	require.NoError(t, err)
	password := string(credential.Data[corev1.BasicAuthPasswordKey])
	require.Len(t, password, 32)

	mirror, err := client.Clientset.CoreV1().Secrets(namespace.Name).
		Get(ctx, kubernetes.OutputSecretName("databases", "data"), metav1.GetOptions{})
	require.NoError(t, err)
	require.Equal(t, password, string(mirror.Data["password"]))
	require.Equal(t, tenant.Host, string(mirror.Data["host"]))
	require.Equal(t, tenant.DatabaseName, string(mirror.Data["name"]))
	require.Equal(t, tenant.RoleName, string(mirror.Data["username"]))
	require.Contains(t, string(mirror.Data["url"]), "postgresql://"+tenant.RoleName+":")
	require.Contains(t, poked, env.ID, "provisioning must poke the environment reconciler")

	// The credential leak audit: the generated password exists only in
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
			c.table, c.name), password).Scan(&count))
		require.Zero(t, count, "password leaked into %s.%s", c.table, c.name)
	}

	// Re-running is a no-op: same identity, same password, still
	// provisioned.
	_, err = controller.reconcileClaim(ctx, created.ID)
	require.NoError(t, err)
	again, err := dbSvc.LiveTenant(ctx, created.ID)
	require.NoError(t, err)
	require.Equal(t, tenant.ID, again.ID)
	repeat, err := client.Clientset.CoreV1().Secrets(Namespace).
		Get(ctx, tenant.CredentialSecret, metav1.GetOptions{})
	require.NoError(t, err)
	require.Equal(t, password, string(repeat.Data[corev1.BasicAuthPasswordKey]))

	// Dynamic observation: pool and tenant project into the observed store
	// through the watch, join the service snapshot via the shared key, and
	// a primary kill propagates topology without request-time reads.
	observedStore := observe.NewStore(nil)
	var pokeMu sync.Mutex
	pokeCount := map[uuid.UUID]int{}
	source := observe.NewKubeSource(client, observedStore, observe.SourceOptions{
		StaleThreshold: 30 * time.Second,
		Enqueue: func(id uuid.UUID) {
			pokeMu.Lock()
			pokeCount[id]++
			pokeMu.Unlock()
		},
		Dynamic: cnpg.ObserveKinds(),
	})
	sourceCtx, cancelSource := context.WithCancel(ctx)
	defer cancelSource()
	go func() { _ = source.Run(sourceCtx) }()

	poolStatus := func() *module.DatabaseClusterStatus {
		snapshot := observedStore.Snapshot(env.ID)
		for _, resource := range snapshot.ForService("databases.data") {
			if resource.Kind == module.KindDatabaseCluster {
				return resource.DatabaseCluster
			}
		}
		return nil
	}
	requireEventually(t, 2*time.Minute, func() bool {
		snapshot := observedStore.Snapshot(env.ID)
		var tenantSeen, poolSeen bool
		for _, resource := range snapshot.ForService("databases.data") {
			switch resource.Kind {
			case module.KindDatabaseTenant:
				tenantSeen = resource.DatabaseTenant.Applied
			case module.KindDatabaseCluster:
				poolSeen = resource.DatabaseCluster.ReadyInstances >= 1
			}
		}
		return tenantSeen && poolSeen
	}, "tenant and pool must join the service snapshot")

	primary := poolStatus().Primary
	require.NotEmpty(t, primary)
	pokeMu.Lock()
	pokesBefore := pokeCount[env.ID]
	pokeMu.Unlock()

	require.NoError(t, client.Clientset.CoreV1().Pods(Namespace).
		Delete(ctx, primary, metav1.DeleteOptions{}))
	requireEventually(t, 2*time.Minute, func() bool {
		status := poolStatus()
		return status != nil && (status.ReadyInstances == 0 || status.Phase != "Cluster in healthy state")
	}, "the primary kill must reach the observed topology")
	requireEventually(t, 3*time.Minute, func() bool {
		status := poolStatus()
		return status != nil && status.ReadyInstances >= 1 && status.Phase == "Cluster in healthy state"
	}, "the pool must recover and the recovery must be observed")
	pokeMu.Lock()
	pokesAfter := pokeCount[env.ID]
	pokeMu.Unlock()
	require.Greater(t, pokesAfter, pokesBefore,
		"pool changes must fan out to the referencing environment")

	// Destructive removal: releasing drops the logical database, removes
	// the substrate objects and credentials, and closes the claim; the
	// shared dev pool survives its tenants and keeps running.
	phased, err := dbSvc.ReleaseClaim(ctx, created.ID)
	require.NoError(t, err)
	require.Equal(t, string(claim.PhaseReleasing), phased.Phase)
	teardownDeadline := time.Now().Add(3 * time.Minute)
	for {
		require.False(t, time.Now().After(teardownDeadline),
			"claim not released before deadline; last wait: %s", controller.WaitingReason(created.ID))
		requeue, err := controller.reconcileClaim(ctx, created.ID)
		if err != nil {
			t.Logf("teardown (retrying): %v", err)
		}
		current, getErr := dbSvc.GetClaim(ctx, created.ID)
		require.NoError(t, getErr)
		if claim.Phase(current.Phase) == claim.PhaseReleased {
			break
		}
		stepClaim(t, "teardown", requeue, err, claim.Phase(current.Phase), controller.WaitingReason(created.ID))
		time.Sleep(2 * time.Second)
	}
	_, err = client.Dynamic.Resource(cnpg.DatabaseGVR).Namespace(Namespace).
		Get(ctx, "db-"+utils.ShortID(created.ID), metav1.GetOptions{})
	require.True(t, apierrors.IsNotFound(err), "the Database object must be gone")
	_, err = client.Clientset.CoreV1().Secrets(Namespace).
		Get(ctx, tenant.CredentialSecret, metav1.GetOptions{})
	require.True(t, apierrors.IsNotFound(err), "the credential Secret must be gone")
	_, err = client.Clientset.CoreV1().Secrets(namespace.Name).
		Get(ctx, kubernetes.OutputSecretName("databases", "data"), metav1.GetOptions{})
	require.True(t, apierrors.IsNotFound(err), "the output mirror must be gone")
	survivor, err := dbSvc.LiveSharedCluster(ctx, "postgres", 17)
	require.NoError(t, err, "the shared dev pool survives its tenants")

	// The idle dev pool stays up: the dev substrate is always on (owner
	// decision 2026-07-31); the hibernation annotation remains an explicit
	// off so previously hibernated pools wake on their next converge.
	_, err = controller.reconcilePool(ctx, survivor.ID)
	require.NoError(t, err)
	require.Equal(t, dbstore.StateActive, survivor.State)
	cluster, err = client.Dynamic.Resource(cnpg.ClusterGVR).Namespace(Namespace).
		Get(ctx, survivor.Name, metav1.GetOptions{})
	require.NoError(t, err)
	require.Equal(t, "off", cluster.GetAnnotations()[cnpg.HibernationAnnotation])
}

// stepClaim enforces the worker-queue invariant on one live reconcile pass:
// a pass that returns neither an error nor a requeue must have settled the
// claim in a terminal phase, otherwise the controller abandoned live work and
// only an external resync would ever revive it.
func stepClaim(t *testing.T, name string, requeue time.Duration, err error, phase claim.Phase, waiting string) {
	t.Helper()
	if err != nil || requeue > 0 {
		return
	}
	switch phase {
	case claim.PhaseProvisioned, claim.PhaseReleased:
	default:
		t.Fatalf("%s abandoned unsettled claim: phase=%s wait=%q", name, phase, waiting)
	}
}

func requireEventually(t *testing.T, timeout time.Duration, condition func() bool, message string) {
	t.Helper()
	deadline := time.Now().Add(timeout)
	for {
		if condition() {
			return
		}
		if time.Now().After(deadline) {
			t.Fatal(message)
		}
		time.Sleep(500 * time.Millisecond)
	}
}

// installOperator applies the vendored CNPG manifest and waits for the
// controller, exactly like both real install paths; idempotent across runs.
func installOperator(t *testing.T, client *kube.Client) {
	t.Helper()
	ctx := context.Background()
	applier := &bundle.Applier{Client: client}
	require.NoError(t, applier.ApplyManifest(ctx, bundle.CNPGManifest()))
	require.NoError(t, applier.WaitDeploymentReady(ctx, "cnpg-system", "cnpg-controller-manager"))
}

// cleanupPlatform removes the skali-platform namespace after the test and
// verifies it is absent before it, so runs never inherit substrate state.
func cleanupPlatform(t *testing.T, client *kube.Client) {
	t.Helper()
	waitNamespaceGone(t, client, Namespace)
	t.Cleanup(func() { deleteNamespace(t, client, Namespace) })
}

func deleteNamespace(t *testing.T, client *kube.Client, name string) {
	t.Helper()
	ctx := context.Background()
	err := client.Clientset.CoreV1().Namespaces().Delete(ctx, name, metav1.DeleteOptions{})
	if apierrors.IsNotFound(err) {
		return
	}
	require.NoError(t, err)
	waitNamespaceGone(t, client, name)
}

func waitNamespaceGone(t *testing.T, client *kube.Client, name string) {
	t.Helper()
	ctx := context.Background()
	// The platform namespace drains CNPG finalizers plus the seaweed
	// workloads and their volumes, and a preceding suite's teardown may
	// still be finalizing on a loaded machine; ten minutes buys out the
	// slowest observed sequence.
	deadline := time.Now().Add(10 * time.Minute)
	for {
		_, err := client.Clientset.CoreV1().Namespaces().Get(ctx, name, metav1.GetOptions{})
		if apierrors.IsNotFound(err) {
			return
		}
		if time.Now().After(deadline) {
			t.Fatalf("namespace %s still present", name)
		}
		if err == nil {
			// A leftover namespace from an aborted run: delete and keep
			// waiting.
			_ = client.Clientset.CoreV1().Namespaces().Delete(ctx, name, metav1.DeleteOptions{})
		}
		time.Sleep(2 * time.Second)
	}
}

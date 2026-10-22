package substrate

import (
	"context"
	"fmt"
	"net/url"
	"strings"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/stretchr/testify/require"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"

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
)

// TestLivePoolConnectionExhaustion reproduces a shared pool whose ordinary
// connection slots are all taken: new application logins fail with 53300
// while the instances stay ready and an open connection keeps working.
// The connection check confirms the refusal within two rounds, opens an
// incident, and closes it again only after the slots are released and two
// checks logged in, with no change to the pool.
func TestLivePoolConnectionExhaustion(t *testing.T) {
	defer livePhase(t, "scenario")()
	config := kubetest.Config(t)
	pool := testdb.New(t)
	ctx := context.Background()

	client, err := kube.NewFromConfig(config)
	require.NoError(t, err)
	cleanupPlatform(t, client)
	installOperator(t, client)

	st := store.NewStore(pool)
	dbSvc := dbstore.New(st)
	projects := project.New(st)
	suffix := uuid.Must(uuid.NewV7()).String()[24:]
	proj, err := projects.Create(ctx, "conn"+suffix, "", uuid.Nil)
	require.NoError(t, err)
	env, err := projects.CreateEnvironment(ctx, proj.ID, "production", project.EnvironmentOptions{})
	require.NoError(t, err)

	controller := New(Deps{
		DB:       dbSvc,
		Cluster:  KubeCluster{Client: client},
		Observed: observe.NewStore(nil),
		Enqueue:  func(uuid.UUID) {},
	}, Config{Managed: false})
	namespace := kubernetes.RenderNamespace(proj.Name, "production", env.ID.String())
	t.Cleanup(func() { deleteNamespace(t, client, namespace.Name) })
	_, err = client.Apply(ctx, namespace, false)
	require.NoError(t, err)

	owner := dbstore.ServiceOwner(proj.ID, env.ID, proj.Name, "production", "data")
	created, err := dbSvc.EnsureClaim(ctx, owner, dbstore.ClaimSpec{
		Engine: "postgres", Major: 17, Isolation: "project", Availability: "single",
	})
	require.NoError(t, err)
	driveClaim(t, controller, dbSvc, created.ID)
	devPool, err := dbSvc.LiveSharedCluster(ctx, "postgres", 17)
	require.NoError(t, err)
	tenant, err := dbSvc.LiveTenant(ctx, created.ID)
	require.NoError(t, err)

	monitor := newConnectionMonitor(controller, time.Now)
	check := func() module.DatabaseConnectionsStatus {
		t.Helper()
		objects, err := monitor.round(ctx)
		require.NoError(t, err)
		for _, obj := range objects {
			if obj.Name == devPool.Name {
				return *obj.DatabaseConnections
			}
		}
		t.Fatalf("no connection check of pool %s in %v", devPool.Name, objects)
		return module.DatabaseConnectionsStatus{}
	}
	incidents := func() []store.PoolConnectionIncident {
		t.Helper()
		rows, err := dbSvc.ConnectionIncidents(ctx, devPool.ID, 10)
		require.NoError(t, err)
		return rows
	}

	// The probe role and its ident line arrive with the pool; CNPG creates
	// the role shortly after the pool turns ready.
	driveLive(t, "connection check logs in", 2*time.Minute, func(context.Context) (bool, error) {
		status := check()
		if status.State == module.ConnectionsAvailable {
			return true, nil
		}
		return false, fmt.Errorf("connection check: %s (%s)", status.State, status.Reason)
	})
	baseline := check()
	require.Equal(t, module.ConnectionsAvailable, baseline.State)
	require.Positive(t, baseline.Limit)
	require.Empty(t, incidents())

	secret, err := client.Clientset.CoreV1().Secrets(Namespace).Get(ctx, tenant.CredentialSecret, metav1.GetOptions{})
	require.NoError(t, err)
	username, password := string(secret.Data["username"]), string(secret.Data["password"])

	// An application connection opened before the pool fills up.
	address := kubetest.PortForward(t, config, Namespace, cnpg.PrimarySelector(devPool.Name), 5432)
	existing, err := pgx.Connect(ctx, (&url.URL{
		Scheme: "postgresql", User: url.UserPassword(username, password),
		Host: address, Path: "/" + tenant.DatabaseName, RawQuery: "sslmode=prefer",
	}).String())
	require.NoError(t, err)
	t.Cleanup(func() { _ = existing.Close(context.Background()) })

	// Take every ordinary slot as the tenant, the way a leaking application
	// would: sessions that log in and sleep, started inside the primary.
	// The logins go over TCP with the tenant's password, like an
	// application's; the port-forward would drop every stream at the first
	// refused one.
	quote := func(value string) string { return "'" + strings.ReplaceAll(value, "'", `'"'"'`) + "'" }
	credentials := "PGPASSWORD=" + quote(password)
	psql := fmt.Sprintf("psql -h 127.0.0.1 -U %s -d %s", quote(username), quote(tenant.DatabaseName))
	login := credentials + " " + psql
	inPrimary := func(command string) (string, error) {
		return client.ExecInPod(ctx, Namespace, cnpg.PrimarySelector(devPool.Name), cnpg.PostgresContainer,
			[]string{"sh", "-c", command})
	}
	release := func() {
		_, _ = client.ExecInPod(context.Background(), Namespace, cnpg.PrimarySelector(devPool.Name), cnpg.PostgresContainer,
			cnpg.PSQLQuery("postgres", "SELECT count(pg_terminate_backend(pid)) FROM pg_stat_activity "+
				"WHERE usename = '"+username+"' AND query LIKE '%pg_sleep%'"))
	}
	t.Cleanup(release)
	_, err = inPrimary(fmt.Sprintf(`for i in $(seq %d); do %s nohup %s -c "SELECT pg_sleep(900)" >/dev/null 2>&1 & done; sleep 5`,
		baseline.Limit+5, credentials, psql))
	require.NoError(t, err)

	driveLive(t, "the pool fills up", time.Minute, func(context.Context) (bool, error) {
		_, err := inPrimary(login + ` -c "SELECT 1"`)
		if err != nil && strings.Contains(err.Error(), "remaining connection slots are reserved") {
			t.Logf("a new login is refused: %s", execFailure(err))
			return true, nil
		}
		sessions, countErr := client.ExecInPod(ctx, Namespace, cnpg.PrimarySelector(devPool.Name), cnpg.PostgresContainer,
			cnpg.PSQLQuery("postgres", "SELECT count(*) FROM pg_stat_activity WHERE backend_type = 'client backend'"))
		return false, fmt.Errorf("login not refused (err %v); %s client sessions (err %v)", err, strings.TrimSpace(sessions), countErr)
	})

	// The evidence skali used to read stays green: the instances are
	// ready and an open connection keeps answering.
	object, err := client.Dynamic.Resource(cnpg.ClusterGVR).Namespace(Namespace).Get(ctx, devPool.Name, metav1.GetOptions{})
	require.NoError(t, err)
	ready, _, _ := unstructured.NestedInt64(object.Object, "status", "readyInstances")
	require.EqualValues(t, devPool.Instances, ready)
	var one int
	require.NoError(t, existing.QueryRow(ctx, "SELECT 1").Scan(&one))

	// One refusal is not yet a verdict; the second confirms it.
	require.Equal(t, module.ConnectionsAvailable, check().State)
	status := check()
	require.Equal(t, module.ConnectionsRefusing, status.State)
	require.Equal(t, "remaining connection slots are reserved for roles with the SUPERUSER attribute", status.Reason)
	opened := incidents()
	require.Len(t, opened, 1)
	require.Nil(t, opened[0].ClosedAt)
	require.Equal(t, ConnectionIncidentCode, opened[0].Code)

	// Releasing the slots recovers the pool without touching it, after two
	// successful logins.
	release()
	driveLive(t, "connection check recovers", time.Minute, func(context.Context) (bool, error) {
		status := check()
		if status.State == module.ConnectionsAvailable {
			return true, nil
		}
		return false, fmt.Errorf("connection check: %s (%s)", status.State, status.Reason)
	})
	closed := incidents()
	require.Len(t, closed, 1)
	require.Equal(t, opened[0].ID, closed[0].ID)
	require.NotNil(t, closed[0].ClosedAt)
	require.NoError(t, existing.QueryRow(ctx, "SELECT 1").Scan(&one), "the application connection survived throughout")
}

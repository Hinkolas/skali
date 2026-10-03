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
	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/stretchr/testify/require"
	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"

	"github.com/Hinkolas/skali/internal/claim"
	"github.com/Hinkolas/skali/internal/dbstore"
	"github.com/Hinkolas/skali/internal/kube"
	"github.com/Hinkolas/skali/internal/kubernetes"
	"github.com/Hinkolas/skali/internal/kubetest"
	"github.com/Hinkolas/skali/internal/observe"
	"github.com/Hinkolas/skali/internal/project"
	"github.com/Hinkolas/skali/internal/store"
	"github.com/Hinkolas/skali/internal/substrate/cnpg"
	"github.com/Hinkolas/skali/internal/testdb"
)

// TestLiveDatabaseCredentialRotation drives a tenant through two rotations
// and its release against a real pool: the next login role appears with
// its password and acts as the owner, both roles log in inside the window,
// the retirement closes the owner's login (first rotation) and drops a
// login role (second), ownership never moves, and teardown leaves no role
// behind. The claim worker is driven by hand, as the other live tests do.
func TestLiveDatabaseCredentialRotation(t *testing.T) {
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
	proj, err := projects.Create(ctx, "rot"+suffix, "", uuid.Nil)
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

	namespace := kubernetes.RenderNamespace(proj.Name, "production", env.ID.String())
	_, err = client.Apply(ctx, namespace, false)
	require.NoError(t, err)
	t.Cleanup(func() { deleteNamespace(t, client, namespace.Name) })

	owner := dbstore.ServiceOwner(proj.ID, env.ID, proj.Name, "production", "data")
	created, err := dbSvc.EnsureClaim(ctx, owner, dbstore.ClaimSpec{
		Engine: "postgres", Major: 17, Isolation: "project", Availability: "single",
	})
	require.NoError(t, err)
	driveClaim(t, controller, dbSvc, created.ID)

	devPool, err := dbSvc.LiveSharedCluster(ctx, "postgres", 17)
	require.NoError(t, err)
	tenant := func() *store.DatabaseTenant {
		t.Helper()
		row, err := dbSvc.LiveTenant(ctx, created.ID)
		require.NoError(t, err)
		return row
	}
	secret := func(name string) *corev1.Secret {
		t.Helper()
		row, err := client.Clientset.CoreV1().Secrets(Namespace).Get(ctx, name, metav1.GetOptions{})
		require.NoError(t, err, "secret %s", name)
		return row
	}
	secretGone := func(name string) bool {
		_, err := client.Clientset.CoreV1().Secrets(Namespace).Get(ctx, name, metav1.GetOptions{})
		return err != nil
	}
	mirror := func() *corev1.Secret {
		t.Helper()
		row, err := client.Clientset.CoreV1().Secrets(namespace.Name).
			Get(ctx, kubernetes.OutputSecretName("databases", "data"), metav1.GetOptions{})
		require.NoError(t, err)
		return row
	}
	clusterObject := func() *unstructured.Unstructured {
		t.Helper()
		object, err := client.Dynamic.Resource(cnpg.ClusterGVR).Namespace(Namespace).Get(ctx, devPool.Name, metav1.GetOptions{})
		require.NoError(t, err)
		return object
	}
	instanceUIDs := func() []string {
		t.Helper()
		pods, err := client.Clientset.CoreV1().Pods(Namespace).List(ctx, metav1.ListOptions{LabelSelector: cnpg.InstanceSelector(devPool.Name)})
		require.NoError(t, err)
		var uids []string
		for _, pod := range pods.Items {
			uids = append(uids, string(pod.UID))
		}
		return uids
	}
	// drive runs worker passes until the predicate holds, bounded.
	drive := func(what string, timeout time.Duration, done func(*store.DatabaseTenant) bool) {
		t.Helper()
		deadline := time.Now().Add(timeout)
		for {
			_, err := controller.reconcileClaim(ctx, created.ID)
			require.NoError(t, err)
			if done(tenant()) {
				return
			}
			require.True(t, time.Now().Before(deadline), "%s: timed out (waiting: %q)", what, controller.WaitingReason(created.ID))
			time.Sleep(2 * time.Second)
		}
	}

	// SQL through a port-forward to the primary, as the tenant's roles.
	address := kubetest.PortForward(t, config, Namespace, cnpg.PrimarySelector(devPool.Name), 5432)
	dsn := func(username, password string) string {
		return (&url.URL{
			Scheme: "postgresql", User: url.UserPassword(username, password),
			Host: address, Path: "/" + tenant().DatabaseName, RawQuery: "sslmode=prefer",
		}).String()
	}
	connect := func(username, password string) *pgx.Conn {
		t.Helper()
		dialCtx, cancel := context.WithTimeout(ctx, 30*time.Second)
		defer cancel()
		conn, err := pgx.Connect(dialCtx, dsn(username, password))
		require.NoError(t, err, "connect as %s", username)
		t.Cleanup(func() { _ = conn.Close(context.Background()) })
		return conn
	}
	refused := func(username, password string) bool {
		dialCtx, cancel := context.WithTimeout(ctx, 30*time.Second)
		defer cancel()
		conn, err := pgx.Connect(dialCtx, dsn(username, password))
		if err == nil {
			_ = conn.Close(ctx)
			return false
		}
		return strings.Contains(err.Error(), "password authentication failed") ||
			strings.Contains(err.Error(), "not permitted to log in")
	}
	currentUser := func(conn *pgx.Conn) string {
		t.Helper()
		var name string
		require.NoError(t, conn.QueryRow(ctx, "SELECT current_user").Scan(&name))
		return name
	}
	tableOwner := func(conn *pgx.Conn, table string) string {
		t.Helper()
		var name string
		require.NoError(t, conn.QueryRow(ctx, "SELECT tableowner FROM pg_tables WHERE tablename = $1", table).Scan(&name))
		return name
	}
	canLogin := func(conn *pgx.Conn, role string) (bool, bool) {
		t.Helper()
		var login bool
		err := conn.QueryRow(ctx, "SELECT rolcanlogin FROM pg_roles WHERE rolname = $1", role).Scan(&login)
		if err == pgx.ErrNoRows {
			return false, false
		}
		require.NoError(t, err)
		return login, true
	}
	leakAudit := func(password string) {
		t.Helper()
		auditPasswordLeak(t, ctx, pool, password)
	}

	// Version 1: the owner logs in and owns what it creates.
	v1 := tenant()
	require.Equal(t, v1.RoleName, v1.LoginRole)
	ownerSecret := secret(v1.CredentialSecret)
	ownerPassword := string(ownerSecret.Data["password"])
	require.Equal(t, v1.RoleName, string(ownerSecret.Data["username"]))
	ownerConn := connect(v1.RoleName, ownerPassword)
	_, err = ownerConn.Exec(ctx, "CREATE TABLE t1 (id int)")
	require.NoError(t, err)
	require.Equal(t, v1.RoleName, tableOwner(ownerConn, "t1"))
	podsBefore := instanceUIDs()

	// The first rotation: the next login role is taken, both roles log in,
	// the new one acts as the owner.
	result, err := controller.RotateDatabaseCredentials(ctx, env.ID, "data", 2*time.Minute)
	require.NoError(t, err)
	require.Equal(t, v1.RoleName+"_v2", result.LoginRole)
	drive("take v2", 3*time.Minute, func(row *store.DatabaseTenant) bool { return row.LoginRole == result.LoginRole })
	v2 := tenant()
	require.EqualValues(t, 2, v2.CredentialVersion)
	require.NotNil(t, v2.PreviousLoginRole)
	require.Equal(t, v1.RoleName, *v2.PreviousLoginRole)
	require.WithinDuration(t, time.Now().Add(2*time.Minute), *v2.CredentialRetireAt, 90*time.Second)
	require.Contains(t, poked, env.ID, "the version bump pokes the environment so the consumers roll")
	v2Secret := secret(v2.CredentialSecret)
	v2Password := string(v2Secret.Data["password"])
	require.Equal(t, result.LoginRole, string(v2Secret.Data["username"]))
	ready, reason := cnpg.RoleReconciled(clusterObject(), result.LoginRole, v2Secret.ResourceVersion)
	require.True(t, ready, reason)
	require.Equal(t, result.LoginRole, string(mirror().Data["username"]))
	require.Equal(t, v2Password, string(mirror().Data["password"]))
	require.Contains(t, string(mirror().Data["url"]), "postgresql://"+result.LoginRole+":")
	require.Equal(t, podsBefore, instanceUIDs(), "a role change never restarts the pool")
	leakAudit(v2Password)

	v2Conn := connect(result.LoginRole, v2Password)
	require.Equal(t, v1.RoleName, currentUser(v2Conn), "a login role's session acts as the owner")
	_, err = v2Conn.Exec(ctx, "CREATE TABLE t2 (id int)")
	require.NoError(t, err)
	require.Equal(t, v1.RoleName, tableOwner(v2Conn, "t2"), "what the application creates belongs to the owner")
	_, err = v2Conn.Exec(ctx, "DROP TABLE t1")
	require.NoError(t, err, "the restore path: the login role can drop what the owner created")
	var one int
	require.NoError(t, ownerConn.QueryRow(ctx, "SELECT 1").Scan(&one), "the owner's session survives inside the window")
	connect(v1.RoleName, ownerPassword).Close(ctx)
	login, exists := canLogin(v2Conn, v1.RoleName)
	require.True(t, exists && login, "the owner keeps its login inside the window")

	// A pass inside the window changes nothing.
	_, err = controller.reconcileClaim(ctx, created.ID)
	require.NoError(t, err)
	require.EqualValues(t, 2, tenant().CredentialVersion)
	require.NotNil(t, tenant().PreviousLoginRole)

	// Retire now: the owner's session is terminated, its login closed, its
	// Secret gone, the row cleared.
	require.NoError(t, controller.RetireDatabaseCredentials(ctx, env.ID, "data"))
	drive("retire the owner's login", 2*time.Minute, func(row *store.DatabaseTenant) bool { return row.PreviousLoginRole == nil })
	require.Error(t, ownerConn.QueryRow(ctx, "SELECT 1").Scan(&one), "the retired role's session is terminated")
	require.True(t, refused(v1.RoleName, ownerPassword), "the owner no longer logs in")
	login, exists = canLogin(v2Conn, v1.RoleName)
	require.True(t, exists)
	require.False(t, login)
	require.True(t, secretGone(ownerSecret.Name))
	require.Nil(t, tenant().CredentialRetireAt)
	require.Equal(t, v1.RoleName, currentUser(v2Conn), "the current session is untouched")
	require.Equal(t, podsBefore, instanceUIDs())

	// The second rotation retires a login role: dropped for good, the
	// tables still the owner's, the new session acting as the owner.
	result3, err := controller.RotateDatabaseCredentials(ctx, env.ID, "data", 2*time.Minute)
	require.NoError(t, err)
	require.Equal(t, v1.RoleName+"_v3", result3.LoginRole)
	drive("take v3", 3*time.Minute, func(row *store.DatabaseTenant) bool { return row.LoginRole == result3.LoginRole })
	v3Secret := secret(tenant().CredentialSecret)
	v3Conn := connect(result3.LoginRole, string(v3Secret.Data["password"]))
	require.Equal(t, v1.RoleName, currentUser(v3Conn))
	require.NoError(t, controller.RetireDatabaseCredentials(ctx, env.ID, "data"))
	drive("drop v2", 3*time.Minute, func(row *store.DatabaseTenant) bool { return row.PreviousLoginRole == nil })
	require.Error(t, v2Conn.QueryRow(ctx, "SELECT 1").Scan(&one))
	require.True(t, refused(result.LoginRole, v2Password))
	_, exists = canLogin(v3Conn, result.LoginRole)
	require.False(t, exists, "a retired login role is dropped")
	require.Equal(t, v1.RoleName, tableOwner(v3Conn, "t2"))
	require.True(t, secretGone(v2Secret.Name))
	require.EqualValues(t, 3, tenant().CredentialVersion)

	// The pool renders the owner without a login and the current role
	// under it, nothing else of this tenant.
	roles, _, _ := unstructured.NestedSlice(clusterObject().Object, "spec", "managed", "roles")
	names := map[string]map[string]any{}
	for _, entry := range roles {
		role := entry.(map[string]any)
		names[role["name"].(string)] = role
	}
	require.Equal(t, false, names[v1.RoleName]["login"])
	require.Equal(t, true, names[result3.LoginRole]["login"])
	require.NotContains(t, names, result.LoginRole)

	// Release: database, Secrets and every role gone, checked through the
	// same exec channel the worker uses.
	released, err := dbSvc.ReleaseClaim(ctx, created.ID)
	require.NoError(t, err)
	require.Equal(t, string(claim.PhaseReleasing), released.Phase)
	deadline := time.Now().Add(4 * time.Minute)
	for {
		_, err := controller.reconcileClaim(ctx, created.ID)
		require.NoError(t, err)
		row, err := dbSvc.GetClaim(ctx, created.ID)
		require.NoError(t, err)
		if claim.Phase(row.Phase) == claim.PhaseReleased {
			break
		}
		require.True(t, time.Now().Before(deadline), "release timed out (waiting: %q)", controller.WaitingReason(created.ID))
		time.Sleep(2 * time.Second)
	}
	out, err := client.ExecInPod(ctx, Namespace, cnpg.PrimarySelector(devPool.Name), cnpg.PostgresContainer,
		cnpg.PSQLCommand("postgres", fmt.Sprintf("SELECT count(*) FROM pg_roles WHERE rolname LIKE '%s%%'", v1.RoleName)))
	require.NoError(t, err)
	require.Equal(t, "0", strings.TrimSpace(strings.Split(strings.TrimSpace(out), "\n")[len(strings.Split(strings.TrimSpace(out), "\n"))-1]),
		"no role of the released tenant remains: %q", out)
	require.True(t, secretGone(v3Secret.Name))
}

// auditPasswordLeak scans every durable text-ish column of the control
// plane for the password: it may exist only in Kubernetes Secrets.
func auditPasswordLeak(t *testing.T, ctx context.Context, pool *pgxpool.Pool, password string) {
	t.Helper()
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
}

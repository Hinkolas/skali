package substrate

import (
	"context"
	"errors"
	"net/url"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"
	"github.com/stretchr/testify/require"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"

	"github.com/Hinkolas/skali/internal/dbstore"
	"github.com/Hinkolas/skali/internal/kube"
	"github.com/Hinkolas/skali/internal/kubernetes"
	"github.com/Hinkolas/skali/internal/kubetest"
	"github.com/Hinkolas/skali/internal/observe"
	"github.com/Hinkolas/skali/internal/project"
	"github.com/Hinkolas/skali/internal/store"
	"github.com/Hinkolas/skali/internal/testdb"
)

// TestLiveVectorExtension proves pgvector end to end on a real pool: a claim
// declaring `vector` gets a usable extension (vector column, similarity
// query, hnsw index), a claim on the same pool without it does not, and
// the tenant role cannot create it itself. Every instance of a pool runs the
// same catalog image, so a replica promoted later carries the same files;
// the dev-mode test cluster runs single-instance pools, which is why that
// is stated rather than exercised here. Requires TEST_KUBECONFIG and
// TEST_DATABASE_URL.
func TestLiveVectorExtension(t *testing.T) {
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
	proj, err := projects.Create(ctx, "vec"+suffix, "", uuid.Nil)
	require.NoError(t, err)
	env, err := projects.CreateEnvironment(ctx, proj.ID, "production", project.EnvironmentOptions{})
	require.NoError(t, err)

	controller := New(Deps{
		DB:       dbSvc,
		Cluster:  KubeCluster{Client: client},
		Observed: observe.NewStore(nil),
		Enqueue:  func(uuid.UUID) {},
	}, Config{Managed: false})

	cleanupPlatform(t, client)

	namespace := kubernetes.RenderNamespace(proj.Name, "production", env.ID.String())
	_, err = client.Apply(ctx, namespace, false)
	require.NoError(t, err)
	t.Cleanup(func() { deleteNamespace(t, client, namespace.Name) })

	// Two claims on the same pool: "search" asks for vector, "plain" does not.
	searchOwner := dbstore.ServiceOwner(proj.ID, env.ID, proj.Name, "production", "search")
	search, err := dbSvc.EnsureClaim(ctx, searchOwner, dbstore.ClaimSpec{
		Engine: "postgres", Major: DefaultMajor,
		Isolation: "shared", Availability: "single",
		Extensions: []string{"vector"},
	})
	require.NoError(t, err)
	plainOwner := dbstore.ServiceOwner(proj.ID, env.ID, proj.Name, "production", "plain")
	plain, err := dbSvc.EnsureClaim(ctx, plainOwner, dbstore.ClaimSpec{
		Engine: "postgres", Major: DefaultMajor,
		Isolation: "shared", Availability: "single",
	})
	require.NoError(t, err)
	driveClaim(t, controller, dbSvc, search.ID)
	driveClaim(t, controller, dbSvc, plain.ID)

	searchTenant, err := dbSvc.LiveTenant(ctx, search.ID)
	require.NoError(t, err)
	plainTenant, err := dbSvc.LiveTenant(ctx, plain.ID)
	require.NoError(t, err)
	require.Equal(t, searchTenant.ClusterID, plainTenant.ClusterID, "both claims share the dev pool")
	devPool, err := dbSvc.LiveSharedCluster(ctx, "postgres", DefaultMajor)
	require.NoError(t, err)

	// Reach the primary the way an application would, as the tenant role
	// with the mirrored outputs, over a tunnel to the primary pod.
	address := kubetest.PortForward(t, config, Namespace,
		"cnpg.io/cluster="+devPool.Name+",cnpg.io/instanceRole=primary", 5432)
	connect := func(service string) *pgx.Conn {
		mirror, err := client.Clientset.CoreV1().Secrets(namespace.Name).
			Get(ctx, kubernetes.OutputSecretName("databases", service), metav1.GetOptions{})
		require.NoError(t, err)
		dsn := url.URL{
			Scheme:   "postgresql",
			User:     url.UserPassword(string(mirror.Data["username"]), string(mirror.Data["password"])),
			Host:     address,
			Path:     "/" + string(mirror.Data["name"]),
			RawQuery: "sslmode=prefer",
		}
		dialCtx, cancel := context.WithTimeout(ctx, 30*time.Second)
		defer cancel()
		conn, err := pgx.Connect(dialCtx, dsn.String())
		require.NoError(t, err, "connect to %s as the tenant role", service)
		t.Cleanup(func() { _ = conn.Close(context.Background()) })
		return conn
	}

	searchDB := connect("search")
	var version string
	require.NoError(t, searchDB.QueryRow(ctx,
		`SELECT extversion FROM pg_extension WHERE extname = 'vector'`).Scan(&version))
	require.NotEmpty(t, version, "CNPG created the requested extension")
	t.Logf("pgvector %s on %s", version, devPool.Image)

	// Storage, similarity and an index, as the application role.
	_, err = searchDB.Exec(ctx, `CREATE TABLE items (id int PRIMARY KEY, embedding vector(3))`)
	require.NoError(t, err)
	_, err = searchDB.Exec(ctx, `INSERT INTO items (id, embedding) VALUES
		(1, $1::text::vector), (2, $2::text::vector), (3, $3::text::vector)`,
		"[1,0,0]", "[0,1,0]", "[0.9,0.1,0]")
	require.NoError(t, err)
	nearest := func() int {
		var id int
		require.NoError(t, searchDB.QueryRow(ctx,
			`SELECT id FROM items ORDER BY embedding <=> $1::text::vector LIMIT 1`, "[1,0.05,0]").Scan(&id))
		return id
	}
	require.Equal(t, 1, nearest(), "cosine distance ranks the aligned vector first")
	_, err = searchDB.Exec(ctx, `CREATE INDEX items_embedding_idx ON items USING hnsw (embedding vector_cosine_ops)`)
	require.NoError(t, err)
	require.Equal(t, 1, nearest(), "the hnsw index serves the same answer")

	// The migration idiom keeps working as a no-op; creating it outright
	// reports that it already exists rather than a privilege failure.
	_, err = searchDB.Exec(ctx, `CREATE EXTENSION IF NOT EXISTS vector`)
	require.NoError(t, err)

	// The pool ships the files, but only the declaring database gets the
	// extension, and the tenant role cannot grant it to itself.
	plainDB := connect("plain")
	var count int
	require.NoError(t, plainDB.QueryRow(ctx,
		`SELECT count(*) FROM pg_extension WHERE extname = 'vector'`).Scan(&count))
	require.Zero(t, count, "a database that does not declare vector must not have it")
	_, err = plainDB.Exec(ctx, `CREATE EXTENSION vector`)
	var pgErr *pgconn.PgError
	require.True(t, errors.As(err, &pgErr), "CREATE EXTENSION as the tenant role must fail, got %v", err)
	require.Equal(t, "42501", pgErr.Code, "insufficient privilege: pgvector is not a trusted extension")
}

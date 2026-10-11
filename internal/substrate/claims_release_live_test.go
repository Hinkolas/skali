package substrate

import (
	"context"
	"strings"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/stretchr/testify/require"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"

	"github.com/Hinkolas/skali/internal/claim"
	"github.com/Hinkolas/skali/internal/compiler"
	"github.com/Hinkolas/skali/internal/dbstore"
	"github.com/Hinkolas/skali/internal/kube"
	"github.com/Hinkolas/skali/internal/kubernetes"
	"github.com/Hinkolas/skali/internal/kubetest"
	"github.com/Hinkolas/skali/internal/observe"
	"github.com/Hinkolas/skali/internal/project"
	"github.com/Hinkolas/skali/internal/reconcile"
	"github.com/Hinkolas/skali/internal/revision"
	"github.com/Hinkolas/skali/internal/store"
	"github.com/Hinkolas/skali/internal/substrate/cnpg"
	"github.com/Hinkolas/skali/internal/testdb"
	"github.com/Hinkolas/skali/internal/utils"
)

// TestLiveDroppedDatabaseWaitsForActivation drives a real database through
// a rollout that drops it: while the rollout is in flight (Ensure without
// Release) the database, its rows, and its CNPG object stay through claim
// passes, a fallback finds it provisioned with its data, and only a pass
// that allows the release tears it down. Requires TEST_KUBECONFIG and
// TEST_DATABASE_URL.
func TestLiveDroppedDatabaseWaitsForActivation(t *testing.T) {
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
	proj, err := projects.Create(ctx, "drop"+suffix, "", uuid.Nil)
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

	input := func(databases map[string]compiler.DatabaseClaim, release bool) reconcile.ClaimEnsureInput {
		return reconcile.ClaimEnsureInput{
			ProjectID: proj.ID, EnvironmentID: env.ID, Release: release,
			Revision: &revision.Revision{Project: proj.Name, Environment: "production",
				Definition: compiler.ProjectDefinition{Databases: databases}},
		}
	}
	declared := map[string]compiler.DatabaseClaim{
		"data": {Engine: "postgres", Version: "17", Isolation: "project", Availability: "single"},
	}

	_, err = controller.Ensure(ctx, input(declared, false))
	require.NoError(t, err)
	live, err := dbSvc.LiveServiceClaim(ctx, env.ID, "data")
	require.NoError(t, err)
	driveClaim(t, controller, dbSvc, live.ID)
	tenant, err := dbSvc.LiveTenant(ctx, live.ID)
	require.NoError(t, err)
	devPool, err := dbSvc.LiveSharedCluster(ctx, "postgres", 17)
	require.NoError(t, err)
	sql := func(query string) string {
		t.Helper()
		out, err := client.ExecInPod(ctx, Namespace, cnpg.PrimarySelector(devPool.Name), cnpg.PostgresContainer,
			cnpg.PSQLQuery(tenant.DatabaseName, query))
		require.NoError(t, err, out)
		return strings.TrimSpace(out)
	}
	sql("CREATE TABLE kept (value text); INSERT INTO kept VALUES ('survives')")
	databaseObject := func() error {
		_, err := client.Dynamic.Resource(cnpg.DatabaseGVR).Namespace(Namespace).
			Get(ctx, "db-"+utils.ShortID(live.ID), metav1.GetOptions{})
		return err
	}

	// The rollout of a revision without the database: retained, repaired
	// as usual, never released.
	states, err := controller.Ensure(ctx, input(nil, false))
	require.NoError(t, err)
	require.Equal(t, []reconcile.ClaimState{{Service: "databases.data", Retained: true}}, states)
	for range 3 {
		_, err := controller.reconcileClaim(ctx, live.ID)
		require.NoError(t, err)
	}
	row, err := dbSvc.GetClaim(ctx, live.ID)
	require.NoError(t, err)
	require.Equal(t, claim.PhaseProvisioned, claim.Phase(row.Phase))
	require.NoError(t, databaseObject())
	require.Equal(t, "survives", sql("SELECT value FROM kept"))

	// The rollout falls back: the previous revision finds its database.
	states, err = controller.Ensure(ctx, input(declared, true))
	require.NoError(t, err)
	require.Equal(t, []reconcile.ClaimState{{Service: "databases.data", Provisioned: true}}, states)
	require.Equal(t, "survives", sql("SELECT value FROM kept"))

	// The revision without it activates: now it goes.
	states, err = controller.Ensure(ctx, input(nil, true))
	require.NoError(t, err)
	require.Empty(t, states)
	driveLive(t, "database release", 3*time.Minute, func(ctx context.Context) (bool, error) {
		return databaseClaimPass(ctx, controller, dbSvc, live.ID, claim.PhaseReleased)
	})
	require.True(t, apierrors.IsNotFound(databaseObject()), "the Database object must be gone")
}

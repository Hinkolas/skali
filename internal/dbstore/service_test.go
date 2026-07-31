package dbstore

import (
	"context"
	"testing"

	"github.com/google/uuid"
	"github.com/stretchr/testify/require"

	"github.com/Hinkolas/skali/internal/claim"
	"github.com/Hinkolas/skali/internal/lifecycle"
	"github.com/Hinkolas/skali/internal/project"
	"github.com/Hinkolas/skali/internal/store"
	"github.com/Hinkolas/skali/internal/testdb"
)

func TestClusterStateMachineInvariants(t *testing.T) {
	t.Parallel()
	require.NoError(t, lifecycle.Verify(ClusterStates))
	require.Equal(t, StateActive, ClusterStates.Initial())
	require.True(t, ClusterStates.Terminal(StateReleased))
}

type fixture struct {
	st            *store.Store
	svc           *Service
	projectID     uuid.UUID
	environmentID uuid.UUID
}

func newFixture(t *testing.T) *fixture {
	t.Helper()
	ctx := context.Background()
	pool := testdb.New(t)
	st := store.NewStore(pool)
	projects := project.New(st)
	proj, err := projects.Create(ctx, "demo", "")
	require.NoError(t, err)
	env, err := projects.CreateEnvironment(ctx, proj.ID, "production")
	require.NoError(t, err)
	return &fixture{st: st, svc: New(st), projectID: proj.ID, environmentID: env.ID}
}

func (f *fixture) owner(serviceKey string) Owner {
	return ServiceOwner(f.projectID, f.environmentID, "demo", "production", serviceKey)
}

func spec() ClaimSpec {
	return ClaimSpec{
		Engine:       "postgres",
		Major:        17,
		Isolation:    "shared",
		Availability: "single",
		StorageBytes: 1 << 30,
		Extensions:   []string{"pg_trgm"},
	}
}

func (f *fixture) cluster(t *testing.T, name string) *store.DatabaseCluster {
	t.Helper()
	pool, err := f.svc.CreateCluster(context.Background(), ClusterInput{
		Name:         name,
		Engine:       "postgres",
		Major:        17,
		Class:        ClassShared,
		Instances:    1,
		StorageBytes: 1 << 30,
		Image:        "test-image:17",
	})
	require.NoError(t, err)
	return pool
}

func TestClaimPhaseWalk(t *testing.T) {
	t.Parallel()
	f := newFixture(t)
	ctx := context.Background()

	created, err := f.svc.EnsureClaim(ctx, f.owner("data"), spec())
	require.NoError(t, err)
	require.Equal(t, string(claim.PhasePending), created.Phase)
	require.Equal(t, "project/demo/environment/production/service/data", created.OwnerRef)

	pool := f.cluster(t, "pg17-shared")
	placement, err := f.svc.BindClaim(ctx, created.ID, pool.ID)
	require.NoError(t, err)
	require.Equal(t, pool.ID, placement.ClusterID)
	bound, err := f.svc.GetClaim(ctx, created.ID)
	require.NoError(t, err)
	require.Equal(t, string(claim.PhaseBound), bound.Phase)

	tenant, err := f.svc.RecordTenant(ctx, TenantInput{
		ClaimID:          created.ID,
		ClusterID:        pool.ID,
		DatabaseName:     "db_data",
		RoleName:         "u_data",
		CredentialSecret: "cred-data",
		Host:             "pg17-shared-rw.skali-platform.svc.cluster.local",
		Port:             5432,
	})
	require.NoError(t, err)

	provisioned, err := f.svc.TransitionClaim(ctx, created.ID, claim.PhaseProvisioned)
	require.NoError(t, err)
	require.Equal(t, string(claim.PhaseProvisioned), provisioned.Phase)

	releasing, err := f.svc.ReleaseClaim(ctx, created.ID)
	require.NoError(t, err)
	require.Equal(t, string(claim.PhaseReleasing), releasing.Phase)
	// Releasing is one-way: a repeat release is a no-op, not an error.
	again, err := f.svc.ReleaseClaim(ctx, created.ID)
	require.NoError(t, err)
	require.Equal(t, string(claim.PhaseReleasing), again.Phase)

	require.NoError(t, f.svc.CompleteClaimRelease(ctx, created.ID))
	released, err := f.svc.GetClaim(ctx, created.ID)
	require.NoError(t, err)
	require.Equal(t, string(claim.PhaseReleased), released.Phase)
	_, err = f.svc.LiveTenant(ctx, created.ID)
	require.ErrorIs(t, err, ErrNotFound)
	_, err = f.svc.ActivePlacement(ctx, created.ID)
	require.ErrorIs(t, err, ErrNotFound)
	// The released tenant identity is history, not current truth.
	count, err := f.svc.CountClusterTenants(ctx, pool.ID)
	require.NoError(t, err)
	require.Zero(t, count)
	_ = tenant

	// The released claim frees the owner slot: ensuring again creates a new
	// identity instead of resurrecting the old one.
	next, err := f.svc.EnsureClaim(ctx, f.owner("data"), spec())
	require.NoError(t, err)
	require.NotEqual(t, created.ID, next.ID)
	require.Equal(t, string(claim.PhasePending), next.Phase)
}

func TestEnsureClaimIdempotentAndDrift(t *testing.T) {
	t.Parallel()
	f := newFixture(t)
	ctx := context.Background()

	first, err := f.svc.EnsureClaim(ctx, f.owner("data"), spec())
	require.NoError(t, err)
	same, err := f.svc.EnsureClaim(ctx, f.owner("data"), spec())
	require.NoError(t, err)
	require.Equal(t, first.ID, same.ID)
	require.Equal(t, first.UpdatedAt, same.UpdatedAt)

	// Mutable drift folds into the live claim.
	drifted := spec()
	drifted.Availability = "asynchronous"
	drifted.StorageBytes = 2 << 30
	drifted.Extensions = []string{"pg_trgm", "citext"}
	updated, err := f.svc.EnsureClaim(ctx, f.owner("data"), drifted)
	require.NoError(t, err)
	require.Equal(t, first.ID, updated.ID)
	require.Equal(t, "asynchronous", updated.Availability)
	require.Equal(t, int64(2<<30), updated.StorageBytes)
	require.Equal(t, []string{"citext", "pg_trgm"}, Extensions(*updated))

	// The physical home is immutable on a live claim.
	conflicting := spec()
	conflicting.Isolation = "dedicated"
	_, err = f.svc.EnsureClaim(ctx, f.owner("data"), conflicting)
	require.ErrorIs(t, err, ErrSpecConflict)
	conflicting = spec()
	conflicting.Major = 18
	_, err = f.svc.EnsureClaim(ctx, f.owner("data"), conflicting)
	require.ErrorIs(t, err, ErrSpecConflict)
}

func TestReleaseUnplacedClaimReleasesDirectly(t *testing.T) {
	t.Parallel()
	f := newFixture(t)
	ctx := context.Background()

	created, err := f.svc.EnsureClaim(ctx, f.owner("data"), spec())
	require.NoError(t, err)
	released, err := f.svc.ReleaseClaim(ctx, created.ID)
	require.NoError(t, err)
	require.Equal(t, string(claim.PhaseReleased), released.Phase)
}

func TestInvalidTransitionsRejected(t *testing.T) {
	t.Parallel()
	f := newFixture(t)
	ctx := context.Background()

	created, err := f.svc.EnsureClaim(ctx, f.owner("data"), spec())
	require.NoError(t, err)
	_, err = f.svc.TransitionClaim(ctx, created.ID, claim.PhaseProvisioned)
	require.ErrorIs(t, err, ErrInvalidTransition)

	_, err = f.svc.ReleaseClaim(ctx, created.ID)
	require.NoError(t, err)
	pool := f.cluster(t, "pg17-shared")
	_, err = f.svc.BindClaim(ctx, created.ID, pool.ID)
	require.ErrorIs(t, err, ErrInvalidTransition)

	_, err = f.svc.TransitionClaim(ctx, uuid.Max, claim.PhaseBound)
	require.ErrorIs(t, err, ErrNotFound)
}

func TestPlacementRebindAndUnbind(t *testing.T) {
	t.Parallel()
	f := newFixture(t)
	ctx := context.Background()

	created, err := f.svc.EnsureClaim(ctx, f.owner("data"), spec())
	require.NoError(t, err)
	first := f.cluster(t, "pg17-shared")

	placement, err := f.svc.BindClaim(ctx, created.ID, first.ID)
	require.NoError(t, err)
	same, err := f.svc.BindClaim(ctx, created.ID, first.ID)
	require.NoError(t, err)
	require.Equal(t, placement.ID, same.ID, "re-bind to the same cluster is idempotent")

	second, err := f.svc.CreateCluster(ctx, ClusterInput{
		Name: "pg17-ded-x", Engine: "postgres", Major: 17, Class: ClassDedicated,
		ClaimID: created.ID, Instances: 1, StorageBytes: 1 << 30, Image: "test-image:17",
	})
	require.NoError(t, err)
	moved, err := f.svc.BindClaim(ctx, created.ID, second.ID)
	require.NoError(t, err)
	require.NotEqual(t, placement.ID, moved.ID)
	active, err := f.svc.ActivePlacement(ctx, created.ID)
	require.NoError(t, err)
	require.Equal(t, second.ID, active.ClusterID, "relocation supersedes the placement")

	require.NoError(t, f.svc.UnbindClaim(ctx, created.ID))
	pending, err := f.svc.GetClaim(ctx, created.ID)
	require.NoError(t, err)
	require.Equal(t, string(claim.PhasePending), pending.Phase)
	_, err = f.svc.ActivePlacement(ctx, created.ID)
	require.ErrorIs(t, err, ErrNotFound)
}

func TestClusterLifecycleAndPacking(t *testing.T) {
	t.Parallel()
	f := newFixture(t)
	ctx := context.Background()

	pool := f.cluster(t, "pg17-shared")

	// Dumb packing: one live shared pool per (engine, major).
	_, err := f.svc.CreateCluster(ctx, ClusterInput{
		Name: "pg17-shared-b", Engine: "postgres", Major: 17, Class: ClassShared,
		Instances: 1, StorageBytes: 1 << 30, Image: "test-image:17",
	})
	require.Error(t, err)

	found, err := f.svc.LiveSharedCluster(ctx, "postgres", 17)
	require.NoError(t, err)
	require.Equal(t, pool.ID, found.ID)

	// The retired hibernated state is rejected outright; release is the
	// only way out of active.
	_, err = f.svc.TransitionCluster(ctx, pool.ID, "hibernated")
	require.Error(t, err)
	_, err = f.svc.TransitionCluster(ctx, pool.ID, StateReleased)
	require.ErrorIs(t, err, ErrInvalidTransition)
	_, err = f.svc.TransitionCluster(ctx, pool.ID, StateReleasing)
	require.NoError(t, err)
	_, err = f.svc.TransitionCluster(ctx, pool.ID, StateReleased)
	require.NoError(t, err)

	// A released pool frees its packing slot and its name.
	_, err = f.svc.LiveSharedCluster(ctx, "postgres", 17)
	require.ErrorIs(t, err, ErrNotFound)
	replacement := f.cluster(t, "pg17-shared")
	require.NotEqual(t, pool.ID, replacement.ID)
}

func TestClaimHistorySurvivesEnvironmentPurge(t *testing.T) {
	t.Parallel()
	f := newFixture(t)
	ctx := context.Background()

	created, err := f.svc.EnsureClaim(ctx, f.owner("data"), spec())
	require.NoError(t, err)
	_, err = f.svc.ReleaseClaim(ctx, created.ID)
	require.NoError(t, err)

	// Environment purge deletes the environment row (cascading its target);
	// claim identity is retained for history through soft references.
	_, err = f.st.Pool.Exec(ctx, "DELETE FROM environments WHERE id = $1", f.environmentID)
	require.NoError(t, err)
	kept, err := f.svc.GetClaim(ctx, created.ID)
	require.NoError(t, err)
	require.Equal(t, "project/demo/environment/production/service/data", kept.OwnerRef)
	require.Equal(t, string(claim.PhaseReleased), kept.Phase)
}

func TestTenantIdentityIsImmutable(t *testing.T) {
	t.Parallel()
	f := newFixture(t)
	ctx := context.Background()

	pool := f.cluster(t, "pg17-shared")
	created, err := f.svc.EnsureClaim(ctx, f.owner("data"), spec())
	require.NoError(t, err)
	_, err = f.svc.BindClaim(ctx, created.ID, pool.ID)
	require.NoError(t, err)

	tenant, err := f.svc.RecordTenant(ctx, TenantInput{
		ClaimID: created.ID, ClusterID: pool.ID,
		DatabaseName: "db_data", RoleName: "u_data",
		CredentialSecret: "cred-data", Host: "h", Port: 5432,
	})
	require.NoError(t, err)
	repeat, err := f.svc.RecordTenant(ctx, TenantInput{
		ClaimID: created.ID, ClusterID: pool.ID,
		DatabaseName: "db_other", RoleName: "u_other",
		CredentialSecret: "cred-other", Host: "h", Port: 5432,
	})
	require.NoError(t, err)
	require.Equal(t, tenant.ID, repeat.ID)
	require.Equal(t, "db_data", repeat.DatabaseName, "generated identity never changes once recorded")
	require.EqualValues(t, 1, tenant.CredentialVersion)

	require.NoError(t, f.svc.BumpCredentialVersion(ctx, tenant.ID))
	live, err := f.svc.LiveTenant(ctx, created.ID)
	require.NoError(t, err)
	require.EqualValues(t, 2, live.CredentialVersion)
	require.Equal(t, "cred-data", live.CredentialSecret)

	tenants, err := f.svc.ListClusterTenants(ctx, pool.ID)
	require.NoError(t, err)
	require.Len(t, tenants, 1)
}

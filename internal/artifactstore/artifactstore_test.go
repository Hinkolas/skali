package artifactstore

import (
	"context"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/stretchr/testify/require"

	"github.com/Hinkolas/skali/internal/revision"
	"github.com/Hinkolas/skali/internal/store"
	"github.com/Hinkolas/skali/internal/testdb"
)

const testDigest = "sha256:aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa"

func newTestService(t *testing.T) (*Service, *store.Store) {
	t.Helper()
	pool := testdb.New(t)
	st := store.NewStore(pool)
	return New(st), st
}

func TestPhaseGuards(t *testing.T) {
	t.Parallel()
	svc, _ := newTestService(t)
	ctx := context.Background()

	record, err := svc.CreatePending(ctx, Pending{Kind: revision.KindImport, Upstream: "ghcr.io/example/api:1.0.0"})
	require.NoError(t, err)
	require.Equal(t, "pending", record.Phase)

	require.NoError(t, svc.Verify(ctx, record.ID, "registry.local/cache/example", testDigest, nil))
	got, err := svc.Get(ctx, record.ID)
	require.NoError(t, err)
	require.Equal(t, "verified", got.Phase)
	require.NotNil(t, got.VerifiedAt)

	// Verification is final; abandoning verified content is refused.
	require.ErrorIs(t, svc.Abandon(ctx, record.ID), ErrInvalidTransition)
	// Re-verifying is also refused (no self-transitions).
	require.ErrorIs(t, svc.Verify(ctx, record.ID, "r", testDigest, nil), ErrInvalidTransition)

	// Abandoned records cannot be verified or evicted.
	abandoned, err := svc.CreatePending(ctx, Pending{Kind: revision.KindImport})
	require.NoError(t, err)
	require.NoError(t, svc.Abandon(ctx, abandoned.ID))
	require.ErrorIs(t, svc.Verify(ctx, abandoned.ID, "r", testDigest, nil), ErrInvalidTransition)
	require.ErrorIs(t, svc.Evict(ctx, abandoned.ID), ErrInvalidTransition)

	require.ErrorIs(t, svc.Abandon(ctx, uuid.New()), ErrNotFound)
}

func TestEvictBlockedByLeaseAndSafetyWindow(t *testing.T) {
	t.Parallel()
	svc, st := newTestService(t)
	ctx := context.Background()

	// An import artifact with a lease cannot be evicted; without one it can.
	imported, err := svc.CreatePending(ctx, Pending{Kind: revision.KindImport})
	require.NoError(t, err)
	require.NoError(t, svc.Verify(ctx, imported.ID, "registry.local/cache/x", testDigest, nil))

	// Fabricate a revision row to lease against (deploy owns real ones).
	projectID, environmentID := seedEnvironment(t, st)
	revisionID := seedRevision(t, st, projectID, environmentID)
	require.NoError(t, st.WithTx(ctx, func(q *store.Queries) error {
		return svc.LeaseTx(ctx, q, revisionID, []uuid.UUID{imported.ID})
	}))
	require.ErrorIs(t, svc.Evict(ctx, imported.ID), ErrLeased)

	_, err = st.Pool.Exec(ctx, "DELETE FROM artifact_leases WHERE artifact_id = $1", imported.ID)
	require.NoError(t, err)
	require.NoError(t, svc.Evict(ctx, imported.ID))

	// A release (build) artifact holds the safety window even unleased.
	built, err := svc.CreatePending(ctx, Pending{Kind: revision.KindBuildLocal})
	require.NoError(t, err)
	require.NoError(t, svc.Verify(ctx, built.ID, "registry.local/skali/x", testDigest, nil))
	require.ErrorIs(t, svc.Evict(ctx, built.ID), ErrSafetyWindow)

	_, err = st.Pool.Exec(ctx,
		"UPDATE artifacts SET verified_at = now() - interval '73 hours' WHERE id = $1", built.ID)
	require.NoError(t, err)
	require.NoError(t, svc.Evict(ctx, built.ID))
}

func TestSweepPending(t *testing.T) {
	t.Parallel()
	svc, st := newTestService(t)
	ctx := context.Background()

	stale, err := svc.CreatePending(ctx, Pending{Kind: revision.KindImport})
	require.NoError(t, err)
	fresh, err := svc.CreatePending(ctx, Pending{Kind: revision.KindImport})
	require.NoError(t, err)
	_, err = st.Pool.Exec(ctx,
		"UPDATE artifacts SET created_at = now() - interval '2 days' WHERE id = $1", stale.ID)
	require.NoError(t, err)

	swept, err := svc.SweepPending(ctx, 24*time.Hour)
	require.NoError(t, err)
	require.Equal(t, int64(1), swept)
	got, err := svc.Get(ctx, stale.ID)
	require.NoError(t, err)
	require.Equal(t, "abandoned", got.Phase)
	got, err = svc.Get(ctx, fresh.ID)
	require.NoError(t, err)
	require.Equal(t, "pending", got.Phase)
}

func seedEnvironment(t *testing.T, st *store.Store) (uuid.UUID, uuid.UUID) {
	t.Helper()
	ctx := context.Background()
	projectID, environmentID := uuid.New(), uuid.New()
	_, err := st.Pool.Exec(ctx, "INSERT INTO projects (id, name) VALUES ($1, 'demo')", projectID)
	require.NoError(t, err)
	_, err = st.Pool.Exec(ctx,
		"INSERT INTO environments (id, project_id, name) VALUES ($1, $2, 'production')", environmentID, projectID)
	require.NoError(t, err)
	return projectID, environmentID
}

func seedRevision(t *testing.T, st *store.Store, projectID, environmentID uuid.UUID) uuid.UUID {
	t.Helper()
	ctx := context.Background()
	definitionVersionID, revisionID := uuid.New(), uuid.New()
	_, err := st.Pool.Exec(ctx, `INSERT INTO definition_versions
		(id, project_id, schema_version, definition_hash, definition, source, format, compiler_version)
		VALUES ($1, $2, '1', 'hash', '{}', '', 'yaml', 'test')`, definitionVersionID, projectID)
	require.NoError(t, err)
	_, err = st.Pool.Exec(ctx, `INSERT INTO revisions
		(id, project_id, environment_id, definition_version_id, schema_version,
		 checksum, definition_hash, values_hash, compiler_version, document)
		VALUES ($1, $2, $3, $4, '1', 'checksum', 'hash', 'vhash', 'test', '{}')`,
		revisionID, projectID, environmentID, definitionVersionID)
	require.NoError(t, err)
	return revisionID
}

package valuestore

import (
	"context"
	"strings"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/stretchr/testify/require"

	"github.com/Hinkolas/skali/internal/crypt"
	"github.com/Hinkolas/skali/internal/project"
	"github.com/Hinkolas/skali/internal/store"
	"github.com/Hinkolas/skali/internal/testdb"
)

const testSecret = "0123456789abcdef0123456789abcdef"

func newTestEnvironment(t *testing.T) (*Service, *store.Store, uuid.UUID) {
	t.Helper()
	pool := testdb.New(t)
	st := store.NewStore(pool)
	svc, err := New(st, testSecret)
	require.NoError(t, err)
	projects := project.New(st)
	proj, err := projects.Create(context.Background(), "demo", "")
	require.NoError(t, err)
	env, err := projects.CreateEnvironment(context.Background(), proj.ID, "production")
	require.NoError(t, err)
	return svc, st, env.ID
}

func TestStagePromoteRoundtrip(t *testing.T) {
	t.Parallel()
	svc, st, envID := newTestEnvironment(t)
	ctx := context.Background()

	candidate, err := svc.Stage(ctx, envID, map[string]string{
		"APP_DOMAIN":     "demo.localhost",
		"SESSION_SECRET": "s3cr3t-plant-value",
	})
	require.NoError(t, err)
	require.Equal(t, []string{"APP_DOMAIN", "SESSION_SECRET"}, candidate.Names)
	require.Equal(t, int64(1), candidate.Versions["SESSION_SECRET"])

	// Nothing is current until promotion.
	current, err := svc.CurrentVersions(ctx, envID)
	require.NoError(t, err)
	require.Empty(t, current)

	require.NoError(t, st.WithTx(ctx, func(q *store.Queries) error {
		return svc.PromoteTx(ctx, q, envID, candidate.ID)
	}))

	current, err = svc.CurrentVersions(ctx, envID)
	require.NoError(t, err)
	require.Equal(t, map[string]int64{"APP_DOMAIN": 1, "SESSION_SECRET": 1}, current)

	// Second candidate supersedes on promote and bumps versions.
	second, err := svc.Stage(ctx, envID, map[string]string{
		"SESSION_SECRET": "changed-plant-value",
	})
	require.NoError(t, err)
	require.Equal(t, int64(2), second.Versions["SESSION_SECRET"])
	require.NoError(t, st.WithTx(ctx, func(q *store.Queries) error {
		return svc.PromoteTx(ctx, q, envID, second.ID)
	}))
	current, err = svc.CurrentVersions(ctx, envID)
	require.NoError(t, err)
	require.Equal(t, map[string]int64{"APP_DOMAIN": 1, "SESSION_SECRET": 2}, current)

	summary, err := svc.Summary(ctx, envID)
	require.NoError(t, err)
	require.Equal(t, []Entry{
		{Name: "APP_DOMAIN", Version: 1},
		{Name: "SESSION_SECRET", Version: 2},
	}, summary)
}

func TestValuesAreEncryptedAtRest(t *testing.T) {
	t.Parallel()
	svc, st, envID := newTestEnvironment(t)
	ctx := context.Background()

	_, err := svc.Stage(ctx, envID, map[string]string{"SESSION_SECRET": "s3cr3t-plant-value"})
	require.NoError(t, err)

	var ciphertext []byte
	require.NoError(t, st.Pool.QueryRow(ctx,
		"SELECT ciphertext FROM environment_secrets WHERE name = 'SESSION_SECRET'").Scan(&ciphertext))
	require.NotContains(t, string(ciphertext), "s3cr3t-plant-value")

	key, err := crypt.Key(testSecret, "skali/values/secret-key/v1")
	require.NoError(t, err)
	plaintext, err := crypt.Decrypt(key, ciphertext)
	require.NoError(t, err)
	require.Equal(t, "s3cr3t-plant-value", string(plaintext))

	wrongKey, err := crypt.Key(strings.Repeat("x", 32), "skali/values/secret-key/v1")
	require.NoError(t, err)
	_, err = crypt.Decrypt(wrongKey, ciphertext)
	require.Error(t, err)
}

// An empty string is a real value: it stages, promotes, and resolves.
func TestEmptyStringRoundtrip(t *testing.T) {
	t.Parallel()
	svc, st, envID := newTestEnvironment(t)
	ctx := context.Background()

	candidate, err := svc.Stage(ctx, envID, map[string]string{"EMPTY": ""})
	require.NoError(t, err)
	require.NoError(t, st.WithTx(ctx, func(q *store.Queries) error {
		return svc.PromoteTx(ctx, q, envID, candidate.ID)
	}))
	current, err := svc.CurrentVersions(ctx, envID)
	require.NoError(t, err)
	require.Equal(t, map[string]int64{"EMPTY": 1}, current)
	plaintexts, err := svc.Plaintexts(ctx, envID, map[string]int{"EMPTY": 1})
	require.NoError(t, err)
	require.Equal(t, map[string]string{"EMPTY": ""}, plaintexts)
}

func TestRedactorCoversCurrentAndStaged(t *testing.T) {
	t.Parallel()
	svc, st, envID := newTestEnvironment(t)
	ctx := context.Background()

	first, err := svc.Stage(ctx, envID, map[string]string{"SESSION_SECRET": "current-plant-value"})
	require.NoError(t, err)
	require.NoError(t, st.WithTx(ctx, func(q *store.Queries) error {
		return svc.PromoteTx(ctx, q, envID, first.ID)
	}))
	second, err := svc.Stage(ctx, envID, map[string]string{"SESSION_SECRET": "staged-plant-value"})
	require.NoError(t, err)

	r, err := svc.Redactor(ctx, envID, second.ID)
	require.NoError(t, err)
	out := r.Redact("current-plant-value staged-plant-value")
	require.NotContains(t, out, "current-plant-value")
	require.NotContains(t, out, "staged-plant-value")
}

func TestDiscardAndSweep(t *testing.T) {
	t.Parallel()
	svc, st, envID := newTestEnvironment(t)
	ctx := context.Background()

	candidate, err := svc.Stage(ctx, envID, map[string]string{"A_SECRET": "discard-plant-value"})
	require.NoError(t, err)
	require.NoError(t, svc.DiscardCandidate(ctx, envID, candidate.ID))

	var count int
	require.NoError(t, st.Pool.QueryRow(ctx,
		"SELECT count(*) FROM environment_secrets").Scan(&count))
	require.Zero(t, count)

	// Sweep removes aged staged rows but never promoted ones.
	promoted, err := svc.Stage(ctx, envID, map[string]string{"KEEP": "kept"})
	require.NoError(t, err)
	require.NoError(t, st.WithTx(ctx, func(q *store.Queries) error {
		return svc.PromoteTx(ctx, q, envID, promoted.ID)
	}))
	_, err = svc.Stage(ctx, envID, map[string]string{"STALE": "stale"})
	require.NoError(t, err)
	_, err = st.Pool.Exec(ctx,
		"UPDATE environment_secrets SET created_at = now() - interval '2 days' WHERE state = 'staged'")
	require.NoError(t, err)

	swept, err := svc.SweepStaged(ctx, 24*time.Hour)
	require.NoError(t, err)
	require.Equal(t, int64(1), swept)
	current, err := svc.CurrentVersions(ctx, envID)
	require.NoError(t, err)
	require.Equal(t, map[string]int64{"KEEP": 1}, current)
}

func TestStageUnknownEnvironment(t *testing.T) {
	t.Parallel()
	svc, _, _ := newTestEnvironment(t)
	_, err := svc.Stage(context.Background(), uuid.New(), map[string]string{"A": "b"})
	require.ErrorIs(t, err, ErrEnvironmentNotFound)
}

// Revisions pin exact versions; superseded rows must keep resolving so old
// revisions stay applicable after later promotions.
func TestPlaintextsResolvesPinnedVersions(t *testing.T) {
	t.Parallel()
	svc, st, envID := newTestEnvironment(t)
	ctx := context.Background()

	stagePromote := func(value string) {
		candidate, err := svc.Stage(ctx, envID, map[string]string{"SESSION_SECRET": value})
		require.NoError(t, err)
		require.NoError(t, st.WithTx(ctx, func(q *store.Queries) error {
			return svc.PromoteTx(ctx, q, envID, candidate.ID)
		}))
	}
	stagePromote("first-plant-value")
	stagePromote("second-plant-value")

	// The superseded version 1 and the current version 2 both resolve.
	plaintexts, err := svc.Plaintexts(ctx, envID, map[string]int{"SESSION_SECRET": 1})
	require.NoError(t, err)
	require.Equal(t, map[string]string{"SESSION_SECRET": "first-plant-value"}, plaintexts)
	plaintexts, err = svc.Plaintexts(ctx, envID, map[string]int{"SESSION_SECRET": 2})
	require.NoError(t, err)
	require.Equal(t, map[string]string{"SESSION_SECRET": "second-plant-value"}, plaintexts)

	// Unknown pins fail without echoing any plaintext.
	_, err = svc.Plaintexts(ctx, envID, map[string]int{"SESSION_SECRET": 9})
	require.Error(t, err)
	require.NotContains(t, err.Error(), "plant-value")
	require.ErrorContains(t, err, "version 9")
}

// Unset tombstones the current generation: it disappears from listings, old
// pinned versions keep resolving, and a later stage continues the version
// sequence.
func TestUnsetTombstonesCurrent(t *testing.T) {
	t.Parallel()
	svc, st, envID := newTestEnvironment(t)
	ctx := context.Background()

	candidate, err := svc.Stage(ctx, envID, map[string]string{"REMOVED": "old-plant-value"})
	require.NoError(t, err)
	require.NoError(t, st.WithTx(ctx, func(q *store.Queries) error {
		return svc.PromoteTx(ctx, q, envID, candidate.ID)
	}))

	unset, err := svc.Unset(ctx, envID, []string{"REMOVED"})
	require.NoError(t, err)
	require.Equal(t, int64(1), unset)
	current, err := svc.CurrentVersions(ctx, envID)
	require.NoError(t, err)
	require.Empty(t, current)

	// A second unset finds nothing; the pinned version still resolves.
	unset, err = svc.Unset(ctx, envID, []string{"REMOVED"})
	require.NoError(t, err)
	require.Zero(t, unset)
	plaintexts, err := svc.Plaintexts(ctx, envID, map[string]int{"REMOVED": 1})
	require.NoError(t, err)
	require.Equal(t, map[string]string{"REMOVED": "old-plant-value"}, plaintexts)

	// Re-setting continues the version sequence past the tombstone.
	again, err := svc.Stage(ctx, envID, map[string]string{"REMOVED": "new-plant-value"})
	require.NoError(t, err)
	require.Equal(t, int64(2), again.Versions["REMOVED"])
	require.NoError(t, st.WithTx(ctx, func(q *store.Queries) error {
		return svc.PromoteTx(ctx, q, envID, again.ID)
	}))
	current, err = svc.CurrentVersions(ctx, envID)
	require.NoError(t, err)
	require.Equal(t, map[string]int64{"REMOVED": 2}, current)
}

// A candidate staged before an unset re-creates the name when it promotes:
// promotion wins, by documented design.
func TestUnsetThenPromoteResurrects(t *testing.T) {
	t.Parallel()
	svc, st, envID := newTestEnvironment(t)
	ctx := context.Background()

	first, err := svc.Stage(ctx, envID, map[string]string{"NAME": "one"})
	require.NoError(t, err)
	require.NoError(t, st.WithTx(ctx, func(q *store.Queries) error {
		return svc.PromoteTx(ctx, q, envID, first.ID)
	}))
	staged, err := svc.Stage(ctx, envID, map[string]string{"NAME": "two"})
	require.NoError(t, err)

	_, err = svc.Unset(ctx, envID, []string{"NAME"})
	require.NoError(t, err)
	require.NoError(t, st.WithTx(ctx, func(q *store.Queries) error {
		return svc.PromoteTx(ctx, q, envID, staged.ID)
	}))
	current, err := svc.CurrentVersions(ctx, envID)
	require.NoError(t, err)
	require.Equal(t, map[string]int64{"NAME": 2}, current)
}

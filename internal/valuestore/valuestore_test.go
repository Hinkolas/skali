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
	"github.com/Hinkolas/skali/internal/values"
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

	candidate, err := svc.Stage(ctx, envID, values.Resolved{
		Plain:  map[string]string{"APP_DOMAIN": "demo.localhost"},
		Secret: map[string]string{"SESSION_SECRET": "s3cr3t-plant-value"},
	})
	require.NoError(t, err)
	require.Equal(t, []string{"APP_DOMAIN"}, candidate.Plain)
	require.Equal(t, []string{"SESSION_SECRET"}, candidate.Secret)
	require.Equal(t, int64(1), candidate.SecretVersions["SESSION_SECRET"])

	// Nothing is current until promotion.
	current, err := svc.CurrentPlain(ctx, envID)
	require.NoError(t, err)
	require.Empty(t, current)

	require.NoError(t, st.WithTx(ctx, func(q *store.Queries) error {
		return svc.PromoteTx(ctx, q, envID, candidate.ID)
	}))

	current, err = svc.CurrentPlain(ctx, envID)
	require.NoError(t, err)
	require.Equal(t, map[string]string{"APP_DOMAIN": "demo.localhost"}, current)
	versions, err := svc.CurrentSecretVersions(ctx, envID)
	require.NoError(t, err)
	require.Equal(t, map[string]int64{"SESSION_SECRET": 1}, versions)

	// Second candidate supersedes on promote and bumps versions.
	second, err := svc.Stage(ctx, envID, values.Resolved{
		Plain:  map[string]string{"APP_DOMAIN": "demo2.localhost"},
		Secret: map[string]string{"SESSION_SECRET": "changed-plant-value"},
	})
	require.NoError(t, err)
	require.Equal(t, int64(2), second.SecretVersions["SESSION_SECRET"])
	require.NoError(t, st.WithTx(ctx, func(q *store.Queries) error {
		return svc.PromoteTx(ctx, q, envID, second.ID)
	}))
	current, err = svc.CurrentPlain(ctx, envID)
	require.NoError(t, err)
	require.Equal(t, "demo2.localhost", current["APP_DOMAIN"])
	versions, err = svc.CurrentSecretVersions(ctx, envID)
	require.NoError(t, err)
	require.Equal(t, int64(2), versions["SESSION_SECRET"])

	summary, err := svc.Summary(ctx, envID)
	require.NoError(t, err)
	require.Len(t, summary, 2)
	require.Equal(t, "APP_DOMAIN", summary[0].Name)
	require.False(t, summary[0].Secret)
	require.Equal(t, "demo2.localhost", summary[0].Value)
	require.Equal(t, "SESSION_SECRET", summary[1].Name)
	require.True(t, summary[1].Secret)
	require.Empty(t, summary[1].Value, "secret values must never appear in a summary")
}

func TestSecretsAreEncryptedAtRest(t *testing.T) {
	t.Parallel()
	svc, st, envID := newTestEnvironment(t)
	ctx := context.Background()

	_, err := svc.Stage(ctx, envID, values.Resolved{
		Secret: map[string]string{"SESSION_SECRET": "s3cr3t-plant-value"},
	})
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

func TestRedactorCoversCurrentAndStaged(t *testing.T) {
	t.Parallel()
	svc, st, envID := newTestEnvironment(t)
	ctx := context.Background()

	first, err := svc.Stage(ctx, envID, values.Resolved{
		Secret: map[string]string{"SESSION_SECRET": "current-plant-value"},
	})
	require.NoError(t, err)
	require.NoError(t, st.WithTx(ctx, func(q *store.Queries) error {
		return svc.PromoteTx(ctx, q, envID, first.ID)
	}))
	second, err := svc.Stage(ctx, envID, values.Resolved{
		Secret: map[string]string{"SESSION_SECRET": "staged-plant-value"},
	})
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

	candidate, err := svc.Stage(ctx, envID, values.Resolved{
		Plain:  map[string]string{"A_VALUE": "one"},
		Secret: map[string]string{"A_SECRET": "discard-plant-value"},
	})
	require.NoError(t, err)
	require.NoError(t, svc.DiscardCandidate(ctx, envID, candidate.ID))

	var count int
	require.NoError(t, st.Pool.QueryRow(ctx,
		"SELECT (SELECT count(*) FROM environment_values) + (SELECT count(*) FROM environment_secrets)").Scan(&count))
	require.Zero(t, count)

	// Sweep removes aged staged rows but never promoted ones.
	promoted, err := svc.Stage(ctx, envID, values.Resolved{
		Plain: map[string]string{"KEEP": "kept"},
	})
	require.NoError(t, err)
	require.NoError(t, st.WithTx(ctx, func(q *store.Queries) error {
		return svc.PromoteTx(ctx, q, envID, promoted.ID)
	}))
	_, err = svc.Stage(ctx, envID, values.Resolved{
		Plain: map[string]string{"STALE": "stale"},
	})
	require.NoError(t, err)
	_, err = st.Pool.Exec(ctx,
		"UPDATE environment_values SET created_at = now() - interval '2 days' WHERE state = 'staged'")
	require.NoError(t, err)

	swept, err := svc.SweepStaged(ctx, 24*time.Hour)
	require.NoError(t, err)
	require.Equal(t, int64(1), swept)
	current, err := svc.CurrentPlain(ctx, envID)
	require.NoError(t, err)
	require.Equal(t, map[string]string{"KEEP": "kept"}, current)
}

func TestStageUnknownEnvironment(t *testing.T) {
	t.Parallel()
	svc, _, _ := newTestEnvironment(t)
	_, err := svc.Stage(context.Background(), uuid.New(), values.Resolved{
		Plain: map[string]string{"A": "b"},
	})
	require.ErrorIs(t, err, ErrEnvironmentNotFound)
}

package migrations_test

import (
	"context"
	"database/sql"
	"testing"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5/stdlib"
	"github.com/pressly/goose/v3"
	"github.com/stretchr/testify/require"

	"github.com/Hinkolas/skali/internal/testdb"
	"github.com/Hinkolas/skali/migrations"
)

func database(t *testing.T) *sql.DB {
	t.Helper()
	pool := testdb.New(t)
	db := stdlib.OpenDB(*pool.Config().ConnConfig)
	t.Cleanup(func() { require.NoError(t, db.Close()) })
	return db
}

func provider(t *testing.T, db *sql.DB) *goose.Provider {
	t.Helper()
	p, err := goose.NewProvider(goose.DialectPostgres, db, migrations.FS)
	require.NoError(t, err)
	return p
}

func TestBaselineRepeatPreservesData(t *testing.T) {
	db := database(t)
	ctx := context.Background()
	var identity uuid.UUID
	require.NoError(t, db.QueryRowContext(ctx, "SELECT id FROM instance_identity").Scan(&identity))
	require.NotEqual(t, uuid.Nil, identity)
	var channel string
	var automatic bool
	require.NoError(t, db.QueryRowContext(ctx, "SELECT channel, auto_update FROM update_settings").Scan(&channel, &automatic))
	require.Equal(t, "stable", channel)
	require.False(t, automatic)

	// A populated installation must be accepted on every subsequent startup.
	user, project, environment, secret := uuid.New(), uuid.New(), uuid.New(), uuid.New()
	_, err := db.ExecContext(ctx, "INSERT INTO users (id, email) VALUES ($1, 'member@example.com')", user)
	require.NoError(t, err)
	_, err = db.ExecContext(ctx, "INSERT INTO projects (id, name) VALUES ($1, 'baseline-test')", project)
	require.NoError(t, err)
	_, err = db.ExecContext(ctx, "INSERT INTO environments (id, project_id, name) VALUES ($1, $2, 'production')", environment, project)
	require.NoError(t, err)
	ciphertext := []byte{1, 2, 3, 4}
	_, err = db.ExecContext(ctx, `INSERT INTO environment_secrets
    (id, environment_id, name, version, ciphertext, state)
    VALUES ($1, $2, 'TOKEN', 1, $3, 'current')`, secret, environment, ciphertext)
	require.NoError(t, err)
	_, err = db.ExecContext(ctx, "UPDATE update_settings SET channel = 'beta', auto_update = true")
	require.NoError(t, err)

	results, err := migrations.Up(ctx, db)
	require.NoError(t, err)
	require.Empty(t, results)
	var after uuid.UUID
	require.NoError(t, db.QueryRowContext(ctx, "SELECT id FROM instance_identity").Scan(&after))
	require.Equal(t, identity, after)
	var stored []byte
	require.NoError(t, db.QueryRowContext(ctx, "SELECT ciphertext FROM environment_secrets WHERE id = $1", secret).Scan(&stored))
	require.Equal(t, ciphertext, stored)
	var role string
	require.NoError(t, db.QueryRowContext(ctx, "SELECT role FROM users WHERE id = $1", user).Scan(&role))
	require.Equal(t, "member", role)
	require.NoError(t, db.QueryRowContext(ctx, "SELECT channel, auto_update FROM update_settings").Scan(&channel, &automatic))
	require.Equal(t, "beta", channel)
	require.True(t, automatic)
	statuses, err := migrations.Status(ctx, db)
	require.NoError(t, err)
	require.NotEmpty(t, statuses)
	require.EqualValues(t, 1, statuses[0].Source.Version)
	require.Equal(t, "00001_baseline.sql", statuses[0].Source.Path)
	require.False(t, statuses[0].AppliedAt.IsZero())
}

func TestBaselineDownAndUp(t *testing.T) {
	db := database(t)
	ctx := context.Background()
	_, err := provider(t, db).DownTo(ctx, 0)
	require.NoError(t, err)
	var tables int
	require.NoError(t, db.QueryRowContext(ctx, `SELECT count(*) FROM pg_tables
    WHERE schemaname = 'public' AND tablename <> 'goose_db_version'`).Scan(&tables))
	require.Zero(t, tables, "Down must remove the entire schema without CASCADE")

	results, err := migrations.Up(ctx, db)
	require.NoError(t, err)
	require.NotEmpty(t, results)
	version, err := provider(t, db).GetDBVersion(ctx)
	require.NoError(t, err)
	require.GreaterOrEqual(t, version, int64(1))
	var marker string
	require.NoError(t, db.QueryRowContext(ctx, "SELECT version FROM skali_schema_baseline").Scan(&marker))
	require.Equal(t, "v0.1.0-alpha.1", marker)
}

func TestPreBaselineDatabaseRefusedWithoutMutation(t *testing.T) {
	for _, tc := range []struct {
		name    string
		version int
		users   bool
	}{
		{name: "old-version-one", version: 1, users: true},
		{name: "old-version-thirty", version: 30, users: true},
		{name: "schema-without-history", users: true},
		{name: "history-without-schema", version: 1},
	} {
		t.Run(tc.name, func(t *testing.T) {
			db := database(t)
			ctx := context.Background()
			_, err := provider(t, db).DownTo(ctx, 0)
			require.NoError(t, err)
			_, err = db.ExecContext(ctx, "DELETE FROM goose_db_version WHERE version_id > 0")
			require.NoError(t, err)
			if tc.version > 0 {
				_, err = db.ExecContext(ctx, "INSERT INTO goose_db_version (version_id, is_applied) VALUES ($1, true)", tc.version)
				require.NoError(t, err)
			}
			if tc.users {
				_, err = db.ExecContext(ctx, "CREATE TABLE users (email TEXT); INSERT INTO users VALUES ('keep@example.com')")
				require.NoError(t, err)
			}
			var before string
			require.NoError(t, db.QueryRowContext(ctx, "SELECT json_agg(v ORDER BY id)::text FROM goose_db_version v").Scan(&before))
			_, err = migrations.Up(ctx, db)
			require.ErrorContains(t, err, "predates the v0.1.0-alpha.1 schema baseline")
			_, err = migrations.Status(ctx, db)
			require.ErrorContains(t, err, "no migrations were applied")
			var after string
			require.NoError(t, db.QueryRowContext(ctx, "SELECT json_agg(v ORDER BY id)::text FROM goose_db_version v").Scan(&after))
			require.Equal(t, before, after)
			var markerExists bool
			require.NoError(t, db.QueryRowContext(ctx, "SELECT to_regclass('public.skali_schema_baseline') IS NOT NULL").Scan(&markerExists))
			require.False(t, markerExists)
			if tc.users {
				var email string
				require.NoError(t, db.QueryRowContext(ctx, "SELECT email FROM users").Scan(&email))
				require.Equal(t, "keep@example.com", email)
			}
		})
	}
}

func TestUnrecognizedBaselineRefused(t *testing.T) {
	db := database(t)
	_, err := db.Exec("DELETE FROM skali_schema_baseline")
	require.NoError(t, err)
	_, err = migrations.Up(context.Background(), db)
	require.ErrorContains(t, err, "unrecognized skali schema baseline")
}

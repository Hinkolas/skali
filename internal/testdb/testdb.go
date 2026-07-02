// Package testdb provides an ephemeral, fully-migrated Postgres database per
// test. Tests run against real Postgres (no mocks): each call to New creates a
// uniquely-named database on the server behind TEST_DATABASE_URL, applies the
// embedded goose migrations, and drops the database again in t.Cleanup. This
// keeps parallel test packages isolated on a shared dev/CI Postgres.
package testdb

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"fmt"
	"os"
	"testing"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/jackc/pgx/v5/stdlib"

	"github.com/Hinkolas/skali/migrations"
)

const envVar = "TEST_DATABASE_URL"

// New returns a small pool connected to a fresh, migrated database. The test
// is skipped when TEST_DATABASE_URL is unset.
func New(t *testing.T) *pgxpool.Pool {
	t.Helper()
	ctx := context.Background()

	adminDSN := os.Getenv(envVar)
	if adminDSN == "" {
		t.Skipf("skipping: %s is not set", envVar)
	}

	var suffix [6]byte
	if _, err := rand.Read(suffix[:]); err != nil {
		t.Fatalf("testdb: random suffix: %v", err)
	}
	// Hex-only name, safe to interpolate as an identifier.
	name := "skali_test_" + hex.EncodeToString(suffix[:])

	adminExec(t, ctx, adminDSN, "CREATE DATABASE "+name)
	t.Cleanup(func() {
		// FORCE (PG13+) terminates any straggler connections.
		adminExec(t, ctx, adminDSN, fmt.Sprintf("DROP DATABASE %s WITH (FORCE)", name))
	})

	// Migrate over database/sql, which goose requires.
	connCfg, err := pgx.ParseConfig(adminDSN)
	if err != nil {
		t.Fatalf("testdb: parse %s: %v", envVar, err)
	}
	connCfg.Database = name
	sqlDB := stdlib.OpenDB(*connCfg)
	if err := migrations.Up(ctx, sqlDB); err != nil {
		t.Fatalf("testdb: migrate: %v", err)
	}
	if err := sqlDB.Close(); err != nil {
		t.Fatalf("testdb: close migration conn: %v", err)
	}

	poolCfg, err := pgxpool.ParseConfig(adminDSN)
	if err != nil {
		t.Fatalf("testdb: parse %s: %v", envVar, err)
	}
	poolCfg.ConnConfig.Database = name
	poolCfg.MaxConns = 4 // shared dev server; don't hog connections
	pool, err := pgxpool.NewWithConfig(ctx, poolCfg)
	if err != nil {
		t.Fatalf("testdb: connect %s: %v", name, err)
	}
	// Registered after the DROP cleanup, so it runs first (LIFO): the pool is
	// closed before the database is dropped.
	t.Cleanup(pool.Close)

	return pool
}

// adminExec runs a single statement over a short-lived connection to the admin
// database (the one named in TEST_DATABASE_URL).
func adminExec(t *testing.T, ctx context.Context, dsn, stmt string) {
	t.Helper()
	conn, err := pgx.Connect(ctx, dsn)
	if err != nil {
		t.Fatalf("testdb: connect admin: %v", err)
	}
	defer conn.Close(ctx)
	if _, err := conn.Exec(ctx, stmt); err != nil {
		t.Fatalf("testdb: %s: %v", stmt, err)
	}
}

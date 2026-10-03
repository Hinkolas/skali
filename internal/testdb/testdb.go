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
	"time"

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
	return NewScoped(t, t)
}

// NewScoped reports setup failures on t but keeps the database alive until
// scope finishes. It allows a selected subtest to lazily initialize a shared
// fixture without making unselected suites provision a database.
func NewScoped(t, scope *testing.T) *pgxpool.Pool {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Minute)
	defer cancel()

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

	start := time.Now()
	defer func() {
		t.Logf("phase=%q elapsed=%s", "control database setup", time.Since(start).Round(time.Millisecond))
	}()
	if err := adminExec(ctx, adminDSN, "CREATE DATABASE "+name); err != nil {
		t.Fatalf("testdb: create database: %v", err)
	}
	scope.Cleanup(func() {
		start := time.Now()
		defer func() {
			scope.Logf("phase=%q elapsed=%s", "control database cleanup", time.Since(start).Round(time.Millisecond))
		}()
		ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
		defer cancel()
		// FORCE (PG13+) terminates any straggler connections.
		if err := adminExec(ctx, adminDSN, fmt.Sprintf("DROP DATABASE %s WITH (FORCE)", name)); err != nil {
			scope.Errorf("testdb: cleanup %s: %v", name, err)
		}
	})

	// Migrate over database/sql, which goose requires.
	connCfg, err := pgx.ParseConfig(adminDSN)
	if err != nil {
		t.Fatalf("testdb: parse %s: %v", envVar, err)
	}
	connCfg.Database = name
	sqlDB := stdlib.OpenDB(*connCfg)
	defer sqlDB.Close()
	if _, err := migrations.Up(ctx, sqlDB); err != nil {
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
	scope.Cleanup(pool.Close)

	return pool
}

// adminExec runs a single statement over a short-lived connection to the admin
// database (the one named in TEST_DATABASE_URL).
func adminExec(ctx context.Context, dsn, stmt string) error {
	conn, err := pgx.Connect(ctx, dsn)
	if err != nil {
		return fmt.Errorf("connect admin: %w", err)
	}
	defer conn.Close(ctx)
	if _, err := conn.Exec(ctx, stmt); err != nil {
		return fmt.Errorf("%s: %w", stmt, err)
	}
	return nil
}

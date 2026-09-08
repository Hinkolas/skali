// Package migrations embeds the goose SQL migrations so binaries and tests can
// migrate a database without a goose CLI. The same files are sqlc's schema
// source (sqlc.yaml points schema: at this directory).
package migrations

import (
	"context"
	"database/sql"
	"embed"
	"fmt"

	"github.com/pressly/goose/v3"
)

//go:embed *.sql
var FS embed.FS

// Up applies all pending migrations and returns what it applied, so callers
// can act on a specific migration having landed in this run. db must use a
// database/sql driver (e.g. github.com/jackc/pgx/v5/stdlib).
func Up(ctx context.Context, db *sql.DB) ([]*goose.MigrationResult, error) {
	// Run before *any* pending migration: older prerelease migrations contain
	// destructive changes, so putting this check only in migration 29 is too late.
	if err := identityPreflight(ctx, db); err != nil {
		return nil, err
	}

	p, err := goose.NewProvider(goose.DialectPostgres, db, FS)
	if err != nil {
		return nil, err
	}
	return p.Up(ctx)
}

// Status returns one line per migration with its applied state.
func Status(ctx context.Context, db *sql.DB) ([]*goose.MigrationStatus, error) {
	p, err := goose.NewProvider(goose.DialectPostgres, db, FS)
	if err != nil {
		return nil, err
	}
	return p.Status(ctx)
}

func identityPreflight(ctx context.Context, db *sql.DB) error {
	var hasEnvironments, hasVersions bool
	if err := db.QueryRowContext(ctx, "SELECT to_regclass('public.environments') IS NOT NULL, to_regclass('public.goose_db_version') IS NOT NULL").Scan(&hasEnvironments, &hasVersions); err != nil {
		return err
	}
	if !hasEnvironments {
		return nil
	}
	var populated bool
	if err := db.QueryRowContext(ctx, "SELECT EXISTS (SELECT 1 FROM environments)").Scan(&populated); err != nil {
		return err
	}
	if !populated {
		return nil
	}
	var current bool
	if hasVersions {
		if err := db.QueryRowContext(ctx, "SELECT COALESCE((SELECT is_applied FROM goose_db_version WHERE version_id=29 ORDER BY id DESC LIMIT 1),false)").Scan(&current); err != nil {
			return err
		}
	}
	if !current {
		return fmt.Errorf("this prerelease requires a fresh installation: legacy environments exist; no migrations were applied")
	}
	return nil
}

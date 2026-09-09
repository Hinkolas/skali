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

// Up applies all pending migrations and returns what it applied. db must use a
// database/sql driver (e.g. github.com/jackc/pgx/v5/stdlib).
func Up(ctx context.Context, db *sql.DB) ([]*goose.MigrationResult, error) {
	if err := baselinePreflight(ctx, db); err != nil {
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
	if err := baselinePreflight(ctx, db); err != nil {
		return nil, err
	}
	p, err := goose.NewProvider(goose.DialectPostgres, db, FS)
	if err != nil {
		return nil, err
	}
	return p.Status(ctx)
}

// The pre-alpha history also started at version 1. Goose records numbers,
// not file checksums, so its version table alone cannot identify the baseline.
// Refuse old or unrecognized schemas before Goose creates or changes anything.
// The marker survives future migrations and is removed by the baseline's Down.
func baselinePreflight(ctx context.Context, db *sql.DB) error {
	var hasBaseline, hasUsers, hasVersions bool
	if err := db.QueryRowContext(ctx, `SELECT
    to_regclass('public.skali_schema_baseline') IS NOT NULL,
    to_regclass('public.users') IS NOT NULL,
    to_regclass('public.goose_db_version') IS NOT NULL`).Scan(&hasBaseline, &hasUsers, &hasVersions); err != nil {
		return err
	}
	if hasBaseline {
		var recognized bool
		if err := db.QueryRowContext(ctx, "SELECT EXISTS (SELECT 1 FROM skali_schema_baseline WHERE version = 'v0.1.0-alpha.1')").Scan(&recognized); err != nil {
			return err
		}
		if recognized {
			return nil
		}
		return fmt.Errorf("unrecognized skali schema baseline; no migrations were applied")
	}
	var applied bool
	if hasVersions {
		if err := db.QueryRowContext(ctx, `SELECT EXISTS (
    SELECT 1 FROM (
        SELECT DISTINCT ON (version_id) version_id, is_applied
        FROM goose_db_version ORDER BY version_id, id DESC
    ) versions WHERE version_id > 0 AND is_applied
)`).Scan(&applied); err != nil {
			return err
		}
	}
	if hasUsers || applied {
		return fmt.Errorf("this database predates the v0.1.0-alpha.1 schema baseline; export any needed data and create a fresh installation; no migrations were applied")
	}
	return nil
}

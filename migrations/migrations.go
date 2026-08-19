// Package migrations embeds the goose SQL migrations so binaries and tests can
// migrate a database without a goose CLI. The same files are sqlc's schema
// source (sqlc.yaml points schema: at this directory).
package migrations

import (
	"context"
	"database/sql"
	"embed"

	"github.com/pressly/goose/v3"
)

//go:embed *.sql
var FS embed.FS

// Up applies all pending migrations and returns what it applied, so callers
// can act on a specific migration having landed in this run. db must use a
// database/sql driver (e.g. github.com/jackc/pgx/v5/stdlib).
func Up(ctx context.Context, db *sql.DB) ([]*goose.MigrationResult, error) {
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

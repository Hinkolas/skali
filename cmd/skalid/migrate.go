package main

import (
	"context"
	"database/sql"
	"errors"
	"fmt"

	_ "github.com/jackc/pgx/v5/stdlib" // database/sql driver for goose

	"github.com/Hinkolas/skali/internal/config"
	"github.com/Hinkolas/skali/migrations"
)

// runMigrate applies the embedded goose migrations. The goose CLI pointed at
// migrations/ works identically for development; this subcommand exists so
// deployments need nothing but the skalid binary.
func runMigrate(args []string) error {
	if len(args) == 0 {
		return errors.New("usage: skalid migrate <up|status>")
	}
	ctx := context.Background()

	cfg, err := config.Load[config.Base](ctx)
	if err != nil {
		return err
	}
	db, err := sql.Open("pgx", cfg.DatabaseURL)
	if err != nil {
		return err
	}
	defer db.Close()

	switch args[0] {
	case "up":
		if err := migrations.Up(ctx, db); err != nil {
			return err
		}
		fmt.Println("migrations applied")
		return nil
	case "status":
		statuses, err := migrations.Status(ctx, db)
		if err != nil {
			return err
		}
		for _, s := range statuses {
			applied := "pending"
			if !s.AppliedAt.IsZero() {
				applied = s.AppliedAt.Format("2006-01-02 15:04:05")
			}
			fmt.Printf("%05d  %-10s  %s\n", s.Source.Version, applied, s.Source.Path)
		}
		return nil
	default:
		return fmt.Errorf("unknown migrate command %q (available: up, status)", args[0])
	}
}

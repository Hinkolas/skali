package main

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"strings"

	_ "github.com/jackc/pgx/v5/stdlib" // database/sql driver for goose

	"github.com/Hinkolas/skali/internal/config"
	"github.com/Hinkolas/skali/migrations"
)

// accessMigrationVersion introduced project membership. Member users that
// predate it hold no product access until an admin grants membership; the
// notice below names them once, when the migration lands.
const accessMigrationVersion = 19

func noticeMembersWithoutAccess(ctx context.Context, db *sql.DB) error {
	rows, err := db.QueryContext(ctx, `SELECT u.email FROM users u
WHERE u.role = 'member' AND NOT EXISTS (SELECT 1 FROM project_members m WHERE m.user_id = u.id)
ORDER BY lower(u.email)`)
	if err != nil {
		return fmt.Errorf("list members without access: %w", err)
	}
	defer rows.Close()
	var emails []string
	for rows.Next() {
		var email string
		if err := rows.Scan(&email); err != nil {
			return err
		}
		emails = append(emails, email)
	}
	if err := rows.Err(); err != nil {
		return err
	}
	if len(emails) == 0 {
		return nil
	}
	fmt.Printf("notice: access is now per project; these member users hold no project access until an admin grants membership: %s\n",
		strings.Join(emails, ", "))
	return nil
}

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
		results, err := migrations.Up(ctx, db)
		if err != nil {
			return err
		}
		fmt.Println("migrations applied")
		for _, result := range results {
			if result.Source.Version == accessMigrationVersion {
				return noticeMembersWithoutAccess(ctx, db)
			}
		}
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

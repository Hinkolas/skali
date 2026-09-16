package compiler

import "github.com/Hinkolas/skali/internal/diagnostic"

// Warnings are advisory notes about a definition that never enter the
// immutable definition or its hash; plan, deployment, and rollback
// responses carry them. Nothing warns today: the backup-policy notice that
// lived here retired when scheduled backups shipped. The hook stays so the
// next advisory has a home without touching every caller.
func Warnings(ProjectDefinition) []diagnostic.Warning {
	return nil
}

package compiler

import (
	"github.com/Hinkolas/skali/internal/diagnostic"
	"github.com/Hinkolas/skali/internal/utils"
)

const BackupPolicyWarning = "Backup policies are accepted but inactive: skali does not run scheduled backups or enforce retention. Create backups manually."

// Warnings are advisory and never enter the immutable definition or its hash.
func Warnings(def ProjectDefinition) []diagnostic.Warning {
	if len(def.Backups) == 0 {
		return nil
	}
	paths := make([]string, 0, len(def.Backups))
	for _, key := range utils.SortedKeys(def.Backups) {
		paths = append(paths, "backups."+key)
	}
	return []diagnostic.Warning{{Code: "backup_policy_inactive", Message: BackupPolicyWarning, Paths: paths}}
}

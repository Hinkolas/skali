package backup

import (
	"strings"
	"testing"

	"github.com/stretchr/testify/require"
)

// Database Jobs wait for the pool before the client runs: a fresh pod's
// first connection can be refused while network policy enforcement still
// registers its address, and Jobs get a single attempt (issue #104).
func TestDatabaseJobsWaitForThePool(t *testing.T) {
	identity := databaseJobIdentity{
		Host: "pg17-shared-rw.skali-platform.svc.cluster.local", Port: 5432, DatabaseName: "app",
		SecretName: "credentials", PostgresImage: "postgres:17", WorkerImage: "skalid:1",
		TargetSecret: targetSecretName, SnapshotObject: "snapshots/db.dump",
	}

	backup := renderDatabaseBackupJob("backup", "skali-platform", "id", identity)
	require.Len(t, backup.Spec.Template.Spec.InitContainers, 1)
	dump := backup.Spec.Template.Spec.InitContainers[0]
	require.Equal(t, []string{"/bin/sh", "-c"}, dump.Command[:2])
	script := dump.Command[2]
	require.True(t, strings.HasPrefix(script, databaseReadyWait))
	require.Contains(t, script, "pg_isready -q")
	require.Contains(t, script, "exec pg_dump -Fc --no-owner --no-privileges -f /work/db.dump")

	restore := renderDatabaseRestoreJob("restore", "skali-platform", "id", identity)
	require.Len(t, restore.Spec.Template.Spec.Containers, 1)
	client := restore.Spec.Template.Spec.Containers[0]
	require.Equal(t, []string{"/bin/sh", "-c"}, client.Command[:2])
	require.True(t, strings.HasPrefix(client.Command[2], databaseReadyWait))
	require.Contains(t, client.Command[2], "exec pg_restore")
}

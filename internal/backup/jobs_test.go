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

// A database restore downloads the dump against the manifest's size and
// checksum, reads its table of contents before it connects, and restores
// in one transaction; a volume restore hands the worker the archive's
// size and checksum to verify before it clears the volume.
func TestRestoreJobsVerifyBeforeTheyWrite(t *testing.T) {
	identity := databaseJobIdentity{
		Host: "pool", Port: 5432, DatabaseName: "app", SecretName: "credentials",
		PostgresImage: "postgres:17", WorkerImage: "skalid:1", TargetSecret: targetSecretName,
		SnapshotObject: "snapshots/db.dump", Expect: objectCheck{Bytes: 1234, SHA256: "abcd"},
	}
	restore := renderDatabaseRestoreJob("restore", "skali-platform", "id", identity)
	download := restore.Spec.Template.Spec.InitContainers[0]
	require.Equal(t, []string{"backup-worker", "download", "--key", "snapshots/db.dump", "--file", "/work/db.dump",
		"--size", "1234", "--sha256", "abcd"}, download.Args)
	script := restore.Spec.Template.Spec.Containers[0].Command[2]
	require.Contains(t, script, "set -e\npg_restore -l /work/db.dump > /work/toc\n")
	require.Contains(t, script, "--single-transaction --exit-on-error -L /work/list")

	volume := renderVolumeRestoreJob("restore", "env", "id", "skalid:1", targetSecretName, "claim", "vol.tar.zst",
		objectCheck{Bytes: 99})
	require.Equal(t, []string{"backup-worker", "untar", "--path", "/data", "--key", "vol.tar.zst", "--size", "99"},
		volume.Spec.Template.Spec.Containers[0].Args)
	backup := renderVolumeJob("backup", "env", "id", "skalid:1", targetSecretName, "claim", "vol.tar.zst", false)
	require.Equal(t, []string{"backup-worker", "tar", "--path", "/data", "--key", "vol.tar.zst"},
		backup.Spec.Template.Spec.Containers[0].Args)
}

// A download must be the size and checksum the snapshot records; without
// a recorded size or checksum, the object's own recorded checksum decides.
func TestVerifyDownload(t *testing.T) {
	require.NoError(t, verifyDownload("k", 4, "abcd", objectCheck{Bytes: 4, SHA256: "ABCD"}, ""))
	require.NoError(t, verifyDownload("k", 4, "abcd", objectCheck{Bytes: -1}, "abcd"))
	require.NoError(t, verifyDownload("k", 4, "abcd", objectCheck{Bytes: -1}, ""))
	require.ErrorContains(t, verifyDownload("k", 3, "abcd", objectCheck{Bytes: 4, SHA256: "abcd"}, ""), "3 bytes")
	require.ErrorContains(t, verifyDownload("k", 4, "abcd", objectCheck{Bytes: 4, SHA256: "ffff"}, "abcd"),
		"sha256 mismatch", "the manifest's checksum wins over the object's")
	require.ErrorContains(t, verifyDownload("k", 4, "abcd", objectCheck{Bytes: -1}, "ffff"), "sha256 mismatch")
}

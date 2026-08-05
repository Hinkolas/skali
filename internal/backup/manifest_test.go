package backup

import (
	"encoding/json"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
)

func fixtureManifest(t *testing.T) *Manifest {
	t.Helper()
	created, err := time.Parse(time.RFC3339, "2026-08-05T03:00:00Z")
	require.NoError(t, err)
	return &Manifest{
		FormatVersion:    ManifestFormatVersion,
		SnapshotID:       "0198c0de-0000-7000-8000-000000000001",
		Encryption:       EncryptionNone,
		CreatedAt:        created,
		SkaliVersion:     "test",
		Project:          "guestbook",
		Environment:      "production",
		RevisionChecksum: "abcdef0123456789abcdef0123456789abcdef0123456789abcdef0123456789",
		Revision:         json.RawMessage(`{"schemaVersion":"1"}`),
		Components: []Component{
			{Kind: ComponentDatabase, ServiceKey: "data",
				ObjectKey: "skali/v1/guestbook/production/databases/data/0198c0de-0000-7000-8000-000000000001.dump",
				Bytes:     4096, SHA256: "aa" + "bb", Status: "complete"},
			{Kind: ComponentBucket, ServiceKey: "assets",
				ObjectPrefix: "skali/v1/guestbook/production/buckets/assets/0198c0de-0000-7000-8000-000000000001/",
				ObjectCount:  3, Bytes: 1024, Status: "complete"},
			{Kind: ComponentVolume, Application: "web", Volume: "state",
				ObjectKey: "skali/v1/guestbook/production/volumes/web/state/0198c0de-0000-7000-8000-000000000001.tar.zst",
				Bytes:     2048, Status: "complete"},
		},
	}
}

// The golden fixture pins the wire format: an unnoticed field rename or
// encoding change must fail here before it strands real snapshots.
func TestManifestGolden(t *testing.T) {
	data, err := encodeManifest(fixtureManifest(t))
	require.NoError(t, err)

	golden := filepath.Join("testdata", "manifest_v1.json")
	if os.Getenv("UPDATE_GOLDEN") != "" {
		require.NoError(t, os.MkdirAll("testdata", 0o755))
		require.NoError(t, os.WriteFile(golden, data, 0o644))
	}
	want, err := os.ReadFile(golden)
	require.NoError(t, err, "run UPDATE_GOLDEN=1 go test ./internal/backup to create the fixture")
	require.Equal(t, string(want), string(data))

	decoded, err := decodeManifest(want)
	require.NoError(t, err)
	require.Equal(t, "guestbook", decoded.Project)
	require.Len(t, decoded.Components, 3)
}

func TestManifestRejectsUnknownFormat(t *testing.T) {
	_, err := decodeManifest([]byte(`{"format_version":"999"}`))
	var unsupported *UnsupportedManifestError
	require.ErrorAs(t, err, &unsupported)
	require.Equal(t, "999", unsupported.Found)
}

func TestManifestSummarize(t *testing.T) {
	summary := summarize(fixtureManifest(t))
	require.Equal(t, 1, summary.Databases)
	require.Equal(t, 1, summary.Buckets)
	require.Equal(t, 1, summary.Volumes)
	require.Equal(t, int64(4096+1024+2048), summary.Bytes)
	require.Equal(t, EncryptionNone, summary.Encryption)
}

func TestLayoutKeys(t *testing.T) {
	for _, tc := range []struct{ got, want string }{
		{manifestKey("", "p", "e", "id"), "skali/v1/p/e/snapshots/id.json"},
		{manifestKey("pre/fix", "p", "e", "id"), "pre/fix/skali/v1/p/e/snapshots/id.json"},
		{manifestKey("/pre/", "p", "e", "id"), "pre/skali/v1/p/e/snapshots/id.json"},
		{databaseKey("", "p", "e", "data", "id"), "skali/v1/p/e/databases/data/id.dump"},
		{bucketPrefixKey("", "p", "e", "assets", "id"), "skali/v1/p/e/buckets/assets/id/"},
		{volumeKey("", "p", "e", "web", "state", "id"), "skali/v1/p/e/volumes/web/state/id.tar.zst"},
		{snapshotPrefix("", "p", "e"), "skali/v1/p/e/snapshots/"},
	} {
		require.Equal(t, tc.want, tc.got)
	}
	require.Equal(t, "id", snapshotIDFromManifestKey("skali/v1/p/e/snapshots/id.json"))
}

func TestComponentLabels(t *testing.T) {
	require.Equal(t, "db:data", componentLabel(Component{Kind: ComponentDatabase, ServiceKey: "data"}))
	require.Equal(t, "bucket:assets", componentLabel(Component{Kind: ComponentBucket, ServiceKey: "assets"}))
	require.Equal(t, "volume:web.state", componentLabel(Component{Kind: ComponentVolume, Application: "web", Volume: "state"}))
}

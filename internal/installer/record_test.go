package installer

import (
	"context"
	"io/fs"
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/Hinkolas/skali/internal/installer/host"
)

func TestRecordRoundTrip(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	fake := &host.Fake{}

	_, err := LoadRecord(ctx, fake)
	require.ErrorIs(t, err, ErrNoRecord)

	record := &Record{
		Version:        RecordVersion,
		InstallationID: "a1b2c3",
		Provider:       ProviderK3s,
		Cluster:        "production",
		Ownership:      OwnershipManaged,
		Node: NodeRecord{
			Name:         "cp-1",
			Role:         "server",
			Capabilities: []string{"application", "database", "object-storage", "registry", "edge"},
		},
		Endpoints: &Endpoints{API: "skali.example.com"},
		TLS:       &TLSConfig{IssuerEmail: "ops@example.com"},
		Versions:  Versions{Installer: "2.0.0", K3s: K3sVersion, Bundle: "2.0.0"},
	}
	require.NoError(t, SaveRecord(ctx, fake, record))
	require.False(t, record.CreatedAt.IsZero())
	require.False(t, record.UpdatedAt.IsZero())

	// Root-owned modes: directory 0750, record 0600.
	require.Equal(t, fs.FileMode(0o750)|fs.ModeDir, fake.Modes[StateDir])
	require.Equal(t, fs.FileMode(0o600), fake.Modes[RecordPath])

	loaded, err := LoadRecord(ctx, fake)
	require.NoError(t, err)
	require.Equal(t, record.InstallationID, loaded.InstallationID)
	require.Equal(t, record.Node, loaded.Node)
	require.Equal(t, record.Endpoints, loaded.Endpoints)
	require.Equal(t, record.Versions, loaded.Versions)
	require.Equal(t, record.CreatedAt, loaded.CreatedAt)
}

// Loading is lenient about unknown fields so an older installer can still
// report status of a newer installation.
func TestRecordLoadLenient(t *testing.T) {
	t.Parallel()
	fake := &host.Fake{FS: map[string][]byte{RecordPath: []byte(`version: "2"
installationId: a1b2c3
provider: k3s
cluster: production
ownership: managed
futureField: something new
node:
  name: cp-1
  role: server
  capabilities: [application]
  futureNodeField: 7
versions:
  installer: 3.0.0
  k3s: v1.40.0+k3s1
`)}}
	loaded, err := LoadRecord(context.Background(), fake)
	require.NoError(t, err)
	require.Equal(t, "2", loaded.Version)
	require.Equal(t, "cp-1", loaded.Node.Name)
}

func TestRecordLoadRejectsMissingIdentity(t *testing.T) {
	t.Parallel()
	fake := &host.Fake{FS: map[string][]byte{RecordPath: []byte("cluster: production\n")}}
	_, err := LoadRecord(context.Background(), fake)
	require.ErrorContains(t, err, "identity fields")
}

// The canonical form feeds the bundle hash: it must be deterministic and
// free of volatile fields.
func TestRecordCanonicalYAML(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	fake := &host.Fake{}
	record := &Record{
		Version:        RecordVersion,
		InstallationID: "a1b2c3",
		Provider:       ProviderK3s,
		Cluster:        "production",
		Ownership:      OwnershipManaged,
		Node:           NodeRecord{Name: "cp-1", Role: "server", Capabilities: []string{"application"}},
		Versions:       Versions{Installer: "2.0.0", K3s: K3sVersion},
	}
	first, err := record.CanonicalYAML()
	require.NoError(t, err)
	require.NotContains(t, first, "createdAt")
	require.NotContains(t, first, "updatedAt")

	// Saving (which stamps timestamps) must not change the canonical form.
	require.NoError(t, SaveRecord(ctx, fake, record))
	second, err := record.CanonicalYAML()
	require.NoError(t, err)
	require.Equal(t, first, second)
}

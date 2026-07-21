package installer

import (
	"context"
	"encoding/json"
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/Hinkolas/skali/internal/installer/host"
	"github.com/Hinkolas/skali/internal/layout"
)

// installReadyHost extends the fresh fake with a k3s install script
// handler and a node that comes up ready and labeled.
func installReadyHost(capabilities []string) *host.Fake {
	fake := linuxHost()
	fake.Handlers["sh"] = func(host.Command) (host.Result, error) { return host.Result{}, nil }
	fake.Handlers["k3s"] = func(cmd host.Command) (host.Result, error) {
		labels := layout.CapabilityLabels(capabilities)
		labels[layout.ClusterLabel] = "e2e"
		node := map[string]any{
			"items": []map[string]any{{
				"metadata": map[string]any{"name": "cp-1", "labels": labels},
				"status": map[string]any{"conditions": []map[string]any{
					{"type": "Ready", "status": "True"},
				}},
			}},
		}
		data, _ := json.Marshal(node)
		return host.Result{Stdout: string(data)}, nil
	}
	return fake
}

func TestInstallFreshServer(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	capabilities := []string{layout.CapabilityApplication, layout.CapabilityEdge}
	fake := installReadyHost(capabilities)

	record, err := Install(ctx, fake, InstallOptions{
		Cluster:      "e2e",
		Capabilities: capabilities,
		Endpoints:    &Endpoints{API: "skali.e2e.test"},
	})
	require.NoError(t, err)
	require.NotEmpty(t, record.InstallationID)
	require.Equal(t, "e2e", record.Cluster)
	require.Equal(t, layout.RoleServer, record.Node.Role)
	require.Equal(t, "cp-1", record.Node.Name)
	require.Equal(t, K3sVersion, record.Versions.K3s)
	require.Empty(t, record.Versions.Bundle, "install never initializes the bundle")

	// The record persisted and the host now detects as a managed server
	// once the units exist; with only the binary probe faked the state is
	// damaged-or-server territory, so just check the record loads.
	loaded, err := LoadRecord(ctx, fake)
	require.NoError(t, err)
	require.Equal(t, record.InstallationID, loaded.InstallationID)

	// The configs were written before the install script ran.
	writes := fake.Writes
	require.Less(t, indexOf(writes, "write "+K3sConfigPath), indexOf(writes, "write "+RecordPath))
	require.Contains(t, string(fake.FS[K3sRegistriesPath]), "registry.skali.internal")
}

func TestInstallRefusesUnmanagedHost(t *testing.T) {
	t.Parallel()
	fake := withK3s(linuxHost(), "k3s.service", true)
	_, err := Install(context.Background(), fake, InstallOptions{
		Capabilities: []string{layout.CapabilityApplication},
	})
	require.ErrorContains(t, err, "will not be adopted or destroyed")
	require.Empty(t, fake.Writes, "the adoption guard must refuse before any mutation")
}

func TestInstallRefusesExistingInstallation(t *testing.T) {
	t.Parallel()
	fake := withRecord(t, withK3s(linuxHost(), "k3s.service", true), "server")
	_, err := Install(context.Background(), fake, InstallOptions{
		Capabilities: []string{layout.CapabilityApplication},
	})
	require.ErrorContains(t, err, "already carries a skali installation")
	require.Empty(t, fake.Writes)
}

func TestUninstallNodeGuardsMultiNode(t *testing.T) {
	t.Parallel()
	fake := &host.Fake{}
	record := &Record{Node: NodeRecord{Role: layout.RoleServer}}
	err := UninstallNode(context.Background(), fake, record, 3, nil)
	require.ErrorContains(t, err, "multi-node cluster is not implemented in this slice")
	require.Empty(t, fake.Commands)
}

func TestUninstallNodeRemovesRecordLast(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	fake := &host.Fake{Handlers: map[string]func(host.Command) (host.Result, error){
		"/usr/local/bin/k3s-uninstall.sh": func(host.Command) (host.Result, error) {
			return host.Result{}, nil
		},
	}}
	record := &Record{
		Version: RecordVersion, InstallationID: "x",
		Node: NodeRecord{Role: layout.RoleServer},
	}
	require.NoError(t, SaveRecord(ctx, fake, record))
	fake.Writes = nil

	require.NoError(t, UninstallNode(ctx, fake, record, 1, nil))
	require.Less(t, indexOf(fake.Writes, "remove "+CacheDir), indexOf(fake.Writes, "remove "+RecordPath))
	info, err := fake.Stat(ctx, StateDir)
	require.NoError(t, err)
	require.False(t, info.Exists)
}

func indexOf(entries []string, needle string) int {
	for index, entry := range entries {
		if entry == needle {
			return index
		}
	}
	return len(entries)
}

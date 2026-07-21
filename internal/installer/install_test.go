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

func TestInstallAgentJoin(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	fake := linuxHost()
	fake.FS["/root/token"] = []byte("K10abc::node:secret\n")
	joined := false
	fake.Handlers["sh"] = func(host.Command) (host.Result, error) {
		joined = true
		fake.FS[k3sAgentKubeletKubeconfig] = []byte("kubeconfig")
		return host.Result{}, nil
	}
	fake.Handlers["systemctl"] = func(cmd host.Command) (host.Result, error) {
		if joined && len(cmd.Args) == 2 && cmd.Args[0] == "is-active" && cmd.Args[1] == "k3s-agent.service" {
			return host.Result{Stdout: "active\n"}, nil
		}
		return host.Result{ExitCode: 4, Stdout: "not-found\n"}, nil
	}

	record, err := Install(ctx, fake, InstallOptions{
		Cluster:      "e2e",
		Role:         layout.RoleAgent,
		Capabilities: []string{layout.CapabilityDatabase},
		Join:         &JoinOptions{Server: "https://cp-1.internal:6443", TokenFile: "/root/token"},
	})
	require.NoError(t, err)
	require.Equal(t, layout.RoleAgent, record.Node.Role)
	require.Equal(t, "e2e", record.Cluster)
	require.Equal(t, []string{layout.CapabilityDatabase}, record.Node.Capabilities)
	require.NotNil(t, record.Join)
	require.Equal(t, "https://cp-1.internal:6443", record.Join.Server)
	require.Nil(t, record.Endpoints, "agents never gather endpoints")

	require.Equal(t, []byte("K10abc::node:secret\n"), fake.FS[K3sTokenPath])
	require.Contains(t, string(fake.FS[K3sConfigPath]), "server: https://cp-1.internal:6443")

	loaded, err := LoadRecord(ctx, fake)
	require.NoError(t, err)
	require.Equal(t, record.InstallationID, loaded.InstallationID)
	require.Equal(t, layout.RoleAgent, loaded.Node.Role)
}

func TestInstallAgentRequiresJoin(t *testing.T) {
	t.Parallel()
	fake := linuxHost()
	_, err := Install(context.Background(), fake, InstallOptions{
		Role:         layout.RoleAgent,
		Capabilities: []string{layout.CapabilityDatabase},
	})
	require.ErrorContains(t, err, "role agent requires join options")
	require.Empty(t, fake.Writes)
}

func TestInstallServerRefusesJoin(t *testing.T) {
	t.Parallel()
	fake := linuxHost()
	_, err := Install(context.Background(), fake, InstallOptions{
		Role:         layout.RoleServer,
		Capabilities: []string{layout.CapabilityApplication},
		Join:         &JoinOptions{Server: "https://cp-1.internal:6443", TokenFile: "/root/token"},
	})
	require.ErrorContains(t, err, "joining as an additional server is not implemented in this slice")
	require.Empty(t, fake.Writes)
}

func TestInstallAgentEmptyTokenFile(t *testing.T) {
	t.Parallel()
	fake := linuxHost()
	fake.FS["/root/token"] = []byte("  \n")
	_, err := Install(context.Background(), fake, InstallOptions{
		Role:         layout.RoleAgent,
		Capabilities: []string{layout.CapabilityDatabase},
		Join:         &JoinOptions{Server: "https://cp-1.internal:6443", TokenFile: "/root/token"},
	})
	require.ErrorContains(t, err, "join token file /root/token is empty")
	require.Empty(t, fake.Writes, "token resolution must fail before any mutation")
}

func TestInstallRefusesUnknownCapability(t *testing.T) {
	t.Parallel()
	fake := linuxHost()
	_, err := Install(context.Background(), fake, InstallOptions{
		Capabilities: []string{"warp-drive"},
	})
	require.ErrorContains(t, err, `unknown capability "warp-drive"`)
	require.Empty(t, fake.Writes)
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

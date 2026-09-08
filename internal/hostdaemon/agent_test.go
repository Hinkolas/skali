package hostdaemon

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"net/http"
	"net/http/httptest"
	"runtime"
	"testing"

	"github.com/stretchr/testify/require"
	"gopkg.in/yaml.v3"

	"github.com/Hinkolas/skali/internal/clusterstate"
	"github.com/Hinkolas/skali/internal/installer"
	"github.com/Hinkolas/skali/internal/installer/host"
	"github.com/Hinkolas/skali/internal/layout"
	"github.com/Hinkolas/skali/internal/version"
)

func upgradeAgent(t *testing.T, role string, k3sVersion string) (*Agent, *host.Fake) {
	t.Helper()
	record := &installer.Record{
		Version: installer.RecordVersionReconciled, Management: installer.ManagementReconciled,
		InstallationID: "install-1", Cluster: "e2e", Ownership: installer.OwnershipManaged,
		Provider: installer.ProviderK3s,
		Node:     installer.NodeRecord{ID: "node-1", Name: "cp-1", Role: role, Capabilities: []string{layout.CapabilityApplication}},
		Versions: installer.Versions{Installer: "v0.1.0", K3s: k3sVersion},
		Coordinator: &installer.CoordinatorRecord{
			Endpoints: []string{"https://10.1.0.3:6444"}, CAPin: "sha256:test", AgentVersion: "v0.1.0",
		},
	}
	data, err := yaml.Marshal(record)
	require.NoError(t, err)
	fake := &host.Fake{
		FS: map[string][]byte{
			installer.RecordPath:      data,
			installer.HostdBinaryPath: []byte("old-binary"),
		},
		Handlers: map[string]func(host.Command) (host.Result, error){
			"systemd-run": func(host.Command) (host.Result, error) { return host.Result{}, nil },
			"k3s": func(cmd host.Command) (host.Result, error) {
				return host.Result{Stdout: "k3s version " + k3sVersion + " (abc)\n"}, nil
			},
		},
	}
	return &Agent{Runner: fake}, fake
}

func TestUpgradeActionFirstPassReplacesHostdAndRestarts(t *testing.T) {
	binary := []byte("new-binary")
	sum := sha256.Sum256(binary)
	mux := http.NewServeMux()
	mux.HandleFunc("/Hinkolas/skali/releases/download/v0.2.0/"+installer.HostdAsset(runtime.GOARCH),
		func(w http.ResponseWriter, _ *http.Request) { _, _ = w.Write(binary) })
	server := httptest.NewServer(mux)
	defer server.Close()
	previous := installer.ReleaseBase
	installer.ReleaseBase = server.URL
	defer func() { installer.ReleaseBase = previous }()

	agent, fake := upgradeAgent(t, layout.RoleServer, "v1.36.2+k3s1")
	agent.Downloads = server.Client()
	action := clusterstate.AgentAction{
		ID: "attempt-1", Type: clusterstate.NodeActionUpgrade,
		Version: "v0.2.0", HostdSHA256: hex.EncodeToString(sum[:]),
	}
	require.NotEqual(t, "v0.2.0", version.Version)

	_, restarting := agent.execute(context.Background(), action)
	require.True(t, restarting, "the old binary hands off to its successor")
	require.Equal(t, binary, fake.FS[installer.HostdBinaryPath])
	require.Len(t, fake.Commands, 1)
	require.Equal(t, "systemd-run", fake.Commands[0].Name)
	require.Contains(t, fake.Commands[0].Args[len(fake.Commands[0].Args)-1], installer.HostdCoordinatorUnit)
	record, err := installer.LoadRecord(context.Background(), fake)
	require.NoError(t, err)
	require.Equal(t, "v0.2.0", record.Coordinator.AgentVersion)

	// A checksum mismatch never writes anything.
	agent, fake = upgradeAgent(t, layout.RoleAgent, "v1.36.2+k3s1")
	agent.Downloads = server.Client()
	action.HostdSHA256 = hex.EncodeToString(make([]byte, 32))
	report, restarting := agent.execute(context.Background(), action)
	require.False(t, restarting)
	require.False(t, report.ActionOK)
	require.Contains(t, report.Error, "does not match its published checksum")
	require.Equal(t, []byte("old-binary"), fake.FS[installer.HostdBinaryPath])
	require.Empty(t, fake.Commands)
}

func TestUpgradeActionSecondPassMovesK3sToOwnPin(t *testing.T) {
	previous := version.Version
	version.Version = "v0.2.0"
	defer func() { version.Version = previous }()

	// Already on the pin: nothing to move, the versions are recorded.
	agent, fake := upgradeAgent(t, layout.RoleAgent, installer.K3sVersion)
	report, restarting := agent.execute(context.Background(), clusterstate.AgentAction{
		ID: "attempt-1", Type: clusterstate.NodeActionUpgrade, Version: "v0.2.0",
	})
	require.False(t, restarting)
	require.True(t, report.ActionOK, report.Error)
	require.Equal(t, installer.K3sVersion, report.K3sVersion)
	require.Equal(t, "v0.2.0", report.AgentVersion)
	record, err := installer.LoadRecord(context.Background(), fake)
	require.NoError(t, err)
	require.Equal(t, "v0.2.0", record.Versions.Installer)
	require.Equal(t, "v0.2.0", record.Coordinator.AgentVersion)
	for _, cmd := range fake.Commands {
		require.NotEqual(t, "sh", cmd.Name, "no k3s install script runs when the pin already holds")
	}

	// A host newer than the pin refuses the downgrade before touching k3s.
	agent, fake = upgradeAgent(t, layout.RoleAgent, "v9.99.9+k3s9")
	report, restarting = agent.execute(context.Background(), clusterstate.AgentAction{
		ID: "attempt-2", Type: clusterstate.NodeActionUpgrade, Version: "v0.2.0",
	})
	require.False(t, restarting)
	require.False(t, report.ActionOK)
	require.Contains(t, report.Error, "downgrade")
	for _, cmd := range fake.Commands {
		require.NotEqual(t, "sh", cmd.Name)
	}
}

func TestReportCarriesAgentVersion(t *testing.T) {
	agent, _ := upgradeAgent(t, layout.RoleAgent, installer.K3sVersion)
	report := agent.report()
	require.Equal(t, version.Version, report.AgentVersion)
	require.Equal(t, installer.K3sVersion, report.K3sVersion)
	require.Equal(t, clusterstate.NodePhaseActive, report.Phase)
}

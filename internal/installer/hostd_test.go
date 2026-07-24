package installer

import (
	"context"
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	"github.com/Hinkolas/skali/internal/clusterstate"
	"github.com/Hinkolas/skali/internal/installer/host"
	"github.com/Hinkolas/skali/internal/layout"
)

func TestResumeEnrolledHostdUsesExistingIdentity(t *testing.T) {
	t.Parallel()
	fake := linuxHost()
	fake.Handlers["systemctl"] = func(host.Command) (host.Result, error) {
		return host.Result{}, nil
	}
	now := time.Date(2026, 7, 24, 10, 0, 0, 0, time.UTC)
	record := &Record{
		Version: RecordVersionReconciled, Management: ManagementReconciled,
		InstallationID: "install-1", Cluster: "e2e", Ownership: OwnershipManaged,
		Provider: ProviderK3s,
		Node: NodeRecord{
			ID: "node-1", Name: "worker-1", Role: layout.RoleAgent,
			Capabilities: []string{layout.CapabilityApplication},
		},
		Coordinator: &CoordinatorRecord{
			Endpoints: []string{"https://10.1.0.3:6444"}, CAPin: "sha256:test",
		},
		Lifecycle: &InstallLifecycle{
			Status: InstallStatusFailed, Phase: InstallPhaseAwaitingApply,
			StartedAt: now, UpdatedAt: now,
		},
		CreatedAt: now,
	}
	config := AgentConfig{
		Version: 1, Cluster: "e2e", NodeID: "node-1", NodeName: "worker-1",
		InstallationID: "install-1", Endpoints: []string{"https://10.1.0.3:6444"},
		CACert: AgentCACertPath, ClientCert: AgentClientCertPath,
		ClientKey: AgentClientKeyPath, PollSeconds: 5,
	}
	configData, err := encodeYAML(config)
	require.NoError(t, err)
	fake.FS[AgentConfigPath] = configData
	fake.FS[AgentCACertPath] = []byte("ca")
	fake.FS[AgentClientCertPath] = []byte("certificate")
	fake.FS[AgentClientKeyPath] = []byte("key")

	resumed, err := ResumeEnrolledHostd(context.Background(), fake, record,
		[]byte("hostd"), "10.1.0.4")

	require.NoError(t, err)
	require.True(t, resumed)
	require.Equal(t, InstallStatusEnrolled, record.Lifecycle.Status)
	require.Equal(t, InstallPhaseAwaitingApply, record.Lifecycle.Phase)
	require.Equal(t, []string{
		"https://10.1.0.4:6444",
		"https://10.1.0.3:6444",
	}, record.Coordinator.Endpoints)
	for _, command := range fake.Commands {
		if command.Name == "systemctl" && len(command.Args) > 2 &&
			command.Args[0] == "enable" {
			require.Equal(t, HostdAgentUnit, command.Args[2])
		}
	}
}

func TestCacheCoordinatorStatePersistsOnlyChangedReferences(t *testing.T) {
	t.Parallel()
	fake := linuxHost()
	now := time.Now().UTC().Truncate(time.Second)
	record := &Record{
		Version: RecordVersionReconciled, Management: ManagementReconciled,
		InstallationID: "install-1", Cluster: "e2e", Ownership: OwnershipManaged,
		Provider: ProviderK3s,
		Node: NodeRecord{
			ID: "node-1", Name: "worker-1", Role: layout.RoleAgent,
			Capabilities: []string{layout.CapabilityApplication},
		},
		Coordinator: &CoordinatorRecord{Endpoints: []string{"https://10.1.0.3:6444"}},
		Versions:    Versions{K3s: K3sVersion},
		Lifecycle: &InstallLifecycle{
			Status: InstallStatusComplete, Phase: InstallPhaseComplete,
		},
		CreatedAt: now,
	}
	require.NoError(t, SaveRecord(context.Background(), fake, record))
	response := clusterstate.AgentPollResponse{
		ConvergedRevision: "revision-1", TargetRevision: "revision-2",
		CandidateRevision: "revision-3", OperationID: "operation-1",
		OperationPhase: clusterstate.OperationAdding,
	}
	require.NoError(t, CacheCoordinatorState(context.Background(), fake, response))
	loaded, err := LoadRecord(context.Background(), fake)
	require.NoError(t, err)
	require.Equal(t, "operation-1", loaded.Coordinator.LastOperation)
	require.Equal(t, clusterstate.OperationAdding,
		loaded.Coordinator.LastOperationPhase)
	writes := len(fake.Writes)
	require.NoError(t, CacheCoordinatorState(context.Background(), fake, response))
	require.Equal(t, writes, len(fake.Writes),
		"unchanged heartbeat state must not rewrite the atomic record")
}

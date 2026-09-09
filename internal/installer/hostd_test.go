package installer

import (
	"context"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	"github.com/Hinkolas/skali/internal/clusterstate"
	"github.com/Hinkolas/skali/internal/installer/host"
	"github.com/Hinkolas/skali/internal/layout"
)

func TestStartHostdRejectsServiceThatDidNotStayActive(t *testing.T) {
	t.Parallel()
	fake := &host.Fake{Handlers: map[string]func(host.Command) (host.Result, error){
		"systemctl": func(cmd host.Command) (host.Result, error) {
			if len(cmd.Args) > 0 && cmd.Args[0] == "is-active" {
				return host.Result{ExitCode: 3, Stdout: "failed\n"}, nil
			}
			return host.Result{}, nil
		},
	}}

	err := StartHostd(context.Background(), fake, true)
	require.ErrorContains(t, err, "skali-node-agent.service did not stay active: failed")
}

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

func TestCancelledEnrollmentCleanupRetainsHostdAndRetiresStateLast(t *testing.T) {
	fake := linuxHost()
	fake.Handlers["systemd-run"] = func(cmd host.Command) (host.Result, error) { return host.Result{}, nil }
	require.NoError(t, ScheduleHostdSelfRemoval(context.Background(), fake, true))
	command := fake.Commands[len(fake.Commands)-1]
	require.Contains(t, command.Args, "--collect")
	script := command.Args[len(command.Args)-1]
	require.NotContains(t, script, "rm -f "+HostdBinaryPath)
	require.NotContains(t, script, "rm -rf "+StateDir)
	require.Contains(t, script, "mv "+StateDir+" ")
	require.Less(t, strings.Index(script, "daemon-reload"), strings.Index(script, "mv "+StateDir))
	// Manual cleanup stops both agents and any old pending timer before reuse.
	fake.Handlers["systemctl"] = func(host.Command) (host.Result, error) { return host.Result{}, nil }
	fake.FS[HostdBinaryPath] = []byte("hostd")
	require.NoError(t, RemoveHostd(context.Background(), fake, false))
	require.Contains(t, fake.FS, HostdBinaryPath)
	found := false
	for _, command := range fake.Commands {
		if slices.Contains(command.Args, "skali-hostd-cleanup.timer") {
			found = true
		}
	}
	require.True(t, found)
}

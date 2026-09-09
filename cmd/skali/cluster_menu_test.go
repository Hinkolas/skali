package main

import (
	"io"
	"os"
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/Hinkolas/skali/internal/clusterstate"
	"github.com/Hinkolas/skali/internal/installer"
	"github.com/Hinkolas/skali/internal/layout"
)

func TestPrintStatusUsesStructuredInstallerHeader(t *testing.T) {
	output, err := os.CreateTemp(t.TempDir(), "status-*.txt")
	require.NoError(t, err)
	t.Cleanup(func() { _ = output.Close() })

	status := &installer.Status{
		Host: &installer.Host{
			State:      installer.StateServer,
			Hostname:   "skali-dev-01",
			K3sVersion: installer.K3sVersion,
			K3sActive:  true,
			Record: &installer.Record{
				Version:    installer.RecordVersionReconciled,
				Management: installer.ManagementReconciled,
				Cluster:    "skali-dev",
				Node: installer.NodeRecord{
					Name: "skali-dev-01",
					Role: layout.RoleServer,
				},
			},
		},
		K3sCurrent:       true,
		ClusterReachable: true,
		Nodes: []installer.NodeStatus{{
			Name:       "skali-dev-01",
			Role:       layout.RoleServer,
			K3sVersion: installer.K3sVersion,
			Current:    true,
		}},
		Components: []installer.ComponentStatus{
			{Name: "database", Detail: "not found"},
			{Name: "registry", Detail: "not found"},
			{Name: "skalid", Detail: "not found"},
		},
		Reconciled: &clusterstate.State{
			ConvergedRevision: "25766f1c",
			CandidateRevision: "25766f1c",
			Nodes: map[string]clusterstate.Node{
				"node-1": {
					ID:    "node-1",
					Name:  "skali-dev-01",
					Role:  layout.RoleServer,
					Phase: clusterstate.NodePhaseActive,
				},
			},
		},
	}

	printStatus(output, status)
	_, err = output.Seek(0, io.SeekStart)
	require.NoError(t, err)
	rendered, err := io.ReadAll(output)
	require.NoError(t, err)
	text := string(rendered)

	require.Contains(t, text, "◆ skali-dev-01")
	require.Contains(t, text, "status     Skali server (cluster \"skali-dev\")")
	require.Contains(t, text, "bundle     not initialized; run skali cluster init")
	require.Contains(t, text, "managed nodes")
	require.Contains(t, text, "skali-dev-01")
	require.NotContains(t, text, "managed    skali-dev-01")
	require.NotContains(t, text, "platforms", "a homogeneous cluster shows no platform summary")
}

func TestNodeArchCounts(t *testing.T) {
	t.Parallel()
	status := &installer.Status{Nodes: []installer.NodeStatus{
		{Name: "a", Arch: "arm64"},
		{Name: "b", Arch: "arm64"},
		{Name: "c", Arch: "amd64"},
		{Name: "d"},
	}}
	require.Equal(t, []string{"2 arm64", "1 amd64"}, nodeArchCounts(status),
		"counted descending, unknown archs skipped")
	require.Equal(t, []string{"1 amd64"},
		nodeArchCounts(&installer.Status{Nodes: []installer.NodeStatus{{Name: "c", Arch: "amd64"}}}))
	require.Empty(t, nodeArchCounts(&installer.Status{}))
}

func TestCancelledHistoryHiddenUnlessAllRequested(t *testing.T) {
	for _, all := range []bool{false, true} {
		output, err := os.CreateTemp(t.TempDir(), "status")
		require.NoError(t, err)
		status := &installer.Status{Host: &installer.Host{Record: &installer.Record{}}, Reconciled: &clusterstate.State{Nodes: map[string]clusterstate.Node{
			"active":    {ID: "active", Name: "current-node", Phase: clusterstate.NodePhaseActive},
			"cancelled": {ID: "cancelled", Name: "cancelled-node", Phase: clusterstate.NodePhaseCancelled},
			"removed":   {ID: "removed", Name: "removed-node", Phase: clusterstate.NodePhaseRemoved},
		}}}
		printReconciledStatus(output, status, all)
		require.NoError(t, output.Close())
		data, err := os.ReadFile(output.Name())
		require.NoError(t, err)
		require.Contains(t, string(data), "current-node")
		if all {
			require.Contains(t, string(data), "cancelled-node")
			require.Contains(t, string(data), "removed-node")
		} else {
			require.NotContains(t, string(data), "cancelled-node")
			require.NotContains(t, string(data), "removed-node")
		}
	}
}

func TestCandidateWithoutK3sIsAwaitingEnrollmentNotBrokenAPI(t *testing.T) {
	output, err := os.CreateTemp(t.TempDir(), "status")
	require.NoError(t, err)
	status := &installer.Status{Host: &installer.Host{State: installer.StateEnrolled, Hostname: "db-01", Record: &installer.Record{
		Management: installer.ManagementReconciled, Version: installer.RecordVersionReconciled, Cluster: "kilohertz",
		Coordinator: &installer.CoordinatorRecord{}, Lifecycle: &installer.InstallLifecycle{Status: installer.InstallStatusFailed, Phase: installer.InstallPhaseEnrolled},
	}}}
	printStatus(output, status)
	require.NoError(t, output.Close())
	data, err := os.ReadFile(output.Name())
	require.NoError(t, err)
	require.NotContains(t, string(data), "kubernetes api unreachable")
	require.Contains(t, string(data), "resume with sudo skali cluster join")
}

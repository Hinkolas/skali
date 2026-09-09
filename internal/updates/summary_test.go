package updates

import (
	"fmt"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
)

func healthyStatus() (*Status, time.Time) {
	now := time.Date(2026, 9, 9, 12, 0, 0, 0, time.UTC)
	s := &Status{Installed: Installed{Version: "v0.1.0-alpha.4", PlatformVersion: "v0.1.0-alpha.4"},
		Managed: true, Manageable: true, Settings: Settings{Channel: ChannelBeta, LastCheckedAt: &now,
			Latest: &Release{Version: "v0.1.0-alpha.4"}}}
	for i := range 11 {
		n := NodeState{ID: fmt.Sprintf("id-%d", i), Name: fmt.Sprintf("node-%d", i), Role: "agent", Phase: "active", AgentVersion: s.Installed.Version, K3sVersion: "v1.36.3+k3s1", LastSeen: now}
		if i < 3 {
			n.Role, n.CoordinatorVersion, n.CoordinatorLastSeen = "server", s.Installed.Version, now
		}
		s.Nodes = append(s.Nodes, n)
	}
	return s, now
}

func TestAggregateUpdateStates(t *testing.T) {
	for _, tc := range []struct {
		name, state, action string
		change              func(*Status)
	}{
		{"healthy", "current", "", func(s *Status) {}},
		{"new release", "available", "update", func(s *Status) { s.Latest.Version = "v0.1.0-alpha.5" }},
		{"platform ahead on stable", "incomplete", "finish", func(s *Status) {
			s.Channel = ChannelStable
			s.Latest = nil
			s.Installed.PlatformVersion = "v0.1.0-alpha.3"
			for i := range s.Nodes {
				s.Nodes[i].AgentVersion = "v0.1.0-alpha.3"
			}
		}},
		{"same release repair", "incomplete", "finish", func(s *Status) { s.Nodes[5].AgentVersion = "v0.1.0-alpha.3" }},
		{"host ahead", "incomplete", "finish", func(s *Status) { s.Nodes[5].AgentVersion = "v0.1.0-alpha.5" }},
		{"coordinator behind", "incomplete", "finish", func(s *Status) { s.Nodes[0].CoordinatorVersion = "v0.1.0-alpha.3" }},
		{"old coordinator cannot report", "incomplete", "finish", func(s *Status) { s.Nodes[0].CoordinatorVersion = "" }},
		{"Kubernetes drift", "incomplete", "finish", func(s *Status) { s.Nodes[5].K3sVersion = "v1.36.2+k3s1" }},
		{"unknown host", "unknown", "", func(s *Status) { s.Nodes[5].AgentVersion = "" }},
		{"development host", "unknown", "", func(s *Status) { s.Nodes[5].AgentVersion = "v0.0.0-dev" }},
		{"stale agent", "unknown", "", func(s *Status) { s.Nodes[5].LastSeen = time.Time{} }},
		{"stale coordinator", "unknown", "", func(s *Status) { s.Nodes[0].CoordinatorLastSeen = time.Time{} }},
		{"failed scan", "unknown", "", func(s *Status) { s.LastError = "offline" }},
		{"no release", "no_release", "", func(s *Status) { s.Latest = nil }},
		{"never checked", "not_checked", "", func(s *Status) { s.LastCheckedAt = nil }},
		{"running", "updating", "", func(s *Status) { s.Operation = &OperationState{Phase: "verifying", TargetVersion: "v0.1.0-alpha.5"} }},
		{"failed operation", "failed", "retry", func(s *Status) {
			s.Operation = &OperationState{Phase: "failed", TargetVersion: "v0.1.0-alpha.5", Error: "download failed"}
		}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			s, now := healthyStatus()
			tc.change(s)
			s.Summary = summarize(s, "v1.36.3+k3s1", now)
			require.Equal(t, tc.state, s.Summary.State)
			require.Equal(t, tc.action, s.Summary.Action)
			if tc.action == "finish" {
				require.NoError(t, ValidateTarget(s, s.Summary.TargetVersion))
			}
		})
	}
}

func TestUpdateTargetCannotDowngradeOrReplayHealthyCluster(t *testing.T) {
	s, now := healthyStatus()
	s.Summary = summarize(s, "v1.36.3+k3s1", now)
	require.ErrorIs(t, ValidateTarget(s, s.Installed.Version), ErrNotNewer)
	require.Error(t, ValidateTarget(s, "v0.1.0-alpha.3"))
	require.NoError(t, ValidateTarget(s, "v0.1.0-alpha.5"))
	s.Nodes[5].AgentVersion = "v0.1.0-alpha.6"
	s.Summary = summarize(s, "v1.36.3+k3s1", now)
	require.Equal(t, "v0.1.0-alpha.6", s.Summary.TargetVersion)
	require.Error(t, ValidateTarget(s, "v0.1.0-alpha.5"))
}

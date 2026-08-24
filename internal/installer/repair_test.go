package installer

import (
	"context"
	"testing"

	"github.com/stretchr/testify/require"
	"gopkg.in/yaml.v3"

	"github.com/Hinkolas/skali/internal/installer/host"
	"github.com/Hinkolas/skali/internal/layout"
)

func actionIDs(actions []RepairAction) []string {
	ids := make([]string, 0, len(actions))
	for _, action := range actions {
		ids = append(ids, action.ID)
	}
	return ids
}

func repairDiagnosis(state HostState, record *Record, failing ...string) *Diagnosis {
	diagnosis := &Diagnosis{Host: &Host{State: state, Record: record}}
	for _, name := range failing {
		diagnosis.Checks = append(diagnosis.Checks, Check{Name: name, Severity: SeverityFail})
	}
	return diagnosis
}

func TestPlanRepairs(t *testing.T) {
	t.Parallel()
	server := &Record{Node: NodeRecord{Name: "cp-1", Role: layout.RoleServer}}
	agent := &Record{Node: NodeRecord{Name: "db-1", Role: layout.RoleAgent}}
	deps := func(record *Record, stampMissing bool) RepairDeps {
		return RepairDeps{Runner: &host.Fake{}, Record: record, StampMissing: stampMissing}
	}

	cases := map[string]struct {
		diagnosis   *Diagnosis
		deps        RepairDeps
		wantIDs     []string
		wantRefusal string
	}{
		"unreadable record refuses everything": {
			diagnosis:   repairDiagnosis(StateDamaged, nil, "installation"),
			deps:        deps(nil, false),
			wantIDs:     nil,
			wantRefusal: "skali cluster restore",
		},
		"damaged with readable record reinstalls": {
			diagnosis: repairDiagnosis(StateDamaged, server, "installation", "k3s service"),
			deps:      deps(server, false),
			wantIDs:   []string{"reinstall-k3s"},
		},
		"inactive unit restarts": {
			diagnosis: repairDiagnosis(StateServer, server, "k3s service"),
			deps:      deps(server, false),
			wantIDs:   []string{"restart-k3s"},
		},
		"unreachable api restarts": {
			diagnosis: repairDiagnosis(StateServer, server, "kubernetes api"),
			deps:      deps(server, false),
			wantIDs:   []string{"restart-k3s"},
		},
		"registries heal forces reconverge": {
			diagnosis: repairDiagnosis(StateServer, server, "registry mirror"),
			deps:      deps(server, false),
			wantIDs:   []string{"heal-registries", "reconverge"},
		},
		"agent registries refuse healing": {
			diagnosis:   repairDiagnosis(StateAgent, agent, "registry mirror"),
			deps:        deps(agent, false),
			wantIDs:     nil,
			wantRefusal: "Re-join the node with a fresh token",
		},
		"unhealthy component reconverges": {
			diagnosis: repairDiagnosis(StateServer, server, "bootstrap database"),
			deps:      deps(server, false),
			wantIDs:   []string{"reconverge"},
		},
		"missing stamp reconverges": {
			diagnosis: repairDiagnosis(StateServer, server),
			deps:      deps(server, true),
			wantIDs:   []string{"reconverge"},
		},
		"agent never reconverges": {
			diagnosis: repairDiagnosis(StateAgent, agent, "k3s service"),
			deps:      deps(agent, false),
			wantIDs:   []string{"restart-k3s"},
		},
		"missing storage prerequisites install them": {
			diagnosis: repairDiagnosis(StateServer, server, "storage prerequisites"),
			deps:      deps(server, false),
			wantIDs:   []string{"storage-prereqs"},
		},
		"unhealthy storage system reconverges": {
			diagnosis: repairDiagnosis(StateServer, server, "storage system"),
			deps:      deps(server, false),
			wantIDs:   []string{"reconverge"},
		},
	}
	for name, tc := range cases {
		actions, refusals := PlanRepairs(tc.diagnosis, tc.deps)
		if len(tc.wantIDs) == 0 {
			require.Empty(t, actionIDs(actions), name)
		} else {
			require.Equal(t, tc.wantIDs, actionIDs(actions), name)
		}
		if tc.wantRefusal == "" {
			require.Empty(t, refusals, name)
		} else {
			require.Len(t, refusals, 1, name)
			require.Contains(t, refusals[0], tc.wantRefusal, name)
		}
	}
}

func TestPlanRepairsHealthy(t *testing.T) {
	t.Parallel()
	server := &Record{Node: NodeRecord{Name: "cp-1", Role: layout.RoleServer}}
	actions, refusals := PlanRepairs(repairDiagnosis(StateServer, server),
		RepairDeps{Runner: &host.Fake{}, Record: server})
	require.Empty(t, actions, "a clean diagnosis plans no mutation")
	require.Empty(t, refusals)
}

func TestPlanRepairsRestoresRecoveredRecord(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	fake := &host.Fake{FS: map[string][]byte{}}
	record := &Record{
		Version:        RecordVersion,
		InstallationID: "install-1",
		Provider:       ProviderK3s,
		Cluster:        "e2e",
		Ownership:      OwnershipManaged,
		Node:           NodeRecord{Name: "cp-1", Role: layout.RoleServer, Capabilities: []string{"edge"}},
		Versions:       Versions{Installer: "test", K3s: K3sVersion},
	}
	fake.FS[RecordPath] = []byte(":corrupt:\n\t")
	backup, err := yaml.Marshal(record)
	require.NoError(t, err)
	fake.FS[RecordBackupPath] = backup

	diagnosis := repairDiagnosis(StateDamaged, record, "installation")
	diagnosis.Host.RecordRecovered = true
	diagnosis.Host.Problems = []string{"using " + RecordBackupPath}
	actions, refusals := PlanRepairs(diagnosis, RepairDeps{Runner: fake, Record: record})
	require.Empty(t, refusals)
	require.Equal(t, []string{"restore-record"}, actionIDs(actions))
	require.NoError(t, actions[0].Run(ctx))

	restored, err := LoadRecord(ctx, fake)
	require.NoError(t, err)
	require.Equal(t, "install-1", restored.InstallationID)
	require.False(t, recordRecovered(ctx, fake))
}

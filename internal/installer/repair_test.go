package installer

import (
	"testing"

	"github.com/stretchr/testify/require"

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

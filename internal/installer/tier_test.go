package installer

import (
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/Hinkolas/skali/internal/bundle"
	"github.com/Hinkolas/skali/internal/layout"
)

func TestTierFromInstancesRoundTrip(t *testing.T) {
	t.Parallel()
	for _, tier := range []layout.Tier{layout.TierSingle, layout.TierAsynchronous, layout.TierSynchronous} {
		require.Equal(t, tier, bundle.TierFromInstances(bundle.TierInstances(tier)))
	}
	require.Equal(t, layout.TierSingle, bundle.TierFromInstances(0))
	require.Equal(t, layout.TierSynchronous, bundle.TierFromInstances(5))
}

func tierStatus() *Status {
	return &Status{
		Host: &Host{
			State:  StateServer,
			Record: &Record{Node: NodeRecord{Name: "cp-1", Role: layout.RoleServer}},
		},
		Initialized:       true,
		ClusterReachable:  true,
		BundleCurrent:     true,
		DeployedTier:      layout.TierSingle,
		AvailableTier:     layout.TierAsynchronous,
		DatabaseNodes:     []string{"cp-1", "db-1"},
		DatabaseInstances: 1,
	}
}

func TestPlanTier(t *testing.T) {
	t.Parallel()
	plan, err := PlanTier(tierStatus())
	require.NoError(t, err)
	require.Equal(t, layout.TierSingle, plan.Deployed)
	require.Equal(t, layout.TierAsynchronous, plan.Available)
	require.Equal(t, 2, plan.Instances)
	require.False(t, plan.Downgrade)
	require.False(t, plan.Nothing())
}

func TestPlanTierDowngrade(t *testing.T) {
	t.Parallel()
	status := tierStatus()
	status.DeployedTier = layout.TierSynchronous
	status.AvailableTier = layout.TierAsynchronous
	plan, err := PlanTier(status)
	require.NoError(t, err)
	require.True(t, plan.Downgrade)
	require.Equal(t, 2, plan.Instances)
}

func TestPlanTierNothing(t *testing.T) {
	t.Parallel()
	status := tierStatus()
	status.DeployedTier = layout.TierAsynchronous
	plan, err := PlanTier(status)
	require.NoError(t, err)
	require.True(t, plan.Nothing())
}

func TestPlanTierGuards(t *testing.T) {
	t.Parallel()
	cases := map[string]struct {
		mutate func(*Status)
		want   string
	}{
		"agent": {func(s *Status) {
			s.Host.Record.Node.Role = layout.RoleAgent
		}, "tier changes run on a server node"},
		"uninitialized": {func(s *Status) {
			s.Initialized = false
		}, "run skali cluster init first"},
		"unreachable": {func(s *Status) {
			s.ClusterReachable = false
		}, "skali cluster diagnose"},
		"secondary server": {func(s *Status) {
			s.InitOwner = "cp-1"
		}, "the bundle is maintained on cp-1"},
		"bundle not current": {func(s *Status) {
			s.BundleCurrent = false
		}, "run skali cluster upgrade first"},
		"database missing": {func(s *Status) {
			s.DeployedTier = ""
		}, "the bootstrap database was not found"},
	}
	for name, tc := range cases {
		status := tierStatus()
		tc.mutate(status)
		_, err := PlanTier(status)
		require.ErrorContains(t, err, tc.want, name)
	}
}

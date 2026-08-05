package layout

import (
	"testing"

	"github.com/stretchr/testify/require"
)

func TestTierInstancesRoundTrip(t *testing.T) {
	t.Parallel()
	require.Equal(t, 1, TierInstances(TierSingle))
	require.Equal(t, 2, TierInstances(TierAsynchronous))
	require.Equal(t, 3, TierInstances(TierSynchronous))
	for _, tier := range []Tier{TierSingle, TierAsynchronous, TierSynchronous} {
		require.Equal(t, tier, DeriveTier(TierInstances(tier)))
	}
	require.Equal(t, TierSingle, DeriveTier(0))
	require.Equal(t, TierSynchronous, DeriveTier(5))
}

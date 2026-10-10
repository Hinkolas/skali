package kube

import (
	"testing"

	"github.com/stretchr/testify/require"
	"k8s.io/client-go/rest"
)

// Zero QPS restores client-go's default budget, which is what the system
// observation reports: a configured burst does not survive it, since
// client-go would default the rate alone and keep that burst.
func TestZeroQPSKeepsClientGoDefaults(t *testing.T) {
	config := &rest.Config{}
	RequestBudget{Burst: 100}.apply(config)
	require.Zero(t, config.QPS)
	require.Zero(t, config.Burst)
	require.Equal(t, RequestBudget{QPS: rest.DefaultQPS, Burst: rest.DefaultBurst}, RequestBudget{Burst: 100}.Effective())

	RequestBudget{QPS: 50, Burst: 100}.apply(config)
	require.Equal(t, float32(50), config.QPS)
	require.Equal(t, 100, config.Burst)
}

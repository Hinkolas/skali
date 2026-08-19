package authz

import (
	"testing"

	"github.com/stretchr/testify/require"
)

func TestPolicyPredicates(t *testing.T) {
	t.Parallel()
	direct := &EnvironmentGrant{Name: "staging", Settings: Settings{DeployPolicy: DeployPolicyDirect, PromoteFrom: []string{"feat-x"}}}
	require.False(t, direct.Protected())
	// promote_from only matters under promote-only.
	require.True(t, direct.AcceptsPromotionFrom("staging"))

	any := &EnvironmentGrant{Name: "production", Settings: Settings{DeployPolicy: DeployPolicyPromoteOnly}}
	require.True(t, any.Protected())
	require.True(t, any.AcceptsPromotionFrom("staging"))
	require.True(t, any.AcceptsPromotionFrom("feat-x"))

	listed := &EnvironmentGrant{Name: "production", Settings: Settings{DeployPolicy: DeployPolicyPromoteOnly, PromoteFrom: []string{"staging", "qa"}}}
	require.True(t, listed.AcceptsPromotionFrom("staging"))
	require.True(t, listed.AcceptsPromotionFrom("qa"))
	require.False(t, listed.AcceptsPromotionFrom("feat-x"))
}

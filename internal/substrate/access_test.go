package substrate

import (
	"context"
	"testing"

	"github.com/stretchr/testify/require"
	networkingv1 "k8s.io/api/networking/v1"

	"github.com/Hinkolas/skali/internal/claim"
	"github.com/Hinkolas/skali/internal/kubernetes"
	"github.com/Hinkolas/skali/internal/substrate/cnpg"
)

// admittedEnvironments lists the environment ids a policy's holder rules
// admit, in rendered order.
func admittedEnvironments(policy *networkingv1.NetworkPolicy) []string {
	var ids []string
	if policy == nil {
		return ids
	}
	for _, rule := range policy.Spec.Ingress {
		for _, peer := range rule.From {
			if peer.NamespaceSelector == nil {
				continue
			}
			if id, ok := peer.NamespaceSelector.MatchLabels[kubernetes.LabelEnvironment]; ok {
				ids = append(ids, id)
			}
		}
	}
	return ids
}

// TestPoolAccessFollowsClaims pins the pool side of "platform ports admit
// only claim holders": the pass that places a claim admits its environment
// to the pool before the claim is provisioned, and releasing the only
// claim narrows the surviving shared pool's policy back to no holder.
func TestPoolAccessFollowsClaims(t *testing.T) {
	fx := newSettleFixture(t)
	ctx := context.Background()
	policyName := cnpg.AccessPolicyName(sharedPoolName(DefaultEngine, DefaultMajor))

	// A waiting pass (pool not ready) has already placed the claim: the
	// policy admits the environment while the pool is still coming up.
	fx.fake.set("Setting up primary", 0, false)
	_, phase := fx.pass(t)
	require.Equal(t, claim.PhaseBound, phase)
	require.Equal(t, []string{fx.envID.String()}, admittedEnvironments(fx.fake.policy(policyName)),
		"placement admits the holder before provisioning completes")

	fx.fake.set(clusterHealthyPhase, 1, true)
	_, phase = fx.pass(t)
	require.Equal(t, claim.PhaseProvisioned, phase)
	require.Equal(t, []string{fx.envID.String()}, admittedEnvironments(fx.fake.policy(policyName)))

	// Releasing the only claim: the shared pool survives with a policy that
	// admits no environment, but still its fixed peers.
	_, err := fx.db.ReleaseClaim(ctx, fx.claim.ID)
	require.NoError(t, err)
	_, phase = fx.pass(t)
	require.Equal(t, claim.PhaseReleased, phase)
	released := fx.fake.policy(policyName)
	require.NotNil(t, released)
	require.Empty(t, admittedEnvironments(released))
	for i, rule := range released.Spec.Ingress {
		require.NotEmpty(t, rule.From, "rule %d admits everyone", i)
	}
}

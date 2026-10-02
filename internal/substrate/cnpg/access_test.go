package cnpg

import (
	"testing"

	"github.com/stretchr/testify/require"
	networkingv1 "k8s.io/api/networking/v1"

	"github.com/Hinkolas/skali/internal/kubernetes"
	"github.com/Hinkolas/skali/internal/platform"
)

// The pool policy admits the pool's own pods and the operator on every
// port, the platform namespace on the postgres port, and nothing else
// unless peers are given: a rule without peers would admit everyone.
func TestRenderAccessPolicyFixedPeersOnly(t *testing.T) {
	t.Parallel()
	policy := RenderAccessPolicy("skali-platform", "pg17-shared", platform.AccessPeers{})

	require.Equal(t, "pg17-shared-access", policy.Name)
	require.Equal(t, "skali-platform", policy.Namespace)
	require.Equal(t, "pg17-shared", policy.Labels[kubernetes.LabelPool])
	require.Equal(t, map[string]string{LabelCluster: "pg17-shared"}, policy.Spec.PodSelector.MatchLabels)
	require.Equal(t, []networkingv1.PolicyType{networkingv1.PolicyTypeIngress}, policy.Spec.PolicyTypes)
	require.Len(t, policy.Spec.Ingress, 3)

	own := policy.Spec.Ingress[0]
	require.Empty(t, own.Ports, "pool pods reach each other on every port")
	require.Equal(t, map[string]string{LabelCluster: "pg17-shared"}, own.From[0].PodSelector.MatchLabels)
	require.Nil(t, own.From[0].NamespaceSelector, "same namespace only")

	operator := policy.Spec.Ingress[1]
	require.Empty(t, operator.Ports)
	require.Equal(t, map[string]string{"kubernetes.io/metadata.name": OperatorNamespace}, operator.From[0].NamespaceSelector.MatchLabels)

	platformNS := policy.Spec.Ingress[2]
	require.Equal(t, map[string]string{"kubernetes.io/metadata.name": "skali-platform"}, platformNS.From[0].NamespaceSelector.MatchLabels)
	require.Len(t, platformNS.Ports, 1)
	require.EqualValues(t, 5432, platformNS.Ports[0].Port.IntValue())

	requireNoOpenRule(t, policy)
}

// Holders are admitted by their environment label, once each and in a
// stable order; the proxy sources reach postgres and the exporter; a local
// platform admits every non-pod source on postgres.
func TestRenderAccessPolicyPeers(t *testing.T) {
	t.Parallel()
	policy := RenderAccessPolicy("skali-platform", "pg17-shared", platform.AccessPeers{
		Environments: []string{"env-b", "env-a", "env-a"},
		ProxyCIDRs:   []string{"10.42.0.0/32", "10.42.0.1/32"},
		HostExcept:   []string{"10.42.0.0/24"},
	})
	require.Len(t, policy.Spec.Ingress, 6)

	holders := policy.Spec.Ingress[3]
	require.Len(t, holders.From, 2)
	require.Equal(t, "env-a", holders.From[0].NamespaceSelector.MatchLabels[kubernetes.LabelEnvironment])
	require.Equal(t, "env-b", holders.From[1].NamespaceSelector.MatchLabels[kubernetes.LabelEnvironment])
	require.Len(t, holders.Ports, 1)
	require.EqualValues(t, 5432, holders.Ports[0].Port.IntValue())

	proxies := policy.Spec.Ingress[4]
	require.Len(t, proxies.From, 2)
	require.Equal(t, "10.42.0.0/32", proxies.From[0].IPBlock.CIDR)
	require.Len(t, proxies.Ports, 2)
	require.EqualValues(t, 5432, proxies.Ports[0].Port.IntValue())
	require.EqualValues(t, MetricsPort, proxies.Ports[1].Port.IntValue())

	host := policy.Spec.Ingress[5]
	require.Len(t, host.From, 1)
	require.Equal(t, "0.0.0.0/0", host.From[0].IPBlock.CIDR)
	require.Equal(t, []string{"10.42.0.0/24"}, host.From[0].IPBlock.Except)
	require.Len(t, host.Ports, 1)
	require.EqualValues(t, 5432, host.Ports[0].Port.IntValue())

	requireNoOpenRule(t, policy)
}

// An empty but non-nil HostExcept still renders the host peer (a local
// platform without observed nodes admits the host); nil renders none, the
// managed shape.
func TestRenderAccessPolicyHostPeerPresence(t *testing.T) {
	t.Parallel()
	local := RenderAccessPolicy("skali-platform", "pg17-shared", platform.AccessPeers{HostExcept: []string{}})
	require.Len(t, local.Spec.Ingress, 4)
	require.Equal(t, "0.0.0.0/0", local.Spec.Ingress[3].From[0].IPBlock.CIDR)
	require.Empty(t, local.Spec.Ingress[3].From[0].IPBlock.Except)

	managed := RenderAccessPolicy("skali-platform", "pg17-shared", platform.AccessPeers{Environments: []string{"env-a"}})
	require.Len(t, managed.Spec.Ingress, 4)
	require.Nil(t, managed.Spec.Ingress[3].From[0].IPBlock)
}

// requireNoOpenRule asserts the invariant every access policy keeps: no
// ingress rule without peers, since that admits everyone.
func requireNoOpenRule(t *testing.T, policy *networkingv1.NetworkPolicy) {
	t.Helper()
	for i, rule := range policy.Spec.Ingress {
		require.NotEmpty(t, rule.From, "rule %d of %s admits everyone", i, policy.Name)
	}
}

package seaweed

import (
	"testing"

	"github.com/stretchr/testify/require"
	networkingv1 "k8s.io/api/networking/v1"

	"github.com/Hinkolas/skali/internal/bundle"
	"github.com/Hinkolas/skali/internal/platform"
)

// The fence no longer opens the S3 port to every pod: only the internal
// policy remains, and the access policy carries the admit list.
func TestRenderFenceClosesS3(t *testing.T) {
	t.Parallel()
	objects := RenderFence("skali-platform")
	require.Len(t, objects, 1)
	policy, ok := objects[0].(*networkingv1.NetworkPolicy)
	require.True(t, ok)
	require.Equal(t, "seaweed-internal", policy.Name)
	require.NotEqual(t, LegacyS3OpenPolicy, policy.Name)
}

// Without holders the S3 port admits exactly the platform's fixed peers:
// the skalid pod and the edge, each pinned to its namespace and pod label.
func TestRenderS3AccessPolicyFixedPeers(t *testing.T) {
	t.Parallel()
	policy := RenderS3AccessPolicy("skali-platform", platform.AccessPeers{})

	require.Equal(t, S3AccessPolicy, policy.Name)
	require.Equal(t, map[string]string{SystemLabel: "object-storage"}, policy.Spec.PodSelector.MatchLabels)
	require.Equal(t, []networkingv1.PolicyType{networkingv1.PolicyTypeIngress}, policy.Spec.PolicyTypes)
	require.Len(t, policy.Spec.Ingress, 1)

	fixed := policy.Spec.Ingress[0]
	require.Len(t, fixed.From, 2)
	require.Equal(t, bundle.Namespace, fixed.From[0].NamespaceSelector.MatchLabels["kubernetes.io/metadata.name"])
	require.Equal(t, "skalid", fixed.From[0].PodSelector.MatchLabels["app.kubernetes.io/name"])
	require.Equal(t, "kube-system", fixed.From[1].NamespaceSelector.MatchLabels["kubernetes.io/metadata.name"])
	require.Equal(t, "traefik", fixed.From[1].PodSelector.MatchLabels["app.kubernetes.io/name"])
	requireOnlyS3Port(t, policy)
}

// Holders are admitted by environment label, once each and sorted; the
// proxy sources and, on a local platform, every non-pod source follow as
// their own rules, all on the S3 port only.
func TestRenderS3AccessPolicyPeers(t *testing.T) {
	t.Parallel()
	policy := RenderS3AccessPolicy("skali-platform", platform.AccessPeers{
		Environments: []string{"env-b", "env-a", "env-a"},
		ProxyCIDRs:   []string{"10.42.0.1/32"},
		HostExcept:   []string{"10.42.0.0/24"},
	})
	require.Len(t, policy.Spec.Ingress, 4)

	holders := policy.Spec.Ingress[1]
	require.Len(t, holders.From, 2)
	require.Equal(t, "env-a", holders.From[0].NamespaceSelector.MatchLabels[platform.EnvironmentLabel])
	require.Equal(t, "env-b", holders.From[1].NamespaceSelector.MatchLabels[platform.EnvironmentLabel])
	require.Nil(t, holders.From[0].PodSelector, "every pod of a holder")

	require.Equal(t, "10.42.0.1/32", policy.Spec.Ingress[2].From[0].IPBlock.CIDR)

	host := policy.Spec.Ingress[3].From[0].IPBlock
	require.Equal(t, "0.0.0.0/0", host.CIDR)
	require.Equal(t, []string{"10.42.0.0/24"}, host.Except)
	requireOnlyS3Port(t, policy)
}

// A nil HostExcept (managed) renders no host peer; an empty one (a local
// platform before any node is observed) still does.
func TestRenderS3AccessPolicyHostPeerPresence(t *testing.T) {
	t.Parallel()
	managed := RenderS3AccessPolicy("skali-platform", platform.AccessPeers{ProxyCIDRs: []string{"10.42.0.1/32"}})
	require.Len(t, managed.Spec.Ingress, 2)
	local := RenderS3AccessPolicy("skali-platform", platform.AccessPeers{HostExcept: []string{}})
	require.Len(t, local.Spec.Ingress, 2)
	require.Equal(t, "0.0.0.0/0", local.Spec.Ingress[1].From[0].IPBlock.CIDR)
}

// requireOnlyS3Port asserts every rule names peers (a rule without any
// admits everyone) and opens nothing but the S3 port.
func requireOnlyS3Port(t *testing.T, policy *networkingv1.NetworkPolicy) {
	t.Helper()
	for i, rule := range policy.Spec.Ingress {
		require.NotEmpty(t, rule.From, "rule %d admits everyone", i)
		require.Len(t, rule.Ports, 1, "rule %d", i)
		require.EqualValues(t, S3Port, rule.Ports[0].Port.IntValue())
	}
}

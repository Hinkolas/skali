package proxytrust

import (
	"context"
	"errors"
	"net/netip"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/client-go/kubernetes/fake"
	ktesting "k8s.io/client-go/testing"
)

func TestProxyDiscovery(t *testing.T) {
	pod := func(namespace, label, ip string, host bool) *corev1.Pod {
		return &corev1.Pod{ObjectMeta: metav1.ObjectMeta{Name: ip, Namespace: namespace, Labels: map[string]string{"app.kubernetes.io/name": label}},
			Spec: corev1.PodSpec{HostNetwork: host}, Status: corev1.PodStatus{Phase: corev1.PodRunning, PodIPs: []corev1.PodIP{{IP: ip}}}}
	}
	client := fake.NewClientset(
		pod("kube-system", "traefik", "10.42.0.1", false),
		pod("skali-system", "skali-web", "10.42.0.2", false),
		pod("application", "traefik", "10.42.0.3", false),
		pod("kube-system", "other", "10.42.0.4", false),
		pod("kube-system", "traefik", "10.42.0.5", true),
	)
	trust := New(nil)
	trust.refresh(context.Background(), client)
	for _, ip := range []string{"10.42.0.1", "10.42.0.2"} {
		require.True(t, trust.Contains(netip.MustParseAddr(ip)))
	}
	for _, ip := range []string{"10.42.0.3", "10.42.0.4", "10.42.0.5"} {
		require.False(t, trust.Contains(netip.MustParseAddr(ip)))
	}
	require.NoError(t, client.CoreV1().Pods("kube-system").Delete(context.Background(), "10.42.0.1", metav1.DeleteOptions{}))
	trust.refresh(context.Background(), client)
	require.False(t, trust.Contains(netip.MustParseAddr("10.42.0.1")))
	trust.updated = time.Now().Add(-maxAge)
	require.False(t, trust.Contains(netip.MustParseAddr("10.42.0.2")), "stale discovery must fail closed")
	trust.refresh(context.Background(), client)
	client.PrependReactor("list", "pods", func(ktesting.Action) (bool, runtime.Object, error) { return true, nil, errors.New("unavailable") })
	trust.refresh(context.Background(), client)
	require.False(t, trust.Contains(netip.MustParseAddr("10.42.0.2")), "API failure must clear dynamic trust")
}

func TestConfiguredProxies(t *testing.T) {
	prefixes, err := Parse("127.0.0.1/32, ::1/128")
	require.NoError(t, err)
	trust := New(prefixes)
	require.True(t, trust.Contains(netip.MustParseAddr("::ffff:127.0.0.1")))
	require.True(t, trust.Contains(netip.MustParseAddr("::1")))
	require.False(t, trust.Contains(netip.MustParseAddr("192.0.2.1")))
	for _, bad := range []string{"127.0.0.1", "10.0.0.0/33", "127.0.0.1/32,"} {
		_, err := Parse(bad)
		require.Error(t, err)
	}
}

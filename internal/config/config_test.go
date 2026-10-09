package config

import (
	"context"
	"testing"

	"github.com/stretchr/testify/require"
)

// The local platform runs cert-manager without the edge probe, so the two
// switches must be independent: the probe defaults on and turns off alone.
func TestEdgeProbeIsIndependentOfCertManager(t *testing.T) {
	t.Setenv("DATABASE_URL", "postgres://skali@localhost/skali")
	t.Setenv("AUTH_SECRET", "test-secret-test-secret-test-secret")

	cfg, err := Load[API](context.Background())
	require.NoError(t, err)
	require.False(t, cfg.CertManager)
	require.True(t, cfg.EdgeProbe)

	t.Setenv("SKALI_CERT_MANAGER", "true")
	t.Setenv("SKALI_EDGE_PROBE", "false")
	cfg, err = Load[API](context.Background())
	require.NoError(t, err)
	require.True(t, cfg.CertManager)
	require.False(t, cfg.EdgeProbe)
}

// The request budget defaults well above client-go's 5 per second, and a
// set QPS needs a burst (client-go rejects the clientset otherwise).
func TestKubeRequestBudget(t *testing.T) {
	t.Setenv("DATABASE_URL", "postgres://skali@localhost/skali")
	t.Setenv("AUTH_SECRET", "test-secret-test-secret-test-secret")

	cfg, err := Load[API](context.Background())
	require.NoError(t, err)
	require.Equal(t, float32(50), cfg.KubeQPS)
	require.Equal(t, 100, cfg.KubeBurst)

	t.Setenv("SKALI_KUBE_QPS", "5")
	t.Setenv("SKALI_KUBE_BURST", "0")
	_, err = Load[API](context.Background())
	require.ErrorContains(t, err, "SKALI_KUBE_BURST")
}

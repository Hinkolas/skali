package config

import (
	"context"
	"os"
	"path/filepath"
	"testing"

	"github.com/sethvargo/go-envconfig"
	"github.com/stretchr/testify/require"
	"gopkg.in/yaml.v3"
)

// unsetenv removes key for the rest of the test and restores it afterwards;
// task runs the suite with .env loaded, so variables may be set on entry.
func unsetenv(t *testing.T, key string) {
	t.Helper()
	t.Setenv(key, "")
	require.NoError(t, os.Unsetenv(key))
}

// A .env in the working directory must never reach a binary: production
// configuration comes from the process environment alone, and a missing
// required value fails naming the variable and where values come from.
func TestLoadIgnoresDotenvFile(t *testing.T) {
	dir := t.TempDir()
	require.NoError(t, os.WriteFile(filepath.Join(dir, ".env"), []byte(
		"DATABASE_URL=postgres://dotenv@localhost/skali\n"+
			"AUTH_SECRET=dotenv-secret-dotenv-secret-dotenv\n"+
			"LOG_LEVEL=debug\n"), 0o600))
	t.Chdir(dir)
	for _, key := range []string{"DATABASE_URL", "AUTH_SECRET", "LOG_LEVEL"} {
		unsetenv(t, key)
	}

	_, err := Load[Base](context.Background())
	require.ErrorIs(t, err, envconfig.ErrMissingRequired)
	require.ErrorContains(t, err, "DATABASE_URL")
	require.ErrorContains(t, err, "environment only")

	t.Setenv("DATABASE_URL", "postgres://env@localhost/skali")
	_, err = Load[API](context.Background())
	require.ErrorContains(t, err, "AUTH_SECRET")

	cfg, err := Load[Base](context.Background())
	require.NoError(t, err)
	require.Equal(t, "postgres://env@localhost/skali", cfg.DatabaseURL)
	require.Equal(t, "info", cfg.LogLevel)
}

// The Taskfile is the one development path that loads .env (task keeps
// variables already set in the shell ahead of the file), so every task that
// runs skalid or the tests sees the developer's configuration.
func TestTaskfileLoadsDotenv(t *testing.T) {
	data, err := os.ReadFile(filepath.Join("..", "..", "Taskfile.yml"))
	require.NoError(t, err)
	var taskfile struct {
		Dotenv []string `yaml:"dotenv"`
	}
	require.NoError(t, yaml.Unmarshal(data, &taskfile))
	require.Contains(t, taskfile.Dotenv, ".env")
}

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

package engine

import (
	"testing"
	"time"

	"github.com/moby/moby/api/types/container"
	"github.com/moby/moby/api/types/mount"
	"github.com/moby/moby/api/types/network"
	"github.com/stretchr/testify/require"
)

func TestSpecConfigsStampsOwnership(t *testing.T) {
	cfg, _, _, err := specConfigs(ContainerSpec{
		Image:  "nginx:alpine",
		Labels: map[string]string{LabelKind: KindApplication, LabelManaged: "false", "team": "web"},
	})
	require.NoError(t, err)
	require.Equal(t, "true", cfg.Labels[LabelManaged], "ownership stamp must win over caller labels")
	require.Equal(t, KindApplication, cfg.Labels[LabelKind])
	require.Equal(t, "web", cfg.Labels["team"])
}

func TestSpecConfigsEnvSortedDeterministically(t *testing.T) {
	cfg, _, _, err := specConfigs(ContainerSpec{
		Image: "img",
		Env:   map[string]string{"ZED": "1", "ALPHA": "2", "MID": "3"},
	})
	require.NoError(t, err)
	require.Equal(t, []string{"ALPHA=2", "MID=3", "ZED=1"}, cfg.Env)
}

func TestSpecConfigsPorts(t *testing.T) {
	cfg, host, _, err := specConfigs(ContainerSpec{
		Image: "img",
		Ports: []PortBinding{
			{HostPort: 8080, ContainerPort: 80},                             // proto defaults to tcp
			{HostIP: "127.0.0.1", ContainerPort: 53, Protocol: "udp"},       // ephemeral host port
			{HostPort: 8443, ContainerPort: 443, Protocol: "tcp"},
		},
	})
	require.NoError(t, err)

	tcp80 := network.MustParsePort("80/tcp")
	udp53 := network.MustParsePort("53/udp")
	require.Contains(t, cfg.ExposedPorts, tcp80)
	require.Contains(t, cfg.ExposedPorts, udp53)
	require.Equal(t, "8080", host.PortBindings[tcp80][0].HostPort)
	require.Empty(t, host.PortBindings[udp53][0].HostPort, "host port 0 = ephemeral")
	require.Equal(t, "127.0.0.1", host.PortBindings[udp53][0].HostIP.String())

	for _, bad := range []PortBinding{
		{ContainerPort: 0, HostPort: 1},
		{ContainerPort: 80, Protocol: "sctp"},
		{ContainerPort: 80, HostIP: "not-an-ip"},
	} {
		_, _, _, err := specConfigs(ContainerSpec{Image: "img", Ports: []PortBinding{bad}})
		require.Error(t, err, "binding %+v", bad)
	}
}

func TestSpecConfigsRestartPolicy(t *testing.T) {
	_, host, _, err := specConfigs(ContainerSpec{Image: "img"})
	require.NoError(t, err)
	require.Empty(t, host.RestartPolicy.Name, "default is the daemon default (no restart)")

	_, host, _, err = specConfigs(ContainerSpec{Image: "img", Restart: RestartOnFailure, RestartMaxRetries: 3})
	require.NoError(t, err)
	require.Equal(t, container.RestartPolicyOnFailure, host.RestartPolicy.Name)
	require.Equal(t, 3, host.RestartPolicy.MaximumRetryCount)

	_, _, _, err = specConfigs(ContainerSpec{Image: "img", Restart: RestartAlways, RestartMaxRetries: 3})
	require.Error(t, err, "max retries only makes sense with on-failure")

	_, _, _, err = specConfigs(ContainerSpec{Image: "img", Restart: "sometimes"})
	require.Error(t, err)
}

func TestSpecConfigsMounts(t *testing.T) {
	_, host, _, err := specConfigs(ContainerSpec{Image: "img", Mounts: []Mount{
		{Type: "volume", Source: "data", Target: "/data"},
		{Type: "bind", Source: "/host", Target: "/ctr", ReadOnly: true},
	}})
	require.NoError(t, err)
	require.Equal(t, mount.TypeVolume, host.Mounts[0].Type)
	require.Equal(t, mount.TypeBind, host.Mounts[1].Type)
	require.True(t, host.Mounts[1].ReadOnly)

	for _, bad := range []Mount{
		{Type: "tmpfs", Source: "x", Target: "/x"},
		{Type: "volume", Target: "/x"},
		{Type: "bind", Source: "/x"},
	} {
		_, _, _, err := specConfigs(ContainerSpec{Image: "img", Mounts: []Mount{bad}})
		require.Error(t, err, "mount %+v", bad)
	}
}

func TestSpecConfigsHealthcheckAndResources(t *testing.T) {
	cfg, host, _, err := specConfigs(ContainerSpec{
		Image:       "img",
		NanoCPUs:    5e8,
		MemoryLimit: 1 << 28,
		Healthcheck: &Healthcheck{
			Test:     []string{"CMD-SHELL", "true"},
			Interval: 10 * time.Second, Timeout: 5 * time.Second,
			StartPeriod: 3 * time.Second, Retries: 4,
		},
	})
	require.NoError(t, err)
	require.Equal(t, int64(5e8), host.NanoCPUs)
	require.Equal(t, int64(1<<28), host.Memory)
	require.Equal(t, []string{"CMD-SHELL", "true"}, cfg.Healthcheck.Test)
	require.Equal(t, 10*time.Second, cfg.Healthcheck.Interval)
	require.Equal(t, 4, cfg.Healthcheck.Retries)
}

func TestSpecConfigsRequiresImage(t *testing.T) {
	_, _, _, err := specConfigs(ContainerSpec{Name: "x"})
	require.Error(t, err)
}

func TestValidateKind(t *testing.T) {
	for _, kind := range Kinds {
		require.NoError(t, ValidateKind(kind))
	}
	require.Error(t, ValidateKind(""))
	require.Error(t, ValidateKind("Application"))
	require.Error(t, ValidateKind("internal"))
}

func TestValidateUserLabels(t *testing.T) {
	require.NoError(t, ValidateUserLabels(map[string]string{"team": "web", "com.example.x": "1"}))
	require.Error(t, ValidateUserLabels(map[string]string{LabelKind: KindSystem}))
	require.Error(t, ValidateUserLabels(map[string]string{"skali.custom": "1"}))
}

func TestDeriveStats(t *testing.T) {
	base := time.Now()
	prev := RawStats{At: base, CPUTotalNs: 10e9, NetRx: 1_000, NetTx: 500}
	cur := RawStats{
		At: base.Add(10 * time.Second), CPUTotalNs: 15e9, OnlineCPUs: 4,
		MemUsed: 64, MemLimit: 256, NetRx: 11_000, NetTx: 500,
	}
	st := deriveStats(prev, cur)
	require.InDelta(t, 50.0, st.CPUPercent, 0.01, "5 cpu-seconds over 10s = half a core")
	require.Equal(t, uint64(1_000), st.NetRxRate)
	require.Equal(t, uint64(0), st.NetTxRate)
	require.Equal(t, uint64(64), st.MemUsed)
	require.Equal(t, uint64(256), st.MemLimit)
}

func TestDeriveStatsClampsAndSurvivesResets(t *testing.T) {
	base := time.Now()
	// Impossible delta: clamped to the visible core count.
	st := deriveStats(
		RawStats{At: base, CPUTotalNs: 0},
		RawStats{At: base.Add(time.Second), CPUTotalNs: 30e9, OnlineCPUs: 2},
	)
	require.Equal(t, 200.0, st.CPUPercent)

	// Counter reset (container restart): zero, never negative.
	st = deriveStats(
		RawStats{At: base, CPUTotalNs: 50e9, NetRx: 9_000},
		RawStats{At: base.Add(time.Second), CPUTotalNs: 1e9, NetRx: 100, OnlineCPUs: 2},
	)
	require.Equal(t, 0.0, st.CPUPercent)
	require.Equal(t, uint64(0), st.NetRxRate)
}

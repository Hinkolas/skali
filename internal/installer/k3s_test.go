package installer

import (
	"context"
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/Hinkolas/skali/internal/installer/host"
	"github.com/Hinkolas/skali/internal/layout"
)

func TestK3sConfigYAML(t *testing.T) {
	t.Parallel()
	rendered := k3sConfigYAML("cp-1", "production", layout.Capabilities)
	require.Equal(t, `node-name: cp-1
embedded-registry: true
node-label:
  - skali.dev/capability-application=true
  - skali.dev/capability-database=true
  - skali.dev/capability-edge=true
  - skali.dev/capability-object-storage=true
  - skali.dev/capability-registry=true
  - skali.dev/cluster=production
`, rendered)
}

func TestK3sRegistriesYAML(t *testing.T) {
	t.Parallel()
	require.Equal(t, `mirrors:
  "*":
  "registry.skali.internal":
    endpoint:
      - "http://127.0.0.1:30500"
`, k3sRegistriesYAML())
}

func TestInstallK3sInvocation(t *testing.T) {
	t.Parallel()
	fake := &host.Fake{Handlers: map[string]func(host.Command) (host.Result, error){
		"sh": func(host.Command) (host.Result, error) { return host.Result{}, nil },
	}}
	err := installK3s(context.Background(), fake, "cp-1", "production",
		[]string{layout.CapabilityApplication}, silentProgress{})
	require.NoError(t, err)

	// Configs are written before the script runs.
	require.Contains(t, fake.FS, K3sConfigPath)
	require.Contains(t, fake.FS, K3sRegistriesPath)
	require.Len(t, fake.Commands, 1)
	command := fake.Commands[0]
	require.Equal(t, "sh", command.Name)
	require.Equal(t, []string{k3sInstallScriptPath}, command.Args)
	require.Contains(t, command.Env, "INSTALL_K3S_VERSION="+K3sVersion)

	// The vendored script itself was staged executable.
	require.NotEmpty(t, fake.FS[k3sInstallScriptPath])
}

func TestInstallK3sScriptFailure(t *testing.T) {
	t.Parallel()
	fake := &host.Fake{Handlers: map[string]func(host.Command) (host.Result, error){
		"sh": func(host.Command) (host.Result, error) {
			return host.Result{ExitCode: 1, Stderr: "curl: (6) could not resolve host\n"}, nil
		},
	}}
	err := installK3s(context.Background(), fake, "cp-1", "production",
		[]string{layout.CapabilityApplication}, silentProgress{})
	require.ErrorContains(t, err, "exit code 1")
	require.ErrorContains(t, err, "could not resolve host")
}

func TestUninstallK3sPicksRoleScript(t *testing.T) {
	t.Parallel()
	for role, script := range map[string]string{
		layout.RoleServer: "/usr/local/bin/k3s-uninstall.sh",
		layout.RoleAgent:  "/usr/local/bin/k3s-agent-uninstall.sh",
	} {
		fake := &host.Fake{Handlers: map[string]func(host.Command) (host.Result, error){
			script: func(host.Command) (host.Result, error) { return host.Result{}, nil },
		}}
		require.NoError(t, uninstallK3s(context.Background(), fake, role), role)
		require.Equal(t, script, fake.Commands[0].Name, role)
	}
}

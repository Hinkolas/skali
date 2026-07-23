package installer

import (
	"context"
	"strings"
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/Hinkolas/skali/internal/installer/host"
	"github.com/Hinkolas/skali/internal/layout"
)

func TestK3sConfigYAML(t *testing.T) {
	t.Parallel()
	rendered := k3sConfigYAML(k3sNode{Name: "cp-1", Cluster: "production", Capabilities: layout.Capabilities})
	require.Equal(t, `node-name: cp-1
cluster-init: true
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

func TestK3sConfigYAMLServerNodeIP(t *testing.T) {
	t.Parallel()
	rendered := k3sConfigYAML(k3sNode{
		Name: "cp-1", Cluster: "production",
		Capabilities: []string{layout.CapabilityEdge},
		NodeIP:       "192.168.64.5",
	})
	require.Equal(t, `node-name: cp-1
node-ip: 192.168.64.5
cluster-init: true
embedded-registry: true
node-label:
  - skali.dev/capability-edge=true
  - skali.dev/cluster=production
`, rendered)
}

func TestK3sConfigYAMLJoiningServer(t *testing.T) {
	t.Parallel()
	rendered := k3sConfigYAML(k3sNode{
		Name: "cp-2", Cluster: "production",
		Role:         layout.RoleServer,
		Capabilities: []string{layout.CapabilityApplication},
		ServerURL:    "https://192.168.64.5:6443",
		Token:        "secret",
	})
	require.Equal(t, `node-name: cp-2
server: https://192.168.64.5:6443
token-file: /etc/rancher/k3s/token
embedded-registry: true
node-label:
  - skali.dev/capability-application=true
  - skali.dev/cluster=production
`, rendered)
	require.NotContains(t, rendered, "cluster-init",
		"only the first server initializes the etcd cluster")
	require.NotContains(t, rendered, "secret", "the token itself never lands in config.yaml")
}

func TestK3sConfigYAMLAgent(t *testing.T) {
	t.Parallel()
	rendered := k3sConfigYAML(k3sNode{
		Name: "db-1", Cluster: "production",
		Role:         layout.RoleAgent,
		Capabilities: []string{layout.CapabilityDatabase},
		ServerURL:    "https://192.168.64.5:6443",
		Token:        "secret",
	})
	require.Equal(t, `node-name: db-1
server: https://192.168.64.5:6443
token-file: /etc/rancher/k3s/token
node-label:
  - skali.dev/capability-database=true
  - skali.dev/cluster=production
`, rendered)
	require.NotContains(t, rendered, "embedded-registry",
		"embedded-registry is a server-only flag and fatal on agents")
	require.NotContains(t, rendered, "secret", "the token itself never lands in config.yaml")
}

func TestK3sConfigYAMLAgentNodeIP(t *testing.T) {
	t.Parallel()
	rendered := k3sConfigYAML(k3sNode{
		Name: "db-1", Cluster: "e2e",
		Role:         layout.RoleAgent,
		Capabilities: []string{layout.CapabilityDatabase},
		NodeIP:       "192.168.64.6",
		ServerURL:    "https://192.168.64.5:6443",
		Token:        "secret",
	})
	require.Equal(t, `node-name: db-1
node-ip: 192.168.64.6
server: https://192.168.64.5:6443
token-file: /etc/rancher/k3s/token
node-label:
  - skali.dev/capability-database=true
  - skali.dev/cluster=e2e
`, rendered)
}

func TestK3sRegistriesYAML(t *testing.T) {
	t.Parallel()
	require.Equal(t, `mirrors:
  "*":
  "registry.skali.internal":
    endpoint:
      - "http://127.0.0.1:30500"
`, k3sRegistriesYAML(""))

	// The pull credential attaches under both the endpoint host and the
	// registry name; the rendered file must round-trip through the parser
	// token and init read it back with.
	withCredential := k3sRegistriesYAML("pull-secret-value")
	require.Equal(t, `mirrors:
  "*":
  "registry.skali.internal":
    endpoint:
      - "http://127.0.0.1:30500"
configs:
  "127.0.0.1:30500":
    auth:
      username: skali-node
      password: "pull-secret-value"
  "registry.skali.internal":
    auth:
      username: skali-node
      password: "pull-secret-value"
`, withCredential)
	require.Equal(t, "pull-secret-value", registriesPullSecret([]byte(withCredential)))
	require.Empty(t, registriesPullSecret([]byte(k3sRegistriesYAML(""))))
}

func TestInstallK3sInvocation(t *testing.T) {
	t.Parallel()
	fake := &host.Fake{Handlers: map[string]func(host.Command) (host.Result, error){
		"sh": func(host.Command) (host.Result, error) { return host.Result{}, nil },
	}}
	err := installK3s(context.Background(), fake, k3sNode{
		Name: "cp-1", Cluster: "production",
		Capabilities: []string{layout.CapabilityApplication},
	}, silentProgress{})
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

func TestInstallK3sAgentInvocation(t *testing.T) {
	t.Parallel()
	fake := &host.Fake{Handlers: map[string]func(host.Command) (host.Result, error){
		"sh": func(host.Command) (host.Result, error) { return host.Result{}, nil },
	}}
	err := installK3s(context.Background(), fake, k3sNode{
		Name: "db-1", Cluster: "production",
		Role:         layout.RoleAgent,
		Capabilities: []string{layout.CapabilityDatabase},
		ServerURL:    "https://cp-1.internal:6443",
		Token:        "K10abc::node:secret",
	}, silentProgress{})
	require.NoError(t, err)

	// The token file lands 0600 before the script runs.
	require.Equal(t, []byte("K10abc::node:secret\n"), fake.FS[K3sTokenPath])
	require.Less(t, indexOf(fake.Writes, "write "+K3sTokenPath),
		indexOf(fake.Writes, "write "+k3sInstallScriptPath))

	require.Len(t, fake.Commands, 1)
	command := fake.Commands[0]
	require.Equal(t, "sh", command.Name)
	require.Equal(t, []string{k3sInstallScriptPath, "agent"}, command.Args,
		"the explicit agent argument selects the role; env selection would persist the token")
	require.Contains(t, command.Env, "INSTALL_K3S_VERSION="+K3sVersion)
	for _, env := range command.Env {
		require.False(t, strings.HasPrefix(env, "K3S_"),
			"the install script copies every K3S_* variable into the systemd env file, got %s", env)
	}
	require.NotContains(t, string(fake.FS[K3sConfigPath]), "embedded-registry")
}

func TestInstallK3sJoiningServerInvocation(t *testing.T) {
	t.Parallel()
	fake := &host.Fake{Handlers: map[string]func(host.Command) (host.Result, error){
		"sh": func(host.Command) (host.Result, error) { return host.Result{}, nil },
	}}
	err := installK3s(context.Background(), fake, k3sNode{
		Name: "cp-2", Cluster: "production",
		Role:         layout.RoleServer,
		Capabilities: []string{layout.CapabilityApplication},
		ServerURL:    "https://cp-1.internal:6443",
		Token:        "K10abc::server:secret",
	}, silentProgress{})
	require.NoError(t, err)

	// A joining server writes the token file like an agent but runs the
	// script with no role argument (the server role is the default).
	require.Equal(t, []byte("K10abc::server:secret\n"), fake.FS[K3sTokenPath])
	require.Len(t, fake.Commands, 1)
	require.Equal(t, []string{k3sInstallScriptPath}, fake.Commands[0].Args)
}

func TestInstallK3sScriptFailure(t *testing.T) {
	t.Parallel()
	fake := &host.Fake{Handlers: map[string]func(host.Command) (host.Result, error){
		"sh": func(host.Command) (host.Result, error) {
			return host.Result{ExitCode: 1, Stderr: "curl: (6) could not resolve host\n"}, nil
		},
	}}
	err := installK3s(context.Background(), fake, k3sNode{
		Name: "cp-1", Cluster: "production",
		Capabilities: []string{layout.CapabilityApplication},
	}, silentProgress{})
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

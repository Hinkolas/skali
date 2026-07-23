package installer

import (
	"context"
	"io/fs"
	"strings"
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/Hinkolas/skali/internal/bundle"
	"github.com/Hinkolas/skali/internal/installer/host"
	"github.com/Hinkolas/skali/internal/layout"
	"github.com/Hinkolas/skali/internal/version"
)

func upgradeStatus(role string, k3sCurrent, bundleCurrent bool, bundleVersion string) *Status {
	installed := K3sVersion
	if !k3sCurrent {
		installed = "v1.33.2+k3s1"
	}
	return &Status{
		Host: &Host{
			K3sVersion: installed,
			Record:     &Record{Node: NodeRecord{Role: role}},
		},
		K3sCurrent:    k3sCurrent,
		BundleVersion: bundleVersion,
		BundleCurrent: bundleCurrent,
		Initialized:   bundleVersion != "",
	}
}

func TestPlanUpgrade(t *testing.T) {
	t.Parallel()

	t.Run("both drifted", func(t *testing.T) {
		plan := PlanUpgrade(upgradeStatus(layout.RoleServer, false, false, "1.0.0"), false)
		require.True(t, plan.K3sDrifted)
		require.False(t, plan.K3sDowngrade)
		require.True(t, plan.BundleDrifted)
		require.Equal(t, "v1.33.2+k3s1", plan.K3sFrom)
		require.Equal(t, K3sVersion, plan.K3sTo)
		require.Equal(t, "1.0.0", plan.BundleFrom)
		require.Equal(t, version.Version, plan.BundleTo)
		require.False(t, plan.Nothing(layout.RoleServer))
	})

	t.Run("k3s only", func(t *testing.T) {
		plan := PlanUpgrade(upgradeStatus(layout.RoleServer, false, true, version.Version), false)
		require.True(t, plan.K3sDrifted)
		require.False(t, plan.BundleDrifted)
		require.False(t, plan.Nothing(layout.RoleServer))
	})

	t.Run("bundle only", func(t *testing.T) {
		plan := PlanUpgrade(upgradeStatus(layout.RoleServer, true, false, "1.0.0"), false)
		require.False(t, plan.K3sDrifted)
		require.True(t, plan.BundleDrifted)
		require.False(t, plan.Nothing(layout.RoleServer))
	})

	t.Run("nothing", func(t *testing.T) {
		plan := PlanUpgrade(upgradeStatus(layout.RoleServer, true, true, version.Version), false)
		require.True(t, plan.Nothing(layout.RoleServer))
	})

	t.Run("tar forces the converge at equal versions", func(t *testing.T) {
		plan := PlanUpgrade(upgradeStatus(layout.RoleServer, true, true, version.Version), true)
		require.True(t, plan.ImageForced)
		require.False(t, plan.BundleDrifted)
		require.False(t, plan.Nothing(layout.RoleServer))
	})

	t.Run("missing hash stamp counts as bundle drift", func(t *testing.T) {
		// Equal versions but BundleCurrent false: an interrupted converge
		// cleared the namespace annotation and must be re-run.
		plan := PlanUpgrade(upgradeStatus(layout.RoleServer, true, false, version.Version), false)
		require.True(t, plan.BundleDrifted)
		require.False(t, plan.Nothing(layout.RoleServer))
	})

	t.Run("downgrade is flagged", func(t *testing.T) {
		status := upgradeStatus(layout.RoleServer, false, true, version.Version)
		status.Host.K3sVersion = "v9.99.9+k3s9"
		plan := PlanUpgrade(status, false)
		require.True(t, plan.K3sDowngrade)
	})

	t.Run("agent counts only k3s", func(t *testing.T) {
		plan := PlanUpgrade(upgradeStatus(layout.RoleAgent, true, false, ""), false)
		require.True(t, plan.Nothing(layout.RoleAgent))
		require.False(t, plan.BundleDrifted, "agents run no bundle")

		drifted := PlanUpgrade(upgradeStatus(layout.RoleAgent, false, false, ""), false)
		require.False(t, drifted.Nothing(layout.RoleAgent))
	})
}

func TestCompareK3sVersions(t *testing.T) {
	t.Parallel()
	cases := []struct {
		a, b string
		want int
		ok   bool
	}{
		{"v1.33.3+k3s1", "v1.33.3+k3s1", 0, true},
		{"v1.33.2+k3s1", "v1.33.3+k3s1", -1, true},
		{"v1.33.3+k3s1", "v1.33.2+k3s1", 1, true},
		{"v1.33.3+k3s1", "v1.33.3+k3s2", -1, true},
		{"v1.32.9+k3s1", "v1.33.0+k3s1", -1, true},
		{"v2.0.0+k3s1", "v1.99.99+k3s9", 1, true},
		{"", "v1.33.3+k3s1", 0, false},
		{"v1.33.3", "v1.33.3+k3s1", 0, false},
		{"v1.33+k3s1", "v1.33.3+k3s1", 0, false},
		{"garbage", "v1.33.3+k3s1", 0, false},
	}
	for _, c := range cases {
		got, ok := compareK3sVersions(c.a, c.b)
		require.Equal(t, c.ok, ok, "%s vs %s", c.a, c.b)
		require.Equal(t, c.want, got, "%s vs %s", c.a, c.b)
	}
}

// upgradeFake scripts a host whose k3s comes back healthy right after the
// script ran, so the wait settles on its first probe.
func upgradeFake() *host.Fake {
	return &host.Fake{Handlers: map[string]func(host.Command) (host.Result, error){
		"sh": func(host.Command) (host.Result, error) { return host.Result{}, nil },
		"k3s": func(cmd host.Command) (host.Result, error) {
			if len(cmd.Args) > 0 && cmd.Args[0] == "--version" {
				return host.Result{Stdout: "k3s version " + K3sVersion + " (abcdef)\n"}, nil
			}
			return host.Result{}, nil
		},
		"systemctl": func(host.Command) (host.Result, error) {
			return host.Result{Stdout: "active\n"}, nil
		},
	}}
}

func TestUpgradeK3sInvocation(t *testing.T) {
	t.Parallel()
	fake := upgradeFake()
	record := &Record{Node: NodeRecord{Name: "cp-1", Role: layout.RoleServer}}
	require.NoError(t, UpgradeK3s(context.Background(), fake, record, silentProgress{}))

	// The vendored script was re-staged from this binary and is the only
	// write: config.yaml, registries.yaml, and the token survive untouched.
	require.NotEmpty(t, fake.FS[k3sInstallScriptPath])
	require.NotContains(t, fake.FS, K3sConfigPath)
	require.NotContains(t, fake.FS, K3sRegistriesPath)
	require.NotContains(t, fake.FS, K3sTokenPath)

	script := fake.Commands[0]
	require.Equal(t, "sh", script.Name)
	require.Equal(t, []string{k3sInstallScriptPath}, script.Args)
	require.Contains(t, script.Env, "INSTALL_K3S_VERSION="+K3sVersion)
	for _, env := range script.Env {
		require.False(t, strings.HasPrefix(env, "K3S_"), "no K3S_* env may reach the script, got %s", env)
	}

	// The server wait gates on the unit and the kube API before the record
	// version moves; nothing is persisted here.
	requireCommand(t, fake, "systemctl", "is-active", "k3s.service")
	requireCommand(t, fake, "k3s", "kubectl", "get", "--raw", "/readyz")
	require.Equal(t, K3sVersion, record.Versions.K3s)
	require.NotContains(t, fake.FS, RecordPath)
}

func TestUpgradeK3sAgentInvocation(t *testing.T) {
	t.Parallel()
	fake := upgradeFake()
	record := &Record{
		Version: RecordVersion, InstallationID: "test-id",
		Node: NodeRecord{Name: "db-1", Role: layout.RoleAgent},
	}
	require.NoError(t, UpgradeNode(context.Background(), fake, record, silentProgress{}))

	script := fake.Commands[0]
	require.Equal(t, "sh", script.Name)
	require.Equal(t, []string{k3sInstallScriptPath, "agent"}, script.Args,
		"the explicit agent argument selects the role; env selection would persist the token")

	requireCommand(t, fake, "systemctl", "is-active", "k3s-agent.service")
	for _, cmd := range fake.Commands {
		require.NotEqual(t, "kubectl", firstArg(cmd), "agents have no kube API to gate on")
	}

	// The agent record is host-local, so UpgradeNode persists it itself.
	require.Equal(t, K3sVersion, record.Versions.K3s)
	saved := string(fake.FS[RecordPath])
	require.Contains(t, saved, "k3s: "+K3sVersion)
	require.Contains(t, saved, "installer: "+version.Version)
}

func TestUpgradeK3sScriptFailure(t *testing.T) {
	t.Parallel()
	fake := upgradeFake()
	fake.Handlers["sh"] = func(host.Command) (host.Result, error) {
		return host.Result{ExitCode: 1, Stderr: "curl: (6) could not resolve host\n"}, nil
	}
	record := &Record{Node: NodeRecord{Name: "cp-1", Role: layout.RoleServer}}
	err := UpgradeK3s(context.Background(), fake, record, silentProgress{})
	require.ErrorContains(t, err, "exit code 1")
	require.ErrorContains(t, err, "could not resolve host")
	require.Empty(t, record.Versions.K3s, "a failed upgrade must not move the recorded version")
}

func requireCommand(t *testing.T, fake *host.Fake, name string, args ...string) {
	t.Helper()
	for _, cmd := range fake.Commands {
		if cmd.Name != name || len(cmd.Args) != len(args) {
			continue
		}
		match := true
		for i := range args {
			if cmd.Args[i] != args[i] {
				match = false
				break
			}
		}
		if match {
			return
		}
	}
	t.Fatalf("no %s %s command was run; got %v", name, strings.Join(args, " "), fake.Commands)
}

func firstArg(cmd host.Command) string {
	if len(cmd.Args) == 0 {
		return ""
	}
	return cmd.Args[0]
}

func TestNodePullCredentialMissing(t *testing.T) {
	t.Parallel()
	ctx := context.Background()

	// A pre-token-auth host: the mirror exists, no configs section.
	old := &host.Fake{FS: map[string][]byte{
		K3sRegistriesPath: []byte(k3sRegistriesYAML("")),
	}}
	missing, err := NodePullCredentialMissing(ctx, old)
	require.NoError(t, err)
	require.True(t, missing)

	current := &host.Fake{FS: map[string][]byte{
		K3sRegistriesPath: []byte(k3sRegistriesYAML("cluster-pull-secret")),
	}}
	missing, err = NodePullCredentialMissing(ctx, current)
	require.NoError(t, err)
	require.False(t, missing)

	noMirror := &host.Fake{FS: map[string][]byte{
		K3sRegistriesPath: []byte("mirrors:\n  \"*\":\n"),
	}}
	_, err = NodePullCredentialMissing(ctx, noMirror)
	require.ErrorContains(t, err, "mirror")

	_, err = NodePullCredentialMissing(ctx, &host.Fake{})
	require.ErrorContains(t, err, K3sRegistriesPath)
}

func TestHealNodePullCredentialWriteOnly(t *testing.T) {
	t.Parallel()
	fake := &host.Fake{FS: map[string][]byte{
		K3sRegistriesPath: []byte(k3sRegistriesYAML("")),
	}}
	// restart=false: a k3s upgrade follows and restarts the service.
	require.NoError(t, HealNodePullCredential(context.Background(), fake, false, silentProgress{}))
	require.Empty(t, fake.Commands, "the write-only heal must run no commands")
	require.Equal(t, fs.FileMode(0o600), fake.Modes[K3sRegistriesPath])
	secret := registriesPullSecret(fake.FS[K3sRegistriesPath])
	require.NotEmpty(t, secret, "the rewritten registries.yaml must carry the minted credential")
	require.Contains(t, string(fake.FS[K3sRegistriesPath]), bundle.RegistryInternalHost,
		"the mirror must survive the rewrite")
}

func TestHealNodePullCredentialRestarts(t *testing.T) {
	t.Parallel()
	fake := upgradeFake()
	fake.FS = map[string][]byte{K3sRegistriesPath: []byte(k3sRegistriesYAML(""))}
	require.NoError(t, HealNodePullCredential(context.Background(), fake, true, silentProgress{}))
	requireCommand(t, fake, "systemctl", "restart", "k3s")
	// The wait gates on the unit and the kube API, like any k3s restart.
	requireCommand(t, fake, "systemctl", "is-active", "k3s.service")
	requireCommand(t, fake, "k3s", "kubectl", "get", "--raw", "/readyz")
	require.NotEmpty(t, registriesPullSecret(fake.FS[K3sRegistriesPath]))
}

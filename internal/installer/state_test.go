package installer

import (
	"context"
	"strings"
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/Hinkolas/skali/internal/installer/host"
	"github.com/Hinkolas/skali/internal/localdev"
)

// The installer and the local dev harness must pin the same k3s release.
func TestK3sVersionAgreesWithLocaldev(t *testing.T) {
	t.Parallel()
	expected := "rancher/k3s:" + strings.ReplaceAll(K3sVersion, "+", "-")
	require.Equal(t, expected, localdev.K3sImage,
		"the installer k3s pin and the localdev k3d image must move together")
}

// linuxHost fakes a supported root host; mutate it per case.
func linuxHost() *host.Fake {
	fake := &host.Fake{
		FS: map[string][]byte{
			"/etc/os-release":     []byte("NAME=\"Ubuntu\"\nPRETTY_NAME=\"Ubuntu 24.04.2 LTS\"\n"),
			"/run/systemd/system": nil,
		},
		Handlers: map[string]func(host.Command) (host.Result, error){
			"hostname": func(host.Command) (host.Result, error) { return host.Result{Stdout: "cp-1\n"}, nil },
			"uname": func(cmd host.Command) (host.Result, error) {
				if len(cmd.Args) > 0 && cmd.Args[0] == "-s" {
					return host.Result{Stdout: "Linux\n"}, nil
				}
				return host.Result{Stdout: "aarch64\n"}, nil
			},
			"id": func(host.Command) (host.Result, error) { return host.Result{Stdout: "0\n"}, nil },
			"systemctl": func(host.Command) (host.Result, error) {
				return host.Result{ExitCode: 4, Stdout: "not-found\n"}, nil
			},
		},
	}
	return fake
}

func withK3s(fake *host.Fake, unit string, active bool) *host.Fake {
	fake.FS["/usr/local/bin/k3s"] = []byte("binary")
	fake.Handlers["k3s"] = func(host.Command) (host.Result, error) {
		return host.Result{Stdout: "k3s version v1.33.3+k3s1 (0000)\ngo version go1.24\n"}, nil
	}
	fake.Handlers["systemctl"] = func(cmd host.Command) (host.Result, error) {
		if len(cmd.Args) != 2 || cmd.Args[1] != unit {
			return host.Result{ExitCode: 4, Stdout: "not-found\n"}, nil
		}
		switch cmd.Args[0] {
		case "is-enabled":
			return host.Result{Stdout: "enabled\n"}, nil
		case "is-active":
			if active {
				return host.Result{Stdout: "active\n"}, nil
			}
			return host.Result{ExitCode: 3, Stdout: "inactive\n"}, nil
		}
		return host.Result{ExitCode: 1}, nil
	}
	return fake
}

func withRecord(t *testing.T, fake *host.Fake, role string) *host.Fake {
	t.Helper()
	record := &Record{
		Version:        RecordVersion,
		InstallationID: "0f0f0f0f",
		Provider:       ProviderK3s,
		Cluster:        "production",
		Ownership:      OwnershipManaged,
		Node:           NodeRecord{Name: "cp-1", Role: role, Capabilities: []string{"application", "edge"}},
		Versions:       Versions{Installer: "test", K3s: K3sVersion},
	}
	require.NoError(t, SaveRecord(context.Background(), fake, record))
	fake.Writes = nil
	return fake
}

func TestDetectStates(t *testing.T) {
	t.Parallel()
	ctx := context.Background()

	cases := []struct {
		name     string
		fake     *host.Fake
		state    HostState
		problems bool
	}{
		{name: "fresh", fake: linuxHost(), state: StateFresh},
		{name: "unmanaged", fake: withK3s(linuxHost(), "k3s.service", true), state: StateUnmanaged},
		{name: "server", fake: withRecord(t, withK3s(linuxHost(), "k3s.service", true), "server"), state: StateServer},
		{name: "server inactive is still server", fake: withRecord(t, withK3s(linuxHost(), "k3s.service", false), "server"), state: StateServer},
		{name: "agent", fake: withRecord(t, withK3s(linuxHost(), "k3s-agent.service", true), "agent"), state: StateAgent},
		{name: "damaged corrupt record", fake: func() *host.Fake {
			fake := withK3s(linuxHost(), "k3s.service", true)
			fake.FS[RecordPath] = []byte(":not yaml:\n\t")
			return fake
		}(), state: StateDamaged, problems: true},
		{name: "damaged record without k3s", fake: withRecord(t, linuxHost(), "server"), state: StateDamaged, problems: true},
		{name: "damaged role unit mismatch", fake: withRecord(t, withK3s(linuxHost(), "k3s-agent.service", true), "server"), state: StateDamaged, problems: true},
		{name: "unsupported not root", fake: func() *host.Fake {
			fake := linuxHost()
			fake.Handlers["id"] = func(host.Command) (host.Result, error) {
				return host.Result{Stdout: "501\n"}, nil
			}
			return fake
		}(), state: StateUnsupported, problems: true},
		{name: "unsupported no systemd", fake: func() *host.Fake {
			fake := linuxHost()
			delete(fake.FS, "/run/systemd/system")
			return fake
		}(), state: StateUnsupported, problems: true},
		{name: "unsupported not linux", fake: func() *host.Fake {
			fake := linuxHost()
			fake.Handlers["uname"] = func(cmd host.Command) (host.Result, error) {
				if len(cmd.Args) > 0 && cmd.Args[0] == "-s" {
					return host.Result{Stdout: "Darwin\n"}, nil
				}
				return host.Result{Stdout: "arm64\n"}, nil
			}
			return fake
		}(), state: StateUnsupported, problems: true},
	}
	for _, testCase := range cases {
		t.Run(testCase.name, func(t *testing.T) {
			t.Parallel()
			detected, err := Detect(ctx, testCase.fake)
			require.NoError(t, err)
			require.Equal(t, testCase.state, detected.State)
			if testCase.problems {
				require.NotEmpty(t, detected.Problems)
			} else {
				require.Empty(t, detected.Problems)
			}
			// Detection is pure read.
			require.Empty(t, testCase.fake.Writes, "detection must never write")
		})
	}
}

func TestDetectGathersFacts(t *testing.T) {
	t.Parallel()
	fake := withRecord(t, withK3s(linuxHost(), "k3s.service", true), "server")
	detected, err := Detect(context.Background(), fake)
	require.NoError(t, err)
	require.Equal(t, "cp-1", detected.Hostname)
	require.Equal(t, "Ubuntu 24.04.2 LTS", detected.OS)
	require.Equal(t, "aarch64", detected.Arch)
	require.Equal(t, "v1.33.3+k3s1", detected.K3sVersion)
	require.True(t, detected.K3sActive)
	require.NotNil(t, detected.Record)
	require.Equal(t, "production", detected.Record.Cluster)
}

func TestDetectOrphanedSkaliInstall(t *testing.T) {
	t.Parallel()
	fake := withK3s(linuxHost(), "k3s.service", false)
	fake.FS[K3sConfigPath] = []byte(k3sConfigYAML(k3sNode{
		Name:         "cp-2",
		Cluster:      "e2e",
		Role:         "server",
		Capabilities: []string{"application", "edge"},
		ServerURL:    "https://10.1.0.4:6443",
	}))
	fake.FS[K3sRegistriesPath] = []byte(k3sRegistriesYAML("pull"))

	detected, err := Detect(context.Background(), fake)
	require.NoError(t, err)
	require.Equal(t, StateOrphaned, detected.State)
	require.Equal(t, "e2e", detected.Record.Cluster)
	require.Equal(t, "https://10.1.0.4:6443", detected.Record.Join.Server)
	require.Empty(t, detected.Record.InstallationID)

	require.NoError(t, PersistOrphanRecord(context.Background(), fake, detected.Record))
	detected, err = Detect(context.Background(), fake)
	require.NoError(t, err)
	require.Equal(t, StateInterrupted, detected.State)
	require.NotEmpty(t, detected.Record.InstallationID)
	require.True(t, detected.Record.Lifecycle.StartAttempted)
}

func TestDetectDoesNotAdoptPartialFingerprint(t *testing.T) {
	t.Parallel()
	fake := withK3s(linuxHost(), "k3s.service", true)
	fake.FS[K3sConfigPath] = []byte("node-label:\n  - skali.dev/cluster=e2e\n  - skali.dev/capability-edge=true\n")
	// Missing the independent Skali registry fingerprint.
	detected, err := Detect(context.Background(), fake)
	require.NoError(t, err)
	require.Equal(t, StateUnmanaged, detected.State)
	require.Nil(t, detected.Record)
}

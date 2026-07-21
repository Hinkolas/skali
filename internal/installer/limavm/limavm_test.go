package limavm

import (
	"context"
	"fmt"
	"os"
	"strconv"
	"strings"
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/Hinkolas/skali/internal/installer/host"
)

func TestRenderTemplate(t *testing.T) {
	t.Parallel()
	spec := Spec{
		Instance: "skali",
		Network:  NetworkBridged,
		CPUs:     9,
		Memory:   "12GiB",
		Disk:     "100GiB",
		Hostname: "mini-01",
	}
	rendered, err := renderTemplate(spec)
	require.NoError(t, err)
	text := string(rendered)
	require.Contains(t, text, "cpus: 9")
	require.Contains(t, text, `memory: "12GiB"`)
	require.Contains(t, text, `disk: "100GiB"`)
	require.Contains(t, text, "- lima: bridged")
	require.Contains(t, text, "hostnamectl set-hostname mini-01")
	require.Contains(t, text, "mounts: []")
	// The API forward exists only on user-v2.
	require.NotContains(t, text, "portForwards")

	spec.Network = NetworkUserV2
	rendered, err = renderTemplate(spec)
	require.NoError(t, err)
	text = string(rendered)
	require.Contains(t, text, "- lima: user-v2")
	require.Contains(t, text, "guestPort: 6443")
	require.Contains(t, text, fmt.Sprintf("hostPort: %d", KubeForwardPort))
}

func TestDefaultSpec(t *testing.T) {
	t.Parallel()
	fake := &host.Fake{Handlers: map[string]func(host.Command) (host.Result, error){
		"sysctl": func(cmd host.Command) (host.Result, error) {
			switch cmd.Args[1] {
			case "hw.ncpu":
				return host.Result{Stdout: "10\n"}, nil
			case "hw.memsize":
				return host.Result{Stdout: "17179869184\n"}, nil
			}
			return host.Result{ExitCode: 1}, nil
		},
	}}
	spec := DefaultSpec(context.Background(), fake, "Nicks-Mac-mini.local")
	require.Equal(t, Spec{
		Instance: "skali",
		Network:  NetworkBridged,
		CPUs:     9,
		Memory:   "12GiB",
		Disk:     "100GiB",
		Hostname: "nicks-mac-mini",
	}, spec)
}

func TestDefaultSpecProbeFailure(t *testing.T) {
	t.Parallel()
	fake := &host.Fake{Handlers: map[string]func(host.Command) (host.Result, error){
		"sysctl": func(host.Command) (host.Result, error) {
			return host.Result{ExitCode: 1}, nil
		},
	}}
	spec := DefaultSpec(context.Background(), fake, "")
	require.Equal(t, 2, spec.CPUs)
	require.Equal(t, "4GiB", spec.Memory)
	require.Equal(t, "skali-node", spec.Hostname)
}

func TestGuestHostname(t *testing.T) {
	t.Parallel()
	cases := map[string]string{
		"Nicks-Mac-mini.local":  "nicks-mac-mini",
		"studio":                "studio",
		"Nick's Mac":            "nick-s-mac",
		"__weird__":             "weird",
		"":                      "skali-node",
		strings.Repeat("a", 80): strings.Repeat("a", 63),
	}
	for input, want := range cases {
		require.Equal(t, want, GuestHostname(input), "input %q", input)
	}
}

func TestInspect(t *testing.T) {
	t.Parallel()
	fake := &host.Fake{Handlers: map[string]func(host.Command) (host.Result, error){
		"limactl": func(cmd host.Command) (host.Result, error) {
			require.Equal(t, []string{"list", "--format", "json", "skali"}, cmd.Args)
			return host.Result{Stdout: `{"name":"skali","status":"Running","network":[{"lima":"bridged"}]}` + "\n"}, nil
		},
	}}
	info, err := Inspect(context.Background(), fake, "skali")
	require.NoError(t, err)
	require.Equal(t, Info{Exists: true, Running: true, Network: NetworkBridged}, info)
}

func TestInspectAbsent(t *testing.T) {
	t.Parallel()
	fake := &host.Fake{Handlers: map[string]func(host.Command) (host.Result, error){
		"limactl": func(host.Command) (host.Result, error) {
			return host.Result{ExitCode: 1, Stderr: `level=warning msg="No instance matching skali found."`}, nil
		},
	}}
	info, err := Inspect(context.Background(), fake, "skali")
	require.NoError(t, err)
	require.False(t, info.Exists)
}

func TestInspectStopped(t *testing.T) {
	t.Parallel()
	fake := &host.Fake{Handlers: map[string]func(host.Command) (host.Result, error){
		"limactl": func(host.Command) (host.Result, error) {
			return host.Result{Stdout: `{"name":"skali","status":"Stopped","config":{"networks":[{"lima":"user-v2"}]}}`}, nil
		},
	}}
	info, err := Inspect(context.Background(), fake, "skali")
	require.NoError(t, err)
	require.Equal(t, Info{Exists: true, Running: false, Network: NetworkUserV2}, info)
}

func TestCreateInvokesLimactl(t *testing.T) {
	t.Parallel()
	fake := &host.Fake{Handlers: map[string]func(host.Command) (host.Result, error){
		"limactl": func(cmd host.Command) (host.Result, error) {
			require.Equal(t, "start", cmd.Args[0])
			require.Equal(t, []string{"--name", "skali-test", "--tty=false"}, cmd.Args[1:4])
			template, err := os.ReadFile(cmd.Args[4])
			require.NoError(t, err)
			require.Contains(t, string(template), "hostnamectl set-hostname node-1")
			return host.Result{}, nil
		},
	}}
	err := Create(context.Background(), fake, Spec{
		Instance: "skali-test", Network: NetworkUserV2,
		CPUs: 2, Memory: "3GiB", Disk: "15GiB", Hostname: "node-1",
	})
	require.NoError(t, err)
	require.Len(t, fake.Commands, 1)
}

func TestStopToleratesStopped(t *testing.T) {
	t.Parallel()
	fake := &host.Fake{Handlers: map[string]func(host.Command) (host.Result, error){
		"limactl": func(cmd host.Command) (host.Result, error) {
			if cmd.Args[0] == "stop" {
				return host.Result{ExitCode: 1, Stderr: "already stopped"}, nil
			}
			return host.Result{Stdout: `{"name":"skali","status":"Stopped"}`}, nil
		},
	}}
	require.NoError(t, Stop(context.Background(), fake, "skali"))
}

func TestDeleteToleratesAbsent(t *testing.T) {
	t.Parallel()
	fake := &host.Fake{Handlers: map[string]func(host.Command) (host.Result, error){
		"limactl": func(cmd host.Command) (host.Result, error) {
			if cmd.Args[0] == "delete" {
				return host.Result{ExitCode: 1, Stderr: "not found"}, nil
			}
			return host.Result{ExitCode: 1}, nil
		},
	}}
	require.NoError(t, Delete(context.Background(), fake, "skali"))
}

func preflightFake(t *testing.T, files map[string]string) *host.Fake {
	t.Helper()
	fake := &host.Fake{Handlers: map[string]func(host.Command) (host.Result, error){
		"limactl": func(host.Command) (host.Result, error) { return host.Result{Stdout: "limactl version 2.1.4"}, nil },
	}}
	for path, content := range files {
		require.NoError(t, fake.WriteFile(context.Background(), path, []byte(content), 0o644))
	}
	return fake
}

func TestPreflight(t *testing.T) {
	t.Setenv("HOME", "/testhome")
	configPath := "/testhome/.lima/_config/networks.yaml"
	fullConfig := "paths:\n  socketVMNet: /opt/socket_vmnet/bin/socket_vmnet\n" +
		"  sudoers: /private/etc/sudoers.d/lima\n" +
		"networks:\n  bridged:\n    mode: bridged\n  shared:\n    mode: shared\n"

	t.Run("user-v2 needs nothing", func(t *testing.T) {
		fake := preflightFake(t, nil)
		require.NoError(t, Preflight(context.Background(), fake, NetworkUserV2))
	})

	t.Run("bridged fully configured", func(t *testing.T) {
		fake := preflightFake(t, map[string]string{
			configPath:                           fullConfig,
			"/opt/socket_vmnet/bin/socket_vmnet": "binary",
			"/private/etc/sudoers.d/lima":        "sudoers",
		})
		require.NoError(t, Preflight(context.Background(), fake, NetworkBridged))
	})

	t.Run("missing networks.yaml", func(t *testing.T) {
		fake := preflightFake(t, nil)
		err := Preflight(context.Background(), fake, NetworkBridged)
		require.ErrorContains(t, err, `the Lima network "bridged" is not configured`)
		require.ErrorContains(t, err, "socket_vmnet-1.2.2")
		require.ErrorContains(t, err, "limactl sudoers | sudo tee /etc/sudoers.d/lima")
	})

	t.Run("missing network key", func(t *testing.T) {
		fake := preflightFake(t, map[string]string{
			configPath: "networks:\n  shared:\n    mode: shared\n",
		})
		err := Preflight(context.Background(), fake, NetworkBridged)
		require.ErrorContains(t, err, `the Lima network "bridged" is not configured in `+configPath)
	})

	t.Run("missing socket_vmnet binary", func(t *testing.T) {
		fake := preflightFake(t, map[string]string{
			configPath:                    fullConfig,
			"/private/etc/sudoers.d/lima": "sudoers",
		})
		err := Preflight(context.Background(), fake, NetworkShared)
		require.ErrorContains(t, err, "socket_vmnet is not installed at /opt/socket_vmnet/bin/socket_vmnet")
	})

	t.Run("missing sudoers", func(t *testing.T) {
		fake := preflightFake(t, map[string]string{
			configPath:                           fullConfig,
			"/opt/socket_vmnet/bin/socket_vmnet": "binary",
		})
		err := Preflight(context.Background(), fake, NetworkBridged)
		require.ErrorContains(t, err, "the Lima sudoers file /private/etc/sudoers.d/lima is missing")
	})
}

func TestInstallLaunchAgent(t *testing.T) {
	t.Setenv("HOME", "/testhome")
	original := lookPath
	lookPath = func(string) (string, error) { return "/opt/homebrew/bin/limactl", nil }
	defer func() { lookPath = original }()

	fake := &host.Fake{Handlers: map[string]func(host.Command) (host.Result, error){
		"launchctl": func(cmd host.Command) (host.Result, error) {
			if cmd.Args[0] == "bootout" {
				return host.Result{ExitCode: 3}, nil // nothing loaded yet
			}
			return host.Result{}, nil
		},
	}}
	path, err := InstallLaunchAgent(context.Background(), fake, "skali")
	require.NoError(t, err)
	require.Equal(t, "/testhome/Library/LaunchAgents/dev.skali.lima.plist", path)

	plist, err := fake.ReadFile(context.Background(), path)
	require.NoError(t, err)
	text := string(plist)
	require.Contains(t, text, "<string>dev.skali.lima</string>")
	require.Contains(t, text, "<string>/opt/homebrew/bin/limactl</string>")
	require.Contains(t, text, "<string>start</string>")
	require.Contains(t, text, "<string>skali</string>")
	require.Contains(t, text, "<key>RunAtLoad</key>")
	require.Contains(t, text, "/opt/homebrew/bin:/usr/bin:/bin:/usr/sbin:/sbin")
	require.Contains(t, text, "/testhome/Library/Logs/skali/lima-launchagent.log")

	domain := "gui/" + strconv.Itoa(os.Getuid())
	require.Equal(t, []string{"bootout", domain + "/" + LaunchAgentLabel}, fake.Commands[0].Args)
	require.Equal(t, []string{"bootstrap", domain, path}, fake.Commands[1].Args)
}

func TestInstallLaunchAgentLoadFailure(t *testing.T) {
	t.Setenv("HOME", "/testhome")
	original := lookPath
	lookPath = func(string) (string, error) { return "/opt/homebrew/bin/limactl", nil }
	defer func() { lookPath = original }()

	fake := &host.Fake{Handlers: map[string]func(host.Command) (host.Result, error){
		"launchctl": func(cmd host.Command) (host.Result, error) {
			if cmd.Args[0] == "bootstrap" {
				return host.Result{ExitCode: 5, Stderr: "Bootstrap failed: 5: Input/output error"}, nil
			}
			return host.Result{}, nil
		},
	}}
	path, err := InstallLaunchAgent(context.Background(), fake, "skali")
	require.ErrorIs(t, err, ErrLaunchAgentLoad)
	require.NotEmpty(t, path)
}

func TestRemoveLaunchAgent(t *testing.T) {
	t.Setenv("HOME", "/testhome")
	fake := &host.Fake{
		FS: map[string][]byte{"/testhome/Library/LaunchAgents/dev.skali.lima.plist": []byte("plist")},
		Handlers: map[string]func(host.Command) (host.Result, error){
			"launchctl": func(host.Command) (host.Result, error) { return host.Result{}, nil },
		},
	}
	require.NoError(t, RemoveLaunchAgent(context.Background(), fake))
	require.NotContains(t, fake.Paths(), "/testhome/Library/LaunchAgents/dev.skali.lima.plist")
}

func TestAutoLoginUser(t *testing.T) {
	t.Parallel()
	enabled := &host.Fake{Handlers: map[string]func(host.Command) (host.Result, error){
		"defaults": func(host.Command) (host.Result, error) { return host.Result{Stdout: "nhinke\n"}, nil },
	}}
	require.Equal(t, "nhinke", AutoLoginUser(context.Background(), enabled))

	disabled := &host.Fake{Handlers: map[string]func(host.Command) (host.Result, error){
		"defaults": func(host.Command) (host.Result, error) {
			return host.Result{ExitCode: 1, Stderr: "does not exist"}, nil
		},
	}}
	require.Empty(t, AutoLoginUser(context.Background(), disabled))
}

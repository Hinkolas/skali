package host

import (
	"bytes"
	"context"
	"io"
	"io/fs"
	"strings"
	"testing"

	"github.com/stretchr/testify/require"
)

func limaOn(fake *Fake) Lima {
	return Lima{Instance: "skali", Host: fake}
}

func TestLimaRunWrapsShell(t *testing.T) {
	t.Parallel()
	fake := &Fake{Handlers: map[string]func(Command) (Result, error){
		"limactl": func(Command) (Result, error) { return Result{ExitCode: 7}, nil },
	}}
	result, err := limaOn(fake).Run(context.Background(), Command{
		Name: "systemctl", Args: []string{"is-active", "k3s.service"},
	})
	require.NoError(t, err)
	require.Equal(t, 7, result.ExitCode)
	require.Equal(t, []string{
		"shell", "--workdir", "/", "skali", "--",
		"sudo", "systemctl", "is-active", "k3s.service",
	}, fake.Commands[0].Args)
}

func TestLimaRunEnvRidesEnvPrefix(t *testing.T) {
	t.Parallel()
	fake := &Fake{Handlers: map[string]func(Command) (Result, error){
		"limactl": func(Command) (Result, error) { return Result{}, nil },
	}}
	_, err := limaOn(fake).Run(context.Background(), Command{
		Name: "sh", Args: []string{"/tmp/script"},
		Env: []string{"INSTALL_K3S_VERSION=v1.33.3+k3s1"},
	})
	require.NoError(t, err)
	require.Equal(t, []string{
		"shell", "--workdir", "/", "skali", "--",
		"sudo", "env", "INSTALL_K3S_VERSION=v1.33.3+k3s1", "sh", "/tmp/script",
	}, fake.Commands[0].Args)
}

func TestLimaRunStreamsThrough(t *testing.T) {
	t.Parallel()
	stdin := strings.NewReader("input")
	var stdout bytes.Buffer
	fake := &Fake{Handlers: map[string]func(Command) (Result, error){
		"limactl": func(cmd Command) (Result, error) {
			require.Equal(t, io.Reader(stdin), cmd.Stdin)
			return Result{Stdout: "streamed"}, nil
		},
	}}
	result, err := limaOn(fake).Run(context.Background(), Command{
		Name: "cat", Stdin: stdin, Stdout: &stdout,
	})
	require.NoError(t, err)
	require.Empty(t, result.Stdout)
	require.Equal(t, "streamed", stdout.String())
}

func TestLimaReadFile(t *testing.T) {
	t.Parallel()
	fake := &Fake{Handlers: map[string]func(Command) (Result, error){
		"limactl": func(cmd Command) (Result, error) {
			require.Equal(t, "/var/lib/skali/installation.yaml", cmd.Args[len(cmd.Args)-1])
			require.Contains(t, cmd.Args, `[ -e "$1" ] || exit 44; cat -- "$1"`)
			return Result{Stdout: "raw\x00bytes"}, nil
		},
	}}
	data, err := limaOn(fake).ReadFile(context.Background(), "/var/lib/skali/installation.yaml")
	require.NoError(t, err)
	require.Equal(t, []byte("raw\x00bytes"), data)
}

func TestLimaReadFileAbsent(t *testing.T) {
	t.Parallel()
	fake := &Fake{Handlers: map[string]func(Command) (Result, error){
		"limactl": func(Command) (Result, error) { return Result{ExitCode: 44}, nil },
	}}
	_, err := limaOn(fake).ReadFile(context.Background(), "/missing")
	require.ErrorIs(t, err, fs.ErrNotExist)
}

func TestLimaReadFileFailure(t *testing.T) {
	t.Parallel()
	fake := &Fake{Handlers: map[string]func(Command) (Result, error){
		"limactl": func(Command) (Result, error) {
			return Result{ExitCode: 1, Stderr: "cat: boom"}, nil
		},
	}}
	_, err := limaOn(fake).ReadFile(context.Background(), "/broken")
	require.ErrorContains(t, err, "read /broken in VM skali")
	require.ErrorContains(t, err, "cat: boom")
}

func TestLimaWriteFile(t *testing.T) {
	t.Parallel()
	fake := &Fake{Handlers: map[string]func(Command) (Result, error){
		"limactl": func(cmd Command) (Result, error) {
			payload, err := io.ReadAll(cmd.Stdin)
			require.NoError(t, err)
			require.Equal(t, []byte("token\n"), payload)
			require.Contains(t, cmd.Args, `cat > "$1" && chmod 600 "$1"`)
			require.Equal(t, "/etc/rancher/k3s/token", cmd.Args[len(cmd.Args)-1])
			return Result{}, nil
		},
	}}
	err := limaOn(fake).WriteFile(context.Background(), "/etc/rancher/k3s/token", []byte("token\n"), 0o600)
	require.NoError(t, err)
}

func TestLimaMkdirAllAndRemove(t *testing.T) {
	t.Parallel()
	fake := &Fake{Handlers: map[string]func(Command) (Result, error){
		"limactl": func(Command) (Result, error) { return Result{}, nil },
	}}
	lima := limaOn(fake)
	require.NoError(t, lima.MkdirAll(context.Background(), "/var/lib/skali", 0o750))
	require.Contains(t, fake.Commands[0].Args, `mkdir -p -- "$1" && chmod 750 "$1"`)
	require.Equal(t, "/var/lib/skali", fake.Commands[0].Args[len(fake.Commands[0].Args)-1])

	require.NoError(t, lima.Remove(context.Background(), "/var/lib/skali"))
	require.Equal(t, []string{
		"shell", "--workdir", "/", "skali", "--",
		"sudo", "rm", "-rf", "--", "/var/lib/skali",
	}, fake.Commands[1].Args)
}

func TestLimaStat(t *testing.T) {
	t.Parallel()
	cases := []struct {
		name   string
		result Result
		want   Info
	}{
		{"directory", Result{Stdout: "41ed 4096\n"}, Info{Exists: true, Mode: 0o755 | fs.ModeDir, Size: 4096}},
		{"regular file", Result{Stdout: "81a4 12\n"}, Info{Exists: true, Mode: 0o644, Size: 12}},
		{"absent", Result{ExitCode: 44}, Info{}},
	}
	for _, testCase := range cases {
		t.Run(testCase.name, func(t *testing.T) {
			t.Parallel()
			fake := &Fake{Handlers: map[string]func(Command) (Result, error){
				"limactl": func(Command) (Result, error) { return testCase.result, nil },
			}}
			info, err := limaOn(fake).Stat(context.Background(), "/probe")
			require.NoError(t, err)
			require.Equal(t, testCase.want, info)
		})
	}
}

func TestLimaAPIAddress(t *testing.T) {
	t.Parallel()
	cases := []struct {
		name    string
		listVM  string
		ipProbe string
		want    string
	}{
		{
			name: "user-v2 with explicit forward",
			listVM: `{"name":"skali","status":"Running","network":[{"lima":"user-v2"}],` +
				`"config":{"portForwards":[{"guestPort":6443,"hostPort":16443}]}}`,
			want: "127.0.0.1:16443",
		},
		{
			name:   "user-v2 without forward",
			listVM: `{"name":"skali","status":"Running","network":[{"lima":"user-v2"}],"config":{}}`,
			want:   "127.0.0.1:6443",
		},
		{
			name:    "bridged uses the guest address",
			listVM:  `{"name":"skali","status":"Running","network":[{"lima":"bridged"}],"config":{}}`,
			ipProbe: "2: lima0    inet 192.168.1.87/24 brd 192.168.1.255 scope global lima0",
			want:    "192.168.1.87:6443",
		},
	}
	for _, testCase := range cases {
		t.Run(testCase.name, func(t *testing.T) {
			t.Parallel()
			fake := &Fake{Handlers: map[string]func(Command) (Result, error){
				"limactl": func(cmd Command) (Result, error) {
					if cmd.Args[0] == "list" {
						require.Equal(t, []string{"list", "--format", "json", "skali"}, cmd.Args)
						return Result{Stdout: testCase.listVM + "\n"}, nil
					}
					require.Contains(t, cmd.Args, "lima0")
					return Result{Stdout: testCase.ipProbe + "\n"}, nil
				},
			}}
			address, err := limaOn(fake).APIAddress(context.Background())
			require.NoError(t, err)
			require.Equal(t, testCase.want, address)
		})
	}
}

func TestLimaAPIAddressNoAddress(t *testing.T) {
	t.Parallel()
	fake := &Fake{Handlers: map[string]func(Command) (Result, error){
		"limactl": func(cmd Command) (Result, error) {
			if cmd.Args[0] == "list" {
				return Result{Stdout: `{"name":"skali","status":"Running","network":[{"lima":"shared"}],"config":{}}`}, nil
			}
			return Result{ExitCode: 1, Stderr: `Device "lima0" does not exist.`}, nil
		},
	}}
	_, err := limaOn(fake).APIAddress(context.Background())
	require.ErrorContains(t, err, "no IPv4 address on interface lima0 in VM skali")
}

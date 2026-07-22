package limavm

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"io"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/Hinkolas/skali/internal/installer/host"
)

func withLimactl(t *testing.T, path string, err error) {
	t.Helper()
	original := lookPath
	lookPath = func(string) (string, error) { return path, err }
	t.Cleanup(func() { lookPath = original })
}

func withDownload(t *testing.T, body string) {
	t.Helper()
	originalGet := httpGet
	httpGet = func(context.Context, string) (io.ReadCloser, error) {
		return io.NopCloser(strings.NewReader(body)), nil
	}
	sum := sha256.Sum256([]byte(body))
	digest := hex.EncodeToString(sum[:])
	arch := machineArch()
	originalLima, originalVMNet := limaSHA256[arch], socketVMNetSHA256[arch]
	limaSHA256[arch], socketVMNetSHA256[arch] = digest, digest
	t.Cleanup(func() {
		httpGet = originalGet
		limaSHA256[arch], socketVMNetSHA256[arch] = originalLima, originalVMNet
	})
}

func networksPath(t *testing.T) string {
	t.Helper()
	home, err := os.UserHomeDir()
	require.NoError(t, err)
	return filepath.Join(home, ".lima", "_config", "networks.yaml")
}

func TestAssessUserV2NeedsOnlyLima(t *testing.T) {
	withLimactl(t, "/opt/homebrew/bin/limactl", nil)
	state, err := Assess(context.Background(), &host.Fake{}, NetworkUserV2)
	require.NoError(t, err)
	require.False(t, state.NeedsVMNet)
	require.False(t, state.Missing())

	withLimactl(t, "", errors.New("not found"))
	state, err = Assess(context.Background(), &host.Fake{}, NetworkUserV2)
	require.NoError(t, err)
	require.True(t, state.Missing(), "a missing limactl is a gap even on user-v2")
}

func TestAssessFreshMac(t *testing.T) {
	withLimactl(t, "", errors.New("not found"))
	state, err := Assess(context.Background(), &host.Fake{}, NetworkBridged)
	require.NoError(t, err)
	require.True(t, state.NeedsVMNet)
	require.Empty(t, state.LimactlPath)
	require.False(t, state.NetworksFileExists)
	require.False(t, state.SocketVMNetPresent)
	require.False(t, state.SudoersPresent)
	require.True(t, state.Missing())
	require.Equal(t, defaultSocketVMNetPath, state.SocketVMNetPath)
	require.Equal(t, defaultSudoersPath, state.SudoersPath)
}

func TestAssessExistingNetworksFileWithoutKey(t *testing.T) {
	withLimactl(t, "/opt/homebrew/bin/limactl", nil)
	fake := &host.Fake{FS: map[string][]byte{
		networksPath(t): []byte("networks:\n  shared:\n    mode: shared\n"),
	}}
	state, err := Assess(context.Background(), fake, NetworkBridged)
	require.NoError(t, err)
	require.True(t, state.NetworksFileExists)
	require.False(t, state.NetworkConfigured)
}

func TestAssessCompleteVMNetSetup(t *testing.T) {
	withLimactl(t, "/opt/homebrew/bin/limactl", nil)
	fake := &host.Fake{FS: map[string][]byte{
		networksPath(t): []byte("paths:\n  socketVMNet: /custom/socket_vmnet\n" +
			"networks:\n  bridged:\n    mode: bridged\n    interface: en0\n"),
		"/custom/socket_vmnet":        []byte("binary"),
		"/private/etc/sudoers.d/lima": []byte("rules"),
	}}
	state, err := Assess(context.Background(), fake, NetworkBridged)
	require.NoError(t, err)
	require.True(t, state.NetworkConfigured)
	require.Equal(t, "/custom/socket_vmnet", state.SocketVMNetPath)
	require.True(t, state.SocketVMNetPresent)
	require.True(t, state.SudoersPresent)
	require.False(t, state.Missing())
}

func TestDownloadChecksumMismatch(t *testing.T) {
	originalGet := httpGet
	httpGet = func(context.Context, string) (io.ReadCloser, error) {
		return io.NopCloser(strings.NewReader("tampered")), nil
	}
	t.Cleanup(func() { httpGet = originalGet })

	_, err := download(context.Background(), "https://example.test/asset.tar.gz", limaSHA256[machineArch()])
	require.ErrorContains(t, err, "checksum mismatch")
}

func TestInstallLimaExtractsIntoManagedPrefix(t *testing.T) {
	withDownload(t, "fake-lima-tarball")
	fake := &host.Fake{Handlers: map[string]func(host.Command) (host.Result, error){
		"tar": func(cmd host.Command) (host.Result, error) {
			require.Equal(t, "-xzf", cmd.Args[0])
			require.Equal(t, "-C", cmd.Args[2])
			return host.Result{}, nil
		},
	}}
	limactl, err := InstallLima(context.Background(), fake)
	require.NoError(t, err)
	prefix, err := ManagedLimaPrefix()
	require.NoError(t, err)
	require.Equal(t, filepath.Join(prefix, "bin", "limactl"), limactl)
	require.Len(t, fake.Commands, 1)
	require.Equal(t, prefix, fake.Commands[0].Args[3], "extraction must target the managed prefix")
}

func TestInstallSocketVMNetInvocation(t *testing.T) {
	withDownload(t, "fake-socket-vmnet-tarball")
	fake := &host.Fake{Handlers: map[string]func(host.Command) (host.Result, error){
		"sudo": func(cmd host.Command) (host.Result, error) {
			require.Equal(t, "tar", cmd.Args[0])
			require.Equal(t, "Cxzf", cmd.Args[1])
			require.Equal(t, "/", cmd.Args[2])
			require.Equal(t, "opt/socket_vmnet", cmd.Args[4])
			return host.Result{}, nil
		},
	}}
	require.NoError(t, InstallSocketVMNet(context.Background(), fake))
	require.Len(t, fake.Commands, 1)
}

func TestWriteSudoersInvocation(t *testing.T) {
	fake := &host.Fake{Handlers: map[string]func(host.Command) (host.Result, error){
		"sh": func(cmd host.Command) (host.Result, error) {
			require.Contains(t, cmd.Args[1], "limactl sudoers | sudo tee /etc/sudoers.d/lima")
			return host.Result{}, nil
		},
	}}
	require.NoError(t, WriteSudoers(context.Background(), fake))
	require.Len(t, fake.Commands, 1)
}

func TestWriteDefaultNetworksWritesOnceOnly(t *testing.T) {
	fake := &host.Fake{}
	require.NoError(t, WriteDefaultNetworks(context.Background(), fake, "en5"))
	path := networksPath(t)
	data, err := fake.ReadFile(context.Background(), path)
	require.NoError(t, err)
	require.Contains(t, string(data), "interface: en5")
	require.Contains(t, string(data), "mode: shared")

	// A second run must not touch the file, whatever it now contains.
	fake.FS[path] = []byte("operator: edited\n")
	writes := len(fake.Writes)
	require.NoError(t, WriteDefaultNetworks(context.Background(), fake, "en0"))
	require.Equal(t, writes, len(fake.Writes), "an existing networks.yaml is never edited")
	require.Equal(t, "operator: edited\n", string(fake.FS[path]))
}

func TestDetectDefaultInterface(t *testing.T) {
	fake := &host.Fake{Handlers: map[string]func(host.Command) (host.Result, error){
		"route": func(cmd host.Command) (host.Result, error) {
			return host.Result{Stdout: "   route to: default\n   gateway: 192.168.1.1\n  interface: en7\n"}, nil
		},
	}}
	require.Equal(t, "en7", DetectDefaultInterface(context.Background(), fake))

	failing := &host.Fake{Handlers: map[string]func(host.Command) (host.Result, error){
		"route": func(cmd host.Command) (host.Result, error) {
			return host.Result{ExitCode: 1}, nil
		},
	}}
	require.Equal(t, "en0", DetectDefaultInterface(context.Background(), failing))
}

func TestPreflightNeverEditsOperatorNetworks(t *testing.T) {
	withLimactl(t, "/opt/homebrew/bin/limactl", nil)
	fake := &host.Fake{FS: map[string][]byte{
		networksPath(t): []byte("networks:\n  shared:\n    mode: shared\n"),
	}}
	err := Preflight(context.Background(), fake, NetworkBridged)
	require.ErrorContains(t, err, "never edited")
}

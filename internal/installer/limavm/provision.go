package limavm

// Mac dependency provisioning: Lima itself into a rootless managed prefix,
// the default networks.yaml, socket_vmnet, and the Lima sudoers file. The
// mechanics live here; the confirmation UX (one printed plan, one
// confirmation, one privileged step) is the command layer's. Every
// function is individually idempotent so an interrupted run resumes at
// the first missing piece.

import (
	"cmp"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"net/http"
	"os"
	"path/filepath"
	"runtime"
	"strings"

	"gopkg.in/yaml.v3"

	"github.com/Hinkolas/skali/internal/installer/host"
)

// Pinned dependency releases. The sha256 values come from each release's
// published SHA256SUMS file; a version bump must update them in lockstep.
// `limactl sudoers` output depends on the Lima version, but an existing
// sudoers file is never refreshed by provisioning; delete it to
// regenerate after a pin bump.
const (
	// LimaVersion is installed into the managed rootless prefix when no
	// limactl is on PATH; an existing one (brew or otherwise) wins.
	LimaVersion = "2.2.0"
	// SocketVMNetVersion carries the bridged and shared networks; it is
	// the one root-owned component.
	SocketVMNetVersion = "1.2.2"
)

var (
	limaSHA256 = map[string]string{
		"arm64":  "bbdef91774885a0d05f7b048c4eb89ae2bcf3a0c252ae7ca7934e63df76d93c3",
		"x86_64": "0d6f99c19f6e4bc3c92730c4c29d929e6927f0cb0a0ba1a84383367135a8ff31",
	}
	socketVMNetSHA256 = map[string]string{
		"arm64":  "c7bf62308fbcfdc29bdfb8373c9b1951f7ac2396446e4390919796a94972e6dc",
		"x86_64": "2968a82c97e692c2d36f87230152e8018e00589c1b598e8257775adfe83800a1",
	}
)

// machineArch names this machine in upstream release-asset terms.
func machineArch() string {
	if runtime.GOARCH == "amd64" {
		return "x86_64"
	}
	return "arm64"
}

func limaURL() string {
	return fmt.Sprintf("https://github.com/lima-vm/lima/releases/download/v%s/lima-%s-Darwin-%s.tar.gz",
		LimaVersion, LimaVersion, machineArch())
}

func socketVMNetURL() string {
	return fmt.Sprintf("https://github.com/lima-vm/socket_vmnet/releases/download/v%s/socket_vmnet-%s-%s.tar.gz",
		SocketVMNetVersion, SocketVMNetVersion, machineArch())
}

// ManagedLimaPrefix is the rootless prefix provisioning extracts Lima
// into; its bin directory joins the process PATH when no other limactl
// exists.
func ManagedLimaPrefix() (string, error) {
	home, err := os.UserHomeDir()
	if err != nil {
		return "", fmt.Errorf("resolve home directory: %w", err)
	}
	return filepath.Join(home, ".local", "share", "skali", "lima"), nil
}

// limaNetworks is the slice of ~/.lima/_config/networks.yaml this package
// consumes.
type limaNetworks struct {
	Paths struct {
		SocketVMNet string `yaml:"socketVMNet"`
		Sudoers     string `yaml:"sudoers"`
	} `yaml:"paths"`
	Networks map[string]struct {
		Mode string `yaml:"mode"`
	} `yaml:"networks"`
}

// ProvisionState is what Assess learned about the Mac-side dependencies
// of one requested network.
type ProvisionState struct {
	// LimactlPath is the resolved limactl binary; empty when Lima is
	// missing entirely.
	LimactlPath string
	// NeedsVMNet reports whether the network runs over socket_vmnet
	// (bridged and shared do, user-v2 does not).
	NeedsVMNet         bool
	NetworksFilePath   string
	NetworksFileExists bool
	// NetworkConfigured reports whether an existing networks.yaml carries
	// the requested network; meaningless while the file is absent.
	NetworkConfigured  bool
	SocketVMNetPath    string
	SocketVMNetPresent bool
	SudoersPath        string
	SudoersPresent     bool
}

// Missing reports whether anything must be provisioned before the
// network can carry a VM.
func (s ProvisionState) Missing() bool {
	if s.LimactlPath == "" {
		return true
	}
	if !s.NeedsVMNet {
		return false
	}
	return !s.NetworksFileExists || !s.SocketVMNetPresent || !s.SudoersPresent
}

// Assess inspects without mutating: it resolves limactl, reads
// networks.yaml when present, and stats the root-owned components.
func Assess(ctx context.Context, mac host.Runner, network Network) (ProvisionState, error) {
	state := ProvisionState{NeedsVMNet: network != NetworkUserV2}
	if path, err := lookPath("limactl"); err == nil {
		state.LimactlPath = path
	}
	if !state.NeedsVMNet {
		return state, nil
	}

	home, err := os.UserHomeDir()
	if err != nil {
		return state, fmt.Errorf("resolve home directory: %w", err)
	}
	state.NetworksFilePath = filepath.Join(home, ".lima", "_config", "networks.yaml")
	state.SocketVMNetPath = defaultSocketVMNetPath
	state.SudoersPath = defaultSudoersPath

	data, err := mac.ReadFile(ctx, state.NetworksFilePath)
	switch {
	case errors.Is(err, fs.ErrNotExist):
	case err != nil:
		return state, fmt.Errorf("read %s: %w", state.NetworksFilePath, err)
	default:
		state.NetworksFileExists = true
		var config limaNetworks
		if err := yaml.Unmarshal(data, &config); err != nil {
			return state, fmt.Errorf("parse %s: %w", state.NetworksFilePath, err)
		}
		_, state.NetworkConfigured = config.Networks[string(network)]
		if config.Paths.SocketVMNet != "" {
			state.SocketVMNetPath = config.Paths.SocketVMNet
		}
		if config.Paths.Sudoers != "" {
			state.SudoersPath = config.Paths.Sudoers
		}
	}

	info, err := mac.Stat(ctx, state.SocketVMNetPath)
	if err != nil {
		return state, fmt.Errorf("stat %s: %w", state.SocketVMNetPath, err)
	}
	state.SocketVMNetPresent = info.Exists
	info, err = mac.Stat(ctx, state.SudoersPath)
	if err != nil {
		return state, fmt.Errorf("stat %s: %w", state.SudoersPath, err)
	}
	state.SudoersPresent = info.Exists
	return state, nil
}

// NetworkNotConfiguredError is the one dependency gap provisioning never
// closes: networks.yaml exists but lacks the requested network. The file
// is operator configuration and is never edited.
func NetworkNotConfiguredError(network Network, path string) error {
	return fmt.Errorf("the Lima network %q is not configured in %s; that file is operator "+
		"configuration and is never edited; add the network there "+
		"(see https://lima-vm.io/docs/config/network/) and re-run", network, path)
}

// httpGet is a seam for tests; production downloads over net/http.
var httpGet = func(ctx context.Context, url string) (io.ReadCloser, error) {
	request, err := http.NewRequestWithContext(ctx, http.MethodGet, url, nil)
	if err != nil {
		return nil, err
	}
	response, err := http.DefaultClient.Do(request)
	if err != nil {
		return nil, err
	}
	if response.StatusCode != http.StatusOK {
		response.Body.Close()
		return nil, fmt.Errorf("GET %s: %s", url, response.Status)
	}
	return response.Body, nil
}

// download fetches url into a temp file and verifies its sha256 before
// anything consumes it; a mismatch removes the file and fails.
func download(ctx context.Context, url, wantSHA string) (string, error) {
	body, err := httpGet(ctx, url)
	if err != nil {
		return "", err
	}
	defer body.Close()
	temp, err := os.CreateTemp("", "skali-download-*")
	if err != nil {
		return "", err
	}
	hasher := sha256.New()
	_, copyErr := io.Copy(io.MultiWriter(temp, hasher), body)
	if err := cmp.Or(copyErr, temp.Close()); err != nil {
		os.Remove(temp.Name())
		return "", fmt.Errorf("download %s: %w", url, err)
	}
	if got := hex.EncodeToString(hasher.Sum(nil)); got != wantSHA {
		os.Remove(temp.Name())
		return "", fmt.Errorf("checksum mismatch for %s: got %s, want %s", url, got, wantSHA)
	}
	return temp.Name(), nil
}

// InstallLima downloads the pinned Lima release, verifies it, and
// extracts it into the managed rootless prefix (bin/, share/lima/, ...).
// It returns the extracted limactl path; the caller makes it visible on
// PATH. Skips nothing itself: callers only reach it when limactl is
// absent.
func InstallLima(ctx context.Context, mac host.Runner) (string, error) {
	prefix, err := ManagedLimaPrefix()
	if err != nil {
		return "", err
	}
	archive, err := download(ctx, limaURL(), limaSHA256[machineArch()])
	if err != nil {
		return "", err
	}
	defer os.Remove(archive)
	if err := mac.MkdirAll(ctx, prefix, 0o755); err != nil {
		return "", err
	}
	result, err := mac.Run(ctx, host.Command{Name: "tar", Args: []string{"-xzf", archive, "-C", prefix}})
	if err != nil {
		return "", err
	}
	if result.ExitCode != 0 {
		return "", fmt.Errorf("extract lima into %s: exit %d: %s", prefix, result.ExitCode, tail(result.Stderr))
	}
	return filepath.Join(prefix, "bin", "limactl"), nil
}

// DetectDefaultInterface names the NIC behind the default route, which is
// what a written-from-scratch bridged network should attach to. Detection
// failures fall back to en0, matching Lima's own default.
func DetectDefaultInterface(ctx context.Context, mac host.Runner) string {
	result, err := mac.Run(ctx, host.Command{Name: "route", Args: []string{"-n", "get", "default"}})
	if err != nil || result.ExitCode != 0 {
		return "en0"
	}
	for line := range strings.SplitSeq(result.Stdout, "\n") {
		if value, ok := strings.CutPrefix(strings.TrimSpace(line), "interface:"); ok {
			if iface := strings.TrimSpace(value); iface != "" {
				return iface
			}
		}
	}
	return "en0"
}

// defaultNetworksYAML mirrors the file Lima itself generates, with the
// bridged interface substituted for the detected one.
const defaultNetworksYAML = `# Written by skali cluster, only because no networks.yaml existed; it is
# operator configuration afterwards and is never edited again.
paths:
  socketVMNet: /opt/socket_vmnet/bin/socket_vmnet
  varRun: /private/var/run/lima
  sudoers: /private/etc/sudoers.d/lima
networks:
  shared:
    mode: shared
    gateway: 192.168.105.1
    dhcpEnd: 192.168.105.254
    netmask: 255.255.255.0
  bridged:
    mode: bridged
    interface: %s
`

// WriteDefaultNetworks writes the default networks.yaml with the given
// bridged interface. An existing file is left byte for byte untouched.
func WriteDefaultNetworks(ctx context.Context, mac host.Runner, iface string) error {
	home, err := os.UserHomeDir()
	if err != nil {
		return fmt.Errorf("resolve home directory: %w", err)
	}
	path := filepath.Join(home, ".lima", "_config", "networks.yaml")
	info, err := mac.Stat(ctx, path)
	if err != nil {
		return fmt.Errorf("stat %s: %w", path, err)
	}
	if info.Exists {
		return nil
	}
	if err := mac.MkdirAll(ctx, filepath.Dir(path), 0o755); err != nil {
		return err
	}
	return mac.WriteFile(ctx, path, fmt.Appendf(nil, defaultNetworksYAML, iface), 0o644)
}

// InstallSocketVMNet downloads and verifies the pinned socket_vmnet
// release, then extracts it root owned in one privileged step: the
// tarball carries opt/socket_vmnet/..., so `sudo tar Cxzf /` lands it at
// the fixed root-owned path. sudo prompts on the terminal.
func InstallSocketVMNet(ctx context.Context, mac host.Runner) error {
	archive, err := download(ctx, socketVMNetURL(), socketVMNetSHA256[machineArch()])
	if err != nil {
		return err
	}
	defer os.Remove(archive)
	result, err := mac.Run(ctx, host.Command{
		Name: "sudo",
		Args: []string{"tar", "Cxzf", "/", archive, "opt/socket_vmnet"},
	})
	if err != nil {
		return err
	}
	if result.ExitCode != 0 {
		return fmt.Errorf("install socket_vmnet: exit %d: %s", result.ExitCode, tail(result.Stderr))
	}
	return nil
}

// WriteSudoers generates the Lima sudoers rules and writes them in one
// privileged step, exactly as the printed plan states. The rules derive
// from networks.yaml, which must exist first. sudo prompts on the
// terminal.
func WriteSudoers(ctx context.Context, mac host.Runner) error {
	result, err := mac.Run(ctx, host.Command{
		Name: "sh",
		Args: []string{"-c", "limactl sudoers | sudo tee /etc/sudoers.d/lima >/dev/null"},
	})
	if err != nil {
		return err
	}
	if result.ExitCode != 0 {
		return fmt.Errorf("write /etc/sudoers.d/lima: exit %d: %s", result.ExitCode, tail(result.Stderr))
	}
	return nil
}

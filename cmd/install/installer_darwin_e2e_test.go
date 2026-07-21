package main

import (
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strconv"
	"strings"
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/Hinkolas/skali/internal/installer/limavm"
)

// The darwin end-to-end suite drives the NATIVE skali-installer binary on
// this Mac: it creates its own small Lima VM (user-v2, so no socket_vmnet
// setup is needed), installs a server inside it, initializes Skali, proves
// the kubeconfig address rewrite through a healthy status, and deletes the
// VM again through the node-scope uninstall. Gated: it pulls operator
// images and takes many minutes.
//
//	limactl stop skali-e2e skali-e2e-agent   # this Mac does not fit all three VMs
//	task test:installer:darwin
//
// The suite owns the VM lifecycle end to end and cleans up on failure.
const darwinE2EVM = "skali-e2e-darwin"

func TestInstallerDarwin(t *testing.T) {
	if os.Getenv("TEST_SKALI_INSTALLER_DARWIN") == "" {
		t.Skip("set TEST_SKALI_INSTALLER_DARWIN=1 to run the darwin installer end-to-end suite")
	}
	if runtime.GOOS != "darwin" {
		t.Skip("the darwin installer suite runs only on macOS")
	}
	h := newBareHarness(t)

	// RAM guard: three VMs do not fit next to macOS on a 16GiB machine.
	list, code := h.hostCommand("limactl", "list", "--format", "{{.Name}} {{.Status}}")
	require.Equal(t, 0, code, list)
	for _, name := range []string{e2eVM, e2eAgentVM} {
		require.NotContains(t, list, name+" Running",
			"stop the Linux e2e VMs first (limactl stop %s %s); this Mac does not fit all three VMs",
			e2eVM, e2eAgentVM)
	}

	home, err := os.UserHomeDir()
	require.NoError(t, err)
	plistPath := filepath.Join(home, "Library", "LaunchAgents", limavm.LaunchAgentLabel+".plist")
	t.Cleanup(func() {
		h.hostCommand("limactl", "delete", "-f", darwinE2EVM)
		h.hostCommand("launchctl", "bootout", "gui/"+strconv.Itoa(os.Getuid())+"/"+limavm.LaunchAgentLabel)
		os.Remove(plistPath)
	})

	// Native build, run directly and never under sudo: on a Mac the
	// privileged work happens inside the VM through limactl.
	binary := filepath.Join(t.TempDir(), "skali-installer")
	build := exec.Command("go", "build", "-o", binary, "./cmd/install")
	build.Dir = h.repoRoot
	buildOut, err := build.CombinedOutput()
	require.NoError(t, err, "build: %s", buildOut)
	run := func(args ...string) (string, int) {
		t.Helper()
		return h.hostCommand(binary, append([]string{"--vm", darwinE2EVM}, args...)...)
	}

	// Fresh detection: no VM means a fresh Mac.
	statusOut, code := run("status")
	require.Equal(t, 0, code, statusOut)
	require.Contains(t, statusOut, "fresh")
	require.Contains(t, statusOut, "vm      none")

	// Install: creates the VM, installs the pinned k3s server inside it,
	// and registers the login LaunchAgent.
	nodeConfig := filepath.Join(t.TempDir(), "node.yaml")
	require.NoError(t, os.WriteFile(nodeConfig, fmt.Appendf(nil, `cluster: e2e
role: server
capabilities: [application, database, object-storage, registry, edge]
vm:
  name: %s
  network: user-v2
  cpus: 2
  memory: 3GiB
  disk: 15GiB
`, darwinE2EVM), 0o644))
	installOut, code := run("install", "--config", nodeConfig)
	require.Equal(t, 0, code, installOut)
	require.Contains(t, installOut, "Create VM "+darwinE2EVM)

	list, code = h.hostCommand("limactl", "list", "--format", "{{.Name}} {{.Status}}")
	require.Equal(t, 0, code, list)
	require.Contains(t, list, darwinE2EVM+" Running")

	record := h.vmOKOn(darwinE2EVM, "sudo", "cat", "/var/lib/skali/installation.yaml")
	require.Contains(t, record, "cluster: e2e")
	require.Contains(t, record, "role: server")
	require.Contains(t, record, "k3s: v1.33.3+k3s1")

	// The guest hostname is derived from the Mac's, so a fleet of Macs
	// yields distinct node names.
	macHostname, err := os.Hostname()
	require.NoError(t, err)
	guestHostname := strings.TrimSpace(h.vmOKOn(darwinE2EVM, "hostname"))
	require.Equal(t, limavm.GuestHostname(macHostname), guestHostname)

	_, err = os.Stat(plistPath)
	require.NoError(t, err, "the login LaunchAgent must exist after install")

	// Repeat install refuses: the VM already carries an installation.
	repeatOut, code := run("install", "--config", nodeConfig)
	require.NotEqual(t, 0, code, repeatOut)

	// Build the control-plane image on the Mac; init stages the tar into
	// the VM's containerd through the runner (the source-install path).
	imageTar := filepath.Join(t.TempDir(), "skalid-dev.tar")
	dockerOut, code := h.hostCommand("docker", "build", "-t", "skalid:dev",
		"-f", filepath.Join(h.repoRoot, "build", "skalid.Dockerfile"), h.repoRoot)
	require.Equal(t, 0, code, dockerOut)
	saveOut, code := h.hostCommand("docker", "save", "skalid:dev", "-o", imageTar)
	require.Equal(t, 0, code, saveOut)

	// Init with a MAC-side password file: the installer must read it on
	// this machine, not inside the VM.
	passwordFile := filepath.Join(t.TempDir(), "admin-password")
	require.NoError(t, os.WriteFile(passwordFile, []byte("e2e-admin-password\n"), 0o600))
	initConfig := filepath.Join(t.TempDir(), "init.yaml")
	require.NoError(t, os.WriteFile(initConfig, fmt.Appendf(nil, `endpoints:
  api: skali.e2e.test
  registry: registry.skali.e2e.test
tls:
  issuerEmail: e2e@skali.e2e.test
  acmeServer: https://acme-staging-v02.api.letsencrypt.org/directory
admin:
  email: admin@skali.e2e.test
  passwordFile: %s
skalid:
  image: skalid:dev
`, passwordFile), 0o644))
	initOut, code := run("init", "--config", initConfig, "--image-tar", imageTar)
	require.Equal(t, 0, code, initOut)
	require.Contains(t, initOut, "Import skalid image skalid:dev")

	// A healthy status can only come from a working kube client, and the
	// template forwards guest 6443 to host 16443: an unrewritten kubeconfig
	// would dial 127.0.0.1:6443 on this Mac and fail. This is the
	// end-to-end proof of the address rewrite.
	statusOut, code = run("status")
	require.Equal(t, 0, code, statusOut)
	require.Contains(t, statusOut, "Skali server")
	require.Contains(t, statusOut, "vm         "+darwinE2EVM)
	require.Contains(t, statusOut, "database healthy")
	require.Contains(t, statusOut, "skalid healthy")
	require.Contains(t, statusOut, "1 joined")
	require.NotContains(t, statusOut, "kubernetes api unreachable")

	// Repeat bare execution with closed stdin performs no mutation.
	before := h.vmOKOn(darwinE2EVM, "sudo", "k3s", "kubectl", "get", "deploy", "-n", "skali-system",
		"-o", "jsonpath={range .items[*]}{.metadata.name}={.metadata.resourceVersion} {end}")
	repeatRun, code := h.hostCommand("sh", "-c", binary+" --vm "+darwinE2EVM+" </dev/null")
	require.Equal(t, 0, code, repeatRun)
	after := h.vmOKOn(darwinE2EVM, "sudo", "k3s", "kubectl", "get", "deploy", "-n", "skali-system",
		"-o", "jsonpath={range .items[*]}{.metadata.name}={.metadata.resourceVersion} {end}")
	require.Equal(t, before, after, "read-only detection must not mutate the cluster")

	// Node-scope uninstall deletes the VM and the LaunchAgent.
	uninstallOut, code := run("uninstall", "--scope", "node", "--confirm", "e2e")
	require.Equal(t, 0, code, uninstallOut)
	require.Contains(t, uninstallOut, "This Mac is fresh again.")

	list, code = h.hostCommand("limactl", "list", "--format", "{{.Name}} {{.Status}}")
	require.Equal(t, 0, code, list)
	require.NotContains(t, list, darwinE2EVM)
	_, err = os.Stat(plistPath)
	require.ErrorIs(t, err, os.ErrNotExist, "the login LaunchAgent must be gone after uninstall")

	// The Mac detects as fresh again.
	statusOut, code = run("status")
	require.Equal(t, 0, code, statusOut)
	require.Contains(t, statusOut, "fresh")
	require.Contains(t, statusOut, "vm      none")
}

package main

import (
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"github.com/stretchr/testify/require"
)

// The end-to-end suite drives the built skali-installer binary inside the
// skali-e2e Lima VM (a faithful fresh Ubuntu host with real systemd)
// through the full cycle: install, init, status, repeat-run no-op, scoped
// uninstall. Gated: it installs k3s and pulls operator images inside the
// VM and takes many minutes on the first run.
//
//	task lima:up
//	task test:installer
//
// The VM's lifecycle belongs to the lima:up/lima:down tasks; the suite
// leaves the host fresh again through the node uninstall it exercises.
const e2eVM = "skali-e2e"

type installerHarness struct {
	t        *testing.T
	repoRoot string
	// binary and fixtures live under /tmp inside the VM.
}

func newInstallerHarness(t *testing.T) *installerHarness {
	t.Helper()
	if os.Getenv("TEST_SKALI_INSTALLER") == "" {
		t.Skip("set TEST_SKALI_INSTALLER=1 to run the installer end-to-end suite")
	}
	repoRoot, err := filepath.Abs("../..")
	require.NoError(t, err)
	harness := &installerHarness{t: t, repoRoot: repoRoot}

	// The VM must exist (task lima:up); start it if it is only stopped.
	out, code := harness.hostCommand("limactl", "list", "--format", "{{.Name}} {{.Status}}")
	require.Equal(t, 0, code, "limactl list: %s", out)
	switch {
	case strings.Contains(out, e2eVM+" Running"):
	case strings.Contains(out, e2eVM+" Stopped"):
		out, code = harness.hostCommand("limactl", "start", e2eVM)
		require.Equal(t, 0, code, "limactl start: %s", out)
	default:
		t.Fatalf("the %s VM does not exist; create it with `task lima:up` first", e2eVM)
	}
	return harness
}

// hostCommand runs a command on the developer machine.
func (h *installerHarness) hostCommand(name string, args ...string) (string, int) {
	h.t.Helper()
	command := exec.Command(name, args...)
	command.Dir = h.repoRoot
	out, err := command.CombinedOutput()
	if exit, ok := err.(*exec.ExitError); ok {
		return string(out), exit.ExitCode()
	}
	require.NoError(h.t, err, "%s %s: %s", name, strings.Join(args, " "), out)
	return string(out), 0
}

// vm runs a command inside the VM.
func (h *installerHarness) vm(args ...string) (string, int) {
	h.t.Helper()
	return h.hostCommand("limactl", append([]string{"shell", e2eVM, "--"}, args...)...)
}

// vmOK runs a command inside the VM and requires success.
func (h *installerHarness) vmOK(args ...string) string {
	h.t.Helper()
	out, code := h.vm(args...)
	require.Equal(h.t, 0, code, "%s: %s", strings.Join(args, " "), out)
	return out
}

func (h *installerHarness) copyIn(local, remote string) {
	h.t.Helper()
	out, code := h.hostCommand("limactl", "cp", local, e2eVM+":"+remote)
	require.Equal(h.t, 0, code, "limactl cp %s: %s", local, out)
}

func (h *installerHarness) writeFixture(name, content string) string {
	h.t.Helper()
	local := filepath.Join(h.t.TempDir(), name)
	require.NoError(h.t, os.WriteFile(local, []byte(content), 0o644))
	remote := "/tmp/" + name
	h.copyIn(local, remote)
	return remote
}

func TestInstallerEndToEnd(t *testing.T) {
	h := newInstallerHarness(t)

	// Cross-compile for the VM's architecture and copy the binary in.
	arch := strings.TrimSpace(h.vmOK("uname", "-m"))
	goArch := map[string]string{"aarch64": "arm64", "x86_64": "amd64"}[arch]
	require.NotEmpty(t, goArch, "unexpected VM architecture %q", arch)
	binary := filepath.Join(t.TempDir(), "skali-installer")
	build := exec.Command("go", "build", "-o", binary, "./cmd/install")
	build.Dir = h.repoRoot
	build.Env = append(os.Environ(), "GOOS=linux", "GOARCH="+goArch, "CGO_ENABLED=0")
	out, err := build.CombinedOutput()
	require.NoError(t, err, "cross-compile: %s", out)
	h.copyIn(binary, "/tmp/skali-installer")

	// Fresh detection before anything is installed.
	statusOut, code := h.vm("sudo", "/tmp/skali-installer", "status")
	require.Equal(t, 0, code, statusOut)
	require.Contains(t, statusOut, "fresh")

	// Install: pinned k3s server with every capability.
	nodeConfig := h.writeFixture("node.yaml", "cluster: e2e\nrole: server\ncapabilities: [application, database, object-storage, registry, edge]\n")
	installOut, code := h.vm("sudo", "/tmp/skali-installer", "install", "--config", nodeConfig)
	require.Equal(t, 0, code, installOut)

	record := h.vmOK("sudo", "cat", "/var/lib/skali/installation.yaml")
	require.Contains(t, record, "cluster: e2e")
	require.Contains(t, record, "role: server")
	require.Contains(t, record, "k3s: v1.33.3+k3s1")

	labels := h.vmOK("sudo", "k3s", "kubectl", "get", "nodes", "-o", "jsonpath={.items[0].metadata.labels}")
	for _, capability := range []string{"application", "database", "object-storage", "registry", "edge"} {
		require.Contains(t, labels, "skali.dev/capability-"+capability)
	}

	// Repeat install refuses: the host is no longer fresh.
	repeatOut, code := h.vm("sudo", "/tmp/skali-installer", "install", "--config", nodeConfig)
	require.NotEqual(t, 0, code, repeatOut)

	// Stage the control-plane image: built on the host, imported into k3s
	// containerd (bootstrap images never come from the managed registry).
	imageTar := filepath.Join(t.TempDir(), "skalid-dev.tar")
	buildOut, code := h.hostCommand("docker", "build", "-t", "skalid:dev",
		"-f", filepath.Join(h.repoRoot, "build", "skalid.Dockerfile"), h.repoRoot)
	require.Equal(t, 0, code, buildOut)
	saveOut, code := h.hostCommand("docker", "save", "skalid:dev", "-o", imageTar)
	require.Equal(t, 0, code, saveOut)
	h.copyIn(imageTar, "/tmp/skalid-dev.tar")
	h.vmOK("sudo", "k3s", "ctr", "images", "import", "/tmp/skalid-dev.tar")

	// Init: the ACME staging directory keeps pending issuance away from
	// production rate limits; skali.e2e.test never resolves, so
	// certificates stay pending by design and nothing asserts TLS.
	passwordFile := h.writeFixture("admin-password", "e2e-admin-password\n")
	initConfig := h.writeFixture("init.yaml", fmt.Sprintf(`endpoints:
  api: skali.e2e.test
tls:
  issuerEmail: e2e@skali.e2e.test
  acmeServer: https://acme-staging-v02.api.letsencrypt.org/directory
admin:
  email: admin@skali.e2e.test
  passwordFile: %s
skalid:
  image: skalid:dev
`, passwordFile))
	initOut, code := h.vm("sudo", "/tmp/skali-installer", "init", "--config", initConfig)
	require.Equal(t, 0, code, initOut)

	// The control plane answers through the service proxy and the
	// in-cluster record exists.
	health := h.vmOK("sudo", "k3s", "kubectl", "get", "--raw",
		"/api/v1/namespaces/skali-system/services/skalid:80/proxy/healthz")
	require.NotEmpty(t, health)
	h.vmOK("sudo", "k3s", "kubectl", "get", "configmap", "-n", "skali-system", "skali-installation")

	statusOut, code = h.vm("sudo", "/tmp/skali-installer", "status")
	require.Equal(t, 0, code, statusOut)
	require.Contains(t, statusOut, "Skali server")
	require.Contains(t, statusOut, "database healthy")
	require.Contains(t, statusOut, "skalid healthy")

	// Repeat bare execution with closed stdin performs no mutation.
	before := h.vmOK("sudo", "k3s", "kubectl", "get", "deploy", "-n", "skali-system",
		"-o", "jsonpath={range .items[*]}{.metadata.name}={.metadata.resourceVersion} {end}")
	repeatRun, code := h.vm("sh", "-c", "sudo /tmp/skali-installer </dev/null")
	require.Equal(t, 0, code, repeatRun)
	after := h.vmOK("sudo", "k3s", "kubectl", "get", "deploy", "-n", "skali-system",
		"-o", "jsonpath={range .items[*]}{.metadata.name}={.metadata.resourceVersion} {end}")
	require.Equal(t, before, after, "read-only detection must not mutate the cluster")

	// Scoped uninstall: bundle first (bare k3s survives), then the node.
	bundleOut, code := h.vm("sudo", "/tmp/skali-installer", "uninstall", "--scope", "bundle", "--confirm", "e2e")
	require.Equal(t, 0, code, bundleOut)
	_, code = h.vm("sudo", "k3s", "kubectl", "get", "namespace", "skali-system")
	require.NotEqual(t, 0, code, "skali-system must be gone after the bundle uninstall")
	active := strings.TrimSpace(h.vmOK("systemctl", "is-active", "k3s"))
	require.Equal(t, "active", active, "bare k3s must keep running")

	nodeOut, code := h.vm("sudo", "/tmp/skali-installer", "uninstall", "--scope", "node", "--confirm", "e2e")
	require.Equal(t, 0, code, nodeOut)
	_, code = h.vm("test", "-e", "/usr/local/bin/k3s")
	require.NotEqual(t, 0, code, "k3s must be uninstalled")
	_, code = h.vm("sudo", "test", "-e", "/var/lib/skali")
	require.NotEqual(t, 0, code, "/var/lib/skali must be gone")

	// The host detects as fresh again.
	statusOut, code = h.vm("sudo", "/tmp/skali-installer", "status")
	require.Equal(t, 0, code, statusOut)
	require.Contains(t, statusOut, "fresh")
}

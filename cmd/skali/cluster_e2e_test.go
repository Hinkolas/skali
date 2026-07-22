package main

import (
	"encoding/json"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/Hinkolas/skali/internal/installer"
)

// The end-to-end suite drives the built skali binary's cluster group inside
// the skali-e2e Lima VMs (faithful fresh Ubuntu hosts with real systemd)
// through the full cycle: install, init, status, version upgrade, agent
// token/join, repeat-run no-op, scoped uninstall. Gated: it installs k3s
// (twice: an older pin, then the upgrade to the current one) and pulls
// operator images inside the VMs and takes many minutes on the first run.
//
//	task lima:up
//	task test:cluster
//
// The VM lifecycle belongs to the lima:up/lima:down tasks; the suite
// leaves both hosts fresh again through the node uninstalls it exercises.
const (
	e2eVM      = "skali-e2e"
	e2eAgentVM = "skali-e2e-agent"

	// e2eOlderK3s is the pin of the simulated older installer build the
	// upgrade phase moves away from; it must be a real k3s release older
	// than the current installer.K3sVersion pin.
	e2eOlderK3s = "v1.33.2+k3s1"
)

type installerHarness struct {
	t        *testing.T
	repoRoot string
	// binary and fixtures live under /tmp inside the VMs.
}

// newBareHarness carries only the helper core; each suite adds its own
// gate and VM expectations on top.
func newBareHarness(t *testing.T) *installerHarness {
	t.Helper()
	repoRoot, err := filepath.Abs("../..")
	require.NoError(t, err)
	return &installerHarness{t: t, repoRoot: repoRoot}
}

func newInstallerHarness(t *testing.T) *installerHarness {
	t.Helper()
	if os.Getenv("TEST_SKALI_CLUSTER") == "" {
		t.Skip("set TEST_SKALI_CLUSTER=1 to run the cluster end-to-end suite")
	}
	harness := newBareHarness(t)
	harness.ensureVM(e2eVM)
	harness.ensureVM(e2eAgentVM)
	return harness
}

// ensureVM requires the named VM to exist (task lima:up) and starts it if
// it is only stopped.
func (h *installerHarness) ensureVM(name string) {
	h.t.Helper()
	out, code := h.hostCommand("limactl", "list", "--format", "{{.Name}} {{.Status}}")
	require.Equal(h.t, 0, code, "limactl list: %s", out)
	switch {
	case strings.Contains(out, name+" Running"):
	case strings.Contains(out, name+" Stopped"):
		out, code = h.hostCommand("limactl", "start", name)
		require.Equal(h.t, 0, code, "limactl start: %s", out)
	default:
		h.t.Fatalf("the %s VM does not exist; create it with `task lima:up` first", name)
	}
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

// vmOn runs a command inside the named VM.
func (h *installerHarness) vmOn(name string, args ...string) (string, int) {
	h.t.Helper()
	return h.hostCommand("limactl", append([]string{"shell", name, "--"}, args...)...)
}

// vmOKOn runs a command inside the named VM and requires success.
func (h *installerHarness) vmOKOn(name string, args ...string) string {
	h.t.Helper()
	out, code := h.vmOn(name, args...)
	require.Equal(h.t, 0, code, "%s: %s", strings.Join(args, " "), out)
	return out
}

func (h *installerHarness) copyInTo(name, local, remote string) {
	h.t.Helper()
	out, code := h.hostCommand("limactl", "cp", local, name+":"+remote)
	require.Equal(h.t, 0, code, "limactl cp %s: %s", local, out)
}

func (h *installerHarness) writeFixtureOn(name, fname, content string) string {
	h.t.Helper()
	local := filepath.Join(h.t.TempDir(), fname)
	require.NoError(h.t, os.WriteFile(local, []byte(content), 0o644))
	remote := "/tmp/" + fname
	h.copyInTo(name, local, remote)
	return remote
}

// vmIP reads the address of the shared network. The user-v2 network
// replaces the default NIC, so eth0 carries the address the VMs can reach
// each other on.
func (h *installerHarness) vmIP(name string) string {
	h.t.Helper()
	out := h.vmOKOn(name, "sh", "-c",
		"ip -4 -o addr show dev eth0 | awk '{print $4}' | cut -d/ -f1")
	ip := strings.TrimSpace(out)
	require.Regexp(h.t, `^\d+\.\d+\.\d+\.\d+$`, ip,
		"no IPv4 address on eth0 in %s; the VM predates the shared-network "+
			"template and must be recreated (task lima:down && task lima:up)", name)
	return ip
}

// Server-VM shorthands keep the single-node phases readable.
func (h *installerHarness) vm(args ...string) (string, int) {
	h.t.Helper()
	return h.vmOn(e2eVM, args...)
}

func (h *installerHarness) vmOK(args ...string) string {
	h.t.Helper()
	return h.vmOKOn(e2eVM, args...)
}

func (h *installerHarness) copyIn(local, remote string) {
	h.t.Helper()
	h.copyInTo(e2eVM, local, remote)
}

func (h *installerHarness) writeFixture(name, content string) string {
	h.t.Helper()
	return h.writeFixtureOn(e2eVM, name, content)
}

func TestClusterEndToEnd(t *testing.T) {
	h := newInstallerHarness(t)

	// Cross-compile two builds for the VM's architecture: build A
	// simulates the older release the host was first installed with (its
	// own version stamp and an older k3s pin), and the plain build is the
	// current installer under test, which later upgrades A's work.
	arch := strings.TrimSpace(h.vmOK("uname", "-m"))
	vmGoArch := map[string]string{"aarch64": "arm64", "x86_64": "amd64"}[arch]
	require.NotEmpty(t, vmGoArch, "unexpected VM architecture %q", arch)
	binary := filepath.Join(t.TempDir(), "skali")
	build := exec.Command("go", "build", "-o", binary, "./cmd/skali")
	build.Dir = h.repoRoot
	build.Env = append(os.Environ(), "GOOS=linux", "GOARCH="+vmGoArch, "CGO_ENABLED=0")
	out, err := build.CombinedOutput()
	require.NoError(t, err, "cross-compile: %s", out)
	h.copyIn(binary, "/tmp/skali")

	binaryA := filepath.Join(t.TempDir(), "skali-a")
	buildA := exec.Command("go", "build", "-ldflags",
		"-X github.com/Hinkolas/skali/internal/version.Version=0.0.0-e2e-a"+
			" -X github.com/Hinkolas/skali/internal/installer.K3sVersion="+e2eOlderK3s,
		"-o", binaryA, "./cmd/skali")
	buildA.Dir = h.repoRoot
	buildA.Env = append(os.Environ(), "GOOS=linux", "GOARCH="+vmGoArch, "CGO_ENABLED=0")
	outA, errA := buildA.CombinedOutput()
	require.NoError(t, errA, "cross-compile older build: %s", outA)
	h.copyIn(binaryA, "/tmp/skali-a")

	// Fresh detection before anything is installed.
	statusOut, code := h.vm("sudo", "/tmp/skali-a", "cluster", "status")
	require.Equal(t, 0, code, statusOut)
	require.Contains(t, statusOut, "fresh")

	// Install: pinned k3s server with every capability. The advertised
	// address is pinned to the shared vmnet interface so the agent VM can
	// reach the server (and so its address lands in the serving cert).
	serverIP := h.vmIP(e2eVM)
	nodeConfig := h.writeFixture("node.yaml", fmt.Sprintf(
		"cluster: e2e\nrole: server\ncapabilities: [application, database, object-storage, registry, edge]\nnodeIP: %s\n", serverIP))
	installOut, code := h.vm("sudo", "/tmp/skali-a", "cluster", "install", "--config", nodeConfig)
	require.Equal(t, 0, code, installOut)

	record := h.vmOK("sudo", "cat", "/var/lib/skali/installation.yaml")
	require.Contains(t, record, "cluster: e2e")
	require.Contains(t, record, "role: server")
	require.Contains(t, record, "k3s: "+e2eOlderK3s)

	labels := h.vmOK("sudo", "k3s", "kubectl", "get", "nodes", "-o", "jsonpath={.items[0].metadata.labels}")
	for _, capability := range []string{"application", "database", "object-storage", "registry", "edge"} {
		require.Contains(t, labels, "skali.dev/capability-"+capability)
	}

	// Repeat install refuses: the host is no longer fresh.
	repeatOut, code := h.vm("sudo", "/tmp/skali-a", "cluster", "install", "--config", nodeConfig)
	require.NotEqual(t, 0, code, repeatOut)

	// Stage the control-plane image: built on the host and handed to init
	// as a tar (bootstrap images never come from the managed registry).
	imageTar := filepath.Join(t.TempDir(), "skalid-dev.tar")
	buildOut, code := h.hostCommand("docker", "build", "-t", "skalid:dev",
		"-f", filepath.Join(h.repoRoot, "build", "skalid.Dockerfile"), h.repoRoot)
	require.Equal(t, 0, code, buildOut)
	saveOut, code := h.hostCommand("docker", "save", "skalid:dev", "-o", imageTar)
	require.Equal(t, 0, code, saveOut)
	h.copyIn(imageTar, "/tmp/skalid-dev.tar")

	// Init: the ACME staging directory keeps pending issuance away from
	// production rate limits; skali.e2e.test never resolves, so
	// certificates stay pending by design and nothing asserts TLS.
	passwordFile := h.writeFixture("admin-password", "e2e-admin-password\n")
	initConfig := h.writeFixture("init.yaml", fmt.Sprintf(`endpoints:
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
`, passwordFile))
	// --image-tar imports the tar into the node's containerd during init.
	initOut, code := h.vm("sudo", "/tmp/skali-a", "cluster", "init", "--config", initConfig,
		"--image-tar", "/tmp/skalid-dev.tar")
	require.Equal(t, 0, code, initOut)
	require.Contains(t, initOut, "Import skalid image skalid:dev")
	require.Contains(t, initOut, "https://registry.skali.e2e.test")

	// The control plane answers through the service proxy and the
	// in-cluster record exists.
	health := h.vmOK("sudo", "k3s", "kubectl", "get", "--raw",
		"/api/v1/namespaces/skali-system/services/skalid:80/proxy/healthz")
	require.NotEmpty(t, health)
	h.vmOK("sudo", "k3s", "kubectl", "get", "configmap", "-n", "skali-system", "skali-installation")

	statusOut, code = h.vm("sudo", "/tmp/skali-a", "cluster", "status")
	require.Equal(t, 0, code, statusOut)
	require.Contains(t, statusOut, "Skali server")
	require.Contains(t, statusOut, "database healthy")
	require.Contains(t, statusOut, "skalid healthy")

	// Upgrade phase: the current build takes over the installation build A
	// created, moving k3s to the current pin and re-converging the bundle
	// under the current version. It runs before the protocol and join
	// phases so those exercise the upgraded cluster and the agent joins a
	// server already on the current pin.
	statusOut, code = h.vm("sudo", "/tmp/skali", "cluster", "status")
	require.Equal(t, 0, code, statusOut)
	require.Contains(t, statusOut, "(expected "+installer.K3sVersion+")")
	require.Contains(t, statusOut, "(skali is 0.0.0-dev)")

	// An older record missing a later-added field fails cleanly under
	// --yes, naming the field, and mutates nothing.
	h.vmOK("sudo", "cp", "/var/lib/skali/installation.yaml", "/tmp/installation-backup.yaml")
	h.vmOK("sudo", "sed", "-i", "/registry:/d", "/var/lib/skali/installation.yaml")
	missingOut, code := h.vm("sudo", "/tmp/skali", "cluster", "upgrade", "--yes",
		"--image-tar", "/tmp/skalid-dev.tar")
	require.NotEqual(t, 0, code, missingOut)
	require.Contains(t, missingOut, "registry domain")
	require.Contains(t, missingOut, "interactively")
	h.vmOK("sudo", "cp", "/tmp/installation-backup.yaml", "/var/lib/skali/installation.yaml")

	// The upgrade itself, reusing the already-staged image tar.
	upgradeOut, code := h.vm("sudo", "/tmp/skali", "cluster", "upgrade", "--yes",
		"--image-tar", "/tmp/skalid-dev.tar")
	require.Equal(t, 0, code, upgradeOut)
	require.Contains(t, upgradeOut, e2eOlderK3s+" -> "+installer.K3sVersion)
	require.Contains(t, upgradeOut, "0.0.0-e2e-a -> 0.0.0-dev")
	require.Contains(t, upgradeOut, "Upgrade k3s to "+installer.K3sVersion)
	require.Contains(t, upgradeOut, "upgrade complete:")

	versionOut := h.vmOK("k3s", "--version")
	require.Contains(t, versionOut, installer.K3sVersion)
	record = h.vmOK("sudo", "cat", "/var/lib/skali/installation.yaml")
	require.Contains(t, record, "k3s: "+installer.K3sVersion)
	require.Contains(t, record, "bundle: 0.0.0-dev")

	// The converge restamped the bundle hash on the namespace.
	namespaceJSON := h.vmOK("sudo", "k3s", "kubectl", "get", "namespace", "skali-system", "-o", "json")
	require.Contains(t, namespaceJSON, `"skali.dev/bundle-hash"`)
	require.NotContains(t, namespaceJSON, `"skali.dev/bundle-hash": ""`)

	// The k3s script re-run must preserve the host-owned config files.
	registriesAfter := h.vmOK("sudo", "cat", "/etc/rancher/k3s/registries.yaml")
	require.Contains(t, registriesAfter, "registry.skali.internal")
	require.Contains(t, registriesAfter, "password:")

	statusOut, code = h.vm("sudo", "/tmp/skali", "cluster", "status")
	require.Equal(t, 0, code, statusOut)
	require.Contains(t, statusOut, installer.K3sVersion+" (current)")
	require.Contains(t, statusOut, "0.0.0-dev (current)")
	require.Contains(t, statusOut, "skalid healthy")

	// Idempotence: a second upgrade has nothing to do.
	repeatUpgrade, code := h.vm("sudo", "/tmp/skali", "cluster", "upgrade", "--yes")
	require.Equal(t, 0, code, repeatUpgrade)
	require.Contains(t, repeatUpgrade, "already current, nothing to do")

	// Registry token protocol phase, driven with curl against the NodePort
	// and skalid's ClusterIP (both reachable from the node without DNS or
	// TLS): anonymous requests are challenged, skalid-minted tokens are
	// verified offline by the registry, and a token cannot push outside
	// the repository it was granted.
	challenge := h.vmOK("curl", "-si", "http://127.0.0.1:30500/v2/")
	require.Contains(t, challenge, "401")
	require.Contains(t, challenge, `realm="https://registry.skali.e2e.test/token"`)
	require.Contains(t, challenge, `service="skali-registry"`)

	skalidIP := strings.TrimSpace(h.vmOK("sudo", "k3s", "kubectl", "get", "svc",
		"-n", "skali-system", "skalid", "-o", "jsonpath={.spec.clusterIP}"))
	loginJSON := h.vmOK("curl", "-s", "-X", "POST",
		"-H", "Content-Type: application/json",
		"-d", `{"email":"admin@skali.e2e.test","password":"e2e-admin-password"}`,
		"http://"+skalidIP+"/v1/auth/login")
	var login struct {
		Session struct {
			Token string `json:"token"`
		} `json:"session"`
	}
	require.NoError(t, json.Unmarshal([]byte(loginJSON), &login), loginJSON)
	require.NotEmpty(t, login.Session.Token, loginJSON)

	mintUserToken := func(scope string) string {
		mintJSON := h.vmOK("curl", "-s", "-u", "admin@skali.e2e.test:"+login.Session.Token,
			"http://"+skalidIP+"/token?service=skali-registry&scope="+scope)
		var minted struct {
			Token string `json:"token"`
		}
		require.NoError(t, json.Unmarshal([]byte(mintJSON), &minted), mintJSON)
		require.NotEmpty(t, minted.Token, mintJSON)
		return minted.Token
	}
	registryStatus := func(token, method, path string) string {
		args := []string{"curl", "-s", "-o", "/dev/null", "-w", "%{http_code}", "-X", method}
		if token != "" {
			args = append(args, "-H", "Authorization: Bearer "+token)
		}
		return h.vmOK(append(args, "http://127.0.0.1:30500"+path)...)
	}

	// The init transcript created no projects yet, so the granted
	// repository rides the cache prefix every member may push to.
	pushToken := mintUserToken("repository:cache/docker.io/library/alpine:push,pull")
	require.Equal(t, "202", registryStatus(pushToken, "POST", "/v2/cache/docker.io/library/alpine/blobs/uploads/"),
		"a skalid-minted token must clear the registry's offline verification")
	require.Equal(t, "401", registryStatus(pushToken, "POST", "/v2/cache/docker.io/library/other/blobs/uploads/"),
		"a token must not push outside its granted repository")
	require.Equal(t, "401", registryStatus("", "POST", "/v2/cache/docker.io/library/alpine/blobs/uploads/"),
		"anonymous pushes must be refused")

	// The node credential from registries.yaml earns pull-only tokens.
	registriesFile := h.vmOK("sudo", "cat", "/etc/rancher/k3s/registries.yaml")
	nodeSecret := ""
	for line := range strings.SplitSeq(registriesFile, "\n") {
		if trimmed := strings.TrimSpace(line); strings.HasPrefix(trimmed, "password:") {
			nodeSecret = strings.Trim(strings.TrimSpace(strings.TrimPrefix(trimmed, "password:")), `"`)
			break
		}
	}
	require.NotEmpty(t, nodeSecret, registriesFile)
	nodeMintJSON := h.vmOK("curl", "-s", "-u", "skali-node:"+nodeSecret,
		"http://"+skalidIP+"/token?service=skali-registry&scope=repository:cache/docker.io/library/alpine:push,pull")
	var nodeMinted struct {
		Token string `json:"token"`
	}
	require.NoError(t, json.Unmarshal([]byte(nodeMintJSON), &nodeMinted), nodeMintJSON)
	require.Equal(t, "401", registryStatus(nodeMinted.Token, "POST", "/v2/cache/docker.io/library/alpine/blobs/uploads/"),
		"the node credential must never earn push access")
	require.Equal(t, "404", registryStatus(nodeMinted.Token, "GET", "/v2/cache/docker.io/library/alpine/manifests/latest"),
		"a pull-scoped node token must pass authorization and hit the absent manifest")

	// Join phase: the agent VM enrolls through the explicit token/join
	// flow, is asserted from the server side, then leaves again so the
	// remaining single-node phases run unchanged.
	agentArch := strings.TrimSpace(h.vmOKOn(e2eAgentVM, "uname", "-m"))
	require.Equal(t, arch, agentArch, "both VMs must share one architecture for one binary")
	h.copyInTo(e2eAgentVM, binary, "/tmp/skali")
	agentIP := h.vmIP(e2eAgentVM)

	// Reachability preflight: a failure here is vzNAT inter-VM traffic
	// being blocked (see build/lima/skali-e2e.yaml), not an installer bug.
	preflight, code := h.vmOn(e2eAgentVM, "curl", "-ksS", "-o", "/dev/null", "--max-time", "15",
		"https://"+serverIP+":6443/ping")
	require.Equal(t, 0, code,
		"agent VM cannot reach the server on the shared vmnet (%s:6443): %s; "+
			"vzNAT inter-VM traffic may be blocked on this host", serverIP, preflight)

	agentStatus, code := h.vmOn(e2eAgentVM, "sudo", "/tmp/skali", "cluster", "status")
	require.Equal(t, 0, code, agentStatus)
	require.Contains(t, agentStatus, "fresh")

	// Mint the join token on the server; the token is the last non-blank
	// output line by contract.
	tokenOut, code := h.vm("sudo", "/tmp/skali", "cluster", "token")
	require.Equal(t, 0, code, tokenOut)
	require.Contains(t, tokenOut, `join command for cluster "e2e"`)
	require.Contains(t, tokenOut, "--role agent")
	var joinToken string
	for line := range strings.SplitSeq(tokenOut, "\n") {
		if trimmed := strings.TrimSpace(line); trimmed != "" {
			joinToken = trimmed
		}
	}
	require.NotEmpty(t, joinToken)
	require.NotContains(t, joinToken, " ", "the token line must be the bare credential")

	tokenFile := h.writeFixtureOn(e2eAgentVM, "join-token", joinToken+"\n")
	h.vmOKOn(e2eAgentVM, "chmod", "600", tokenFile)

	joinOut, code := h.vmOn(e2eAgentVM, "sudo", "/tmp/skali", "cluster", "join",
		"--server", "https://"+serverIP+":6443", "--token-file", tokenFile,
		"--role", "agent", "--capabilities", "database", "--cluster", "e2e",
		"--node-ip", agentIP)
	require.Equal(t, 0, code, joinOut)
	require.Contains(t, joinOut, "(agent)")

	agentRecord := h.vmOKOn(e2eAgentVM, "sudo", "cat", "/var/lib/skali/installation.yaml")
	require.Contains(t, agentRecord, "role: agent")
	require.Contains(t, agentRecord, "cluster: e2e")
	require.Contains(t, agentRecord, "server: https://"+serverIP+":6443")

	// The composite join token carried the cluster's registry pull
	// credential into the agent's containerd config.
	agentRegistries := h.vmOKOn(e2eAgentVM, "sudo", "cat", "/etc/rancher/k3s/registries.yaml")
	require.Contains(t, agentRegistries, nodeSecret,
		"the agent must receive the same pull credential the server minted")

	// Cluster-side truth from the server: the node is Ready and carries
	// exactly the labels the join stamped.
	agentName := strings.TrimSpace(h.vmOKOn(e2eAgentVM, "hostname"))
	h.vmOK("sudo", "k3s", "kubectl", "wait", "--for=condition=Ready",
		"node/"+agentName, "--timeout=180s")
	agentLabels := h.vmOK("sudo", "k3s", "kubectl", "get", "node", agentName,
		"-o", "jsonpath={.metadata.labels}")
	require.Contains(t, agentLabels, "skali.dev/capability-database")
	require.Contains(t, agentLabels, `"skali.dev/cluster":"e2e"`)
	require.NotContains(t, agentLabels, "node-role.kubernetes.io/control-plane")

	statusOut, code = h.vm("sudo", "/tmp/skali", "cluster", "status")
	require.Equal(t, 0, code, statusOut)
	require.Contains(t, statusOut, "2 joined")

	// A repeat join refuses: the agent host is no longer fresh.
	repeatJoin, code := h.vmOn(e2eAgentVM, "sudo", "/tmp/skali", "cluster", "join",
		"--server", "https://"+serverIP+":6443", "--token-file", tokenFile,
		"--role", "agent", "--capabilities", "database", "--cluster", "e2e")
	require.NotEqual(t, 0, code, repeatJoin)

	// The agent host reports itself without the kube API by design.
	agentStatus, code = h.vmOn(e2eAgentVM, "sudo", "/tmp/skali", "cluster", "status")
	require.Equal(t, 0, code, agentStatus)
	require.Contains(t, agentStatus, "Skali agent")

	// Leave again: the agent removes its own host state (the API is not
	// reachable from there, so the single-node guard sees one node), then
	// the lingering node object is deleted from the server.
	agentUninstall, code := h.vmOn(e2eAgentVM, "sudo", "/tmp/skali",
		"cluster", "uninstall", "--scope", "node", "--confirm", "e2e")
	require.Equal(t, 0, code, agentUninstall)
	_, code = h.vmOn(e2eAgentVM, "test", "-e", "/usr/local/bin/k3s")
	require.NotEqual(t, 0, code, "k3s must be gone from the agent VM")
	agentStatus, code = h.vmOn(e2eAgentVM, "sudo", "/tmp/skali", "cluster", "status")
	require.Equal(t, 0, code, agentStatus)
	require.Contains(t, agentStatus, "fresh")

	h.vmOK("sudo", "k3s", "kubectl", "delete", "node", agentName)
	nodeCount := strings.TrimSpace(h.vmOK("sh", "-c",
		"sudo k3s kubectl get nodes --no-headers | wc -l"))
	require.Equal(t, "1", nodeCount)

	// Repeat bare execution with closed stdin performs no mutation.
	before := h.vmOK("sudo", "k3s", "kubectl", "get", "deploy", "-n", "skali-system",
		"-o", "jsonpath={range .items[*]}{.metadata.name}={.metadata.resourceVersion} {end}")
	repeatRun, code := h.vm("sh", "-c", "sudo /tmp/skali cluster </dev/null")
	require.Equal(t, 0, code, repeatRun)
	after := h.vmOK("sudo", "k3s", "kubectl", "get", "deploy", "-n", "skali-system",
		"-o", "jsonpath={range .items[*]}{.metadata.name}={.metadata.resourceVersion} {end}")
	require.Equal(t, before, after, "read-only detection must not mutate the cluster")

	// Scoped uninstall: bundle first (bare k3s survives), then the node.
	bundleOut, code := h.vm("sudo", "/tmp/skali", "cluster", "uninstall", "--scope", "bundle", "--confirm", "e2e")
	require.Equal(t, 0, code, bundleOut)
	_, code = h.vm("sudo", "k3s", "kubectl", "get", "namespace", "skali-system")
	require.NotEqual(t, 0, code, "skali-system must be gone after the bundle uninstall")
	active := strings.TrimSpace(h.vmOK("systemctl", "is-active", "k3s"))
	require.Equal(t, "active", active, "bare k3s must keep running")

	nodeOut, code := h.vm("sudo", "/tmp/skali", "cluster", "uninstall", "--scope", "node", "--confirm", "e2e")
	require.Equal(t, 0, code, nodeOut)
	_, code = h.vm("test", "-e", "/usr/local/bin/k3s")
	require.NotEqual(t, 0, code, "k3s must be uninstalled")
	_, code = h.vm("sudo", "test", "-e", "/var/lib/skali")
	require.NotEqual(t, 0, code, "/var/lib/skali must be gone")

	// The host detects as fresh again.
	statusOut, code = h.vm("sudo", "/tmp/skali", "cluster", "status")
	require.Equal(t, 0, code, statusOut)
	require.Contains(t, statusOut, "fresh")
}

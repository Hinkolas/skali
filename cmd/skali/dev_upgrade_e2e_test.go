package main

import (
	"encoding/json"
	"fmt"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	"github.com/Hinkolas/skali/internal/localdev"
)

// e2eOldK3sImage is the k3s one minor below the pin the upgrade starts
// from; bump it along with localdev.K3sImage.
const e2eOldK3sImage = "rancher/k3s:v1.35.9-k3s1"

// TestDevUpgradeEndToEnd upgrades a populated local platform across a
// release and a k3s minor version in place: two stamped CLI/skalid pairs
// built from the working tree, the first on the older k3s. The guestbook
// holds database rows, bucket objects, and a volume file; all of it, the
// platform's own state and credentials, and the development CA must come
// through an interrupted upgrade, its resumption, and a repeat.
func TestDevUpgradeEndToEnd(t *testing.T) {
	h := newE2EHarnessFor(t, "guestbook", "guestbook.localhost")
	repo, err := filepath.Abs("../..")
	require.NoError(t, err)
	versions := []string{"v0.1.0-rc.3", "v0.1.0-rc.4"}
	binaries := make([]string, len(versions))
	for i, release := range versions {
		binaries[i] = filepath.Join(t.TempDir(), "skali")
		build := exec.Command("go", "build", "-ldflags", "-X github.com/Hinkolas/skali/internal/version.Version="+release, "-o", binaries[i], "./cmd/skali")
		build.Dir = repo
		out, err := build.CombinedOutput()
		require.NoError(t, err, "%s", out)
		image := exec.Command("docker", "build", "--build-arg", "VERSION="+release, "-t", "ghcr.io/hinkolas/skalid:"+release, "-f", filepath.Join(repo, "build/skalid.Dockerfile"), repo)
		out, err = image.CombinedOutput()
		require.NoError(t, err, "%s", out)
	}
	stateDir := filepath.Join(h.stateDir(), "skali", "dev", e2eCluster)
	record := func() localdev.State {
		raw, err := os.ReadFile(filepath.Join(stateDir, "state.json"))
		require.NoError(t, err)
		var state localdev.State
		require.NoError(t, json.Unmarshal(raw, &state))
		return state
	}
	baseEnv := h.env
	oldK3s := append(append([]string{}, baseEnv...), "SKALI_DEV_K3S_IMAGE="+e2eOldK3sImage)

	// The installed platform: the first release on the older k3s, with the
	// guestbook seeded with all three data kinds.
	h.binary, h.env = binaries[0], oldK3s
	out := h.run(false, "", "dev", "-d", "--offline")
	require.Contains(t, out, e2eOldK3sImage)
	h.waitRoute("visits: ", 10*time.Minute)
	status, body := h.request(http.MethodPut, "/notes/e2e", "bucket note survives the upgrade")
	require.Equal(t, http.StatusOK, status, "store note: %s", body)
	status, body = h.request(http.MethodPut, "/disk/e2e", "disk note survives the upgrade")
	require.Equal(t, http.StatusOK, status, "store disk file: %s", body)
	visits := func() int {
		_, body := h.route("/")
		var count int
		_, err := fmt.Sscanf(body, "visits: %d", &count)
		require.NoError(t, err, "unexpected body %q", body)
		return count
	}
	before := visits()
	caBefore, err := os.ReadFile(filepath.Join(stateDir, "ca.crt"))
	require.NoError(t, err)
	keyBefore, err := os.ReadFile(filepath.Join(stateDir, "ca.key"))
	require.NoError(t, err)
	installed := record()
	require.Equal(t, versions[0], installed.Version)
	volumes := nodeVolumes(t)
	require.Contains(t, volumes, "/var/lib/rancher/k3s")

	// The newer release refuses the platform and names the upgrade; a
	// declined upgrade shows the plan and changes nothing.
	h.binary, h.env = binaries[1], baseEnv
	out = h.run(true, "", "dev", "start", "--offline")
	require.Contains(t, out, "run skali dev upgrade")
	out = h.run(true, "n\n", "dev", "upgrade", "--offline")
	for _, line := range []string{
		"release       " + versions[0] + " -> " + versions[1],
		"k3s           " + e2eOldK3sImage + " -> " + localdev.K3sImage,
		"migrations    none (platform database at schema",
		"interruption  the node container is replaced on the new k3s",
		"skalid restarts",
		"afterwards    skali releases older than " + versions[1],
		"aborted; the local platform is unchanged",
	} {
		require.Contains(t, out, line)
	}
	require.Equal(t, installed.Version, record().Version)
	require.Nil(t, record().Upgrade)

	// Interrupted after the node moved to the new k3s, during the
	// converge: the record keeps the unfinished upgrade and every other
	// command (either release's) refuses the platform until it finishes.
	out = h.runInterrupted("Apply blessed operators", 10*time.Minute, "dev", "upgrade", "--offline", "--yes")
	require.Contains(t, out, "the upgrade to "+versions[1]+" did not finish")
	interrupted := record()
	require.NotNil(t, interrupted.Upgrade)
	require.Equal(t, versions[0], interrupted.Upgrade.FromVersion)
	require.Equal(t, localdev.K3sImage, interrupted.K3sImage)
	require.Contains(t, h.run(true, "", "dev", "start", "--offline"), "did not finish")
	require.Contains(t, h.run(false, "", "dev", "status"), "unfinished")
	h.binary, h.env = binaries[0], oldK3s
	h.run(true, "", "dev", "start", "--offline")

	// The retry resumes and finishes; a repeat finds nothing to do.
	h.binary, h.env = binaries[1], baseEnv
	out = h.run(false, "", "dev", "upgrade", "--offline", "--yes")
	require.Contains(t, out, "resumes")
	require.Contains(t, out, "upgraded to "+versions[1])
	require.Contains(t, out, "unchanged; local and remote trust stay valid")
	require.Contains(t, h.run(false, "", "dev", "upgrade", "--offline", "--yes"), "nothing to upgrade")

	upgraded := record()
	require.Nil(t, upgraded.Upgrade)
	require.Equal(t, versions[1], upgraded.Version)
	require.Equal(t, "ghcr.io/hinkolas/skalid:"+versions[1], upgraded.SkalidImage)
	require.Equal(t, localdev.K3sImage, upgraded.K3sImage)
	require.Equal(t, installed.AdminPassword, upgraded.AdminPassword)
	require.Equal(t, installed.AuthSecret, upgraded.AuthSecret)
	require.True(t, installed.CreatedAt.Equal(upgraded.CreatedAt))
	caAfter, err := os.ReadFile(filepath.Join(stateDir, "ca.crt"))
	require.NoError(t, err)
	keyAfter, err := os.ReadFile(filepath.Join(stateDir, "ca.key"))
	require.NoError(t, err)
	require.Equal(t, caBefore, caAfter, "the development CA certificate is kept")
	require.Equal(t, keyBefore, keyAfter, "the development CA key is kept")
	require.NoDirExists(t, filepath.Join(stateDir, "node-upgrade"))
	require.Equal(t, volumes, nodeVolumes(t), "the node mounts the very same volumes")
	image, err := exec.Command("docker", "inspect", "--format", "{{.Config.Image}}", e2eCluster).Output()
	require.NoError(t, err)
	require.Equal(t, localdev.K3sImage, strings.TrimSpace(string(image)))

	// The project came through without a redeploy: its route answers over
	// the kept CA with the earlier rows, the bucket object, and the file.
	h.waitRoute("visits: ", 6*time.Minute)
	require.Greater(t, visits(), before, "the visit history survives the upgrade")
	require.Eventually(t, func() bool {
		status, body := h.route("/notes/e2e")
		return status == http.StatusOK && body == "bucket note survives the upgrade"
	}, 2*time.Minute, 3*time.Second, "the bucket object survives the upgrade")
	_, body = h.route("/disk/e2e")
	require.Equal(t, "disk note survives the upgrade", body, "the volume file survives the upgrade")
	out = h.run(false, "", "dev", "status")
	require.Contains(t, out, versions[1])
	require.Contains(t, out, "database.data")
	require.Contains(t, h.run(false, "", "dev", "ls"), "guestbook")
	require.Contains(t, h.run(false, "", "dev", "-d", "--offline"), "ready")

	// Reset still destroys everything, the volumes the replacement node
	// mounted by name included.
	h.run(false, "", "dev", "reset", "--yes")
	for name := range volumes {
		require.Error(t, exec.Command("docker", "volume", "inspect", volumes[name]).Run(), "volume %s for %s outlived the reset", volumes[name], name)
	}
}

// nodeVolumes maps the node container's mount destinations to their
// volume names.
func nodeVolumes(t *testing.T) map[string]string {
	t.Helper()
	out, err := exec.Command("docker", "inspect", "--format",
		`{{range .Mounts}}{{if eq .Type "volume"}}{{.Destination}}={{.Name}}{{"\n"}}{{end}}{{end}}`, e2eCluster).Output()
	require.NoError(t, err)
	volumes := map[string]string{}
	for _, line := range strings.Fields(string(out)) {
		destination, name, _ := strings.Cut(line, "=")
		volumes[destination] = name
	}
	return volumes
}

// runInterrupted starts the CLI, sends SIGINT once its output contains
// marker, and requires the command to fail; it returns the output.
func (h *e2eHarness) runInterrupted(marker string, wait time.Duration, args ...string) string {
	h.t.Helper()
	command := exec.Command(h.binary, args...)
	command.Dir = h.projectDir
	command.Env = h.env
	output := &syncBuffer{}
	command.Stdout = output
	command.Stderr = output
	require.NoError(h.t, command.Start())
	require.Eventually(h.t, func() bool {
		return strings.Contains(output.String(), marker)
	}, wait, 200*time.Millisecond, "output never contained the marker:\n%s", output.String())
	require.NoError(h.t, command.Process.Signal(os.Interrupt))
	require.Error(h.t, command.Wait(), "the interrupted command must fail:\n%s", output.String())
	return output.String()
}

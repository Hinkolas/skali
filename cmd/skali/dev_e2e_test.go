package main

import (
	"bytes"
	"fmt"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
)

// The end-to-end suite drives the built skali binary through the real
// paved path against a throwaway local installation (its own cluster name,
// ports, and state directories), so a developer's actual skali-dev
// platform is never touched. Gated: it creates and destroys a k3d cluster
// and takes minutes.
//
//	TEST_SKALI_DEV=1 go test -timeout 40m -count=1 -run TestDev ./cmd/skali/...

const (
	e2eCluster      = "skali-dev-e2e"
	e2eHTTPPort     = 8082
	e2eRegistryPort = 5512
)

type e2eHarness struct {
	t          *testing.T
	binary     string
	projectDir string
	env        []string
}

func newE2EHarness(t *testing.T) *e2eHarness {
	t.Helper()
	if os.Getenv("TEST_SKALI_DEV") == "" {
		t.Skip("set TEST_SKALI_DEV=1 to run the skali dev end-to-end suite")
	}
	repoRoot, err := filepath.Abs("../..")
	require.NoError(t, err)

	stateHome := t.TempDir()
	configHome := t.TempDir()
	binary := filepath.Join(t.TempDir(), "skali")
	build := exec.Command("go", "build", "-o", binary, "./cmd/skali")
	build.Dir = repoRoot
	out, err := build.CombinedOutput()
	require.NoError(t, err, "build skali: %s", out)

	// The scratch project lives outside the repository, so the CLI's
	// working-tree image fallback cannot trigger; build the control-plane
	// image here and pass it explicitly on the first run.
	image := exec.Command("docker", "build", "-t", "skalid:dev",
		"-f", filepath.Join(repoRoot, "build", "skalid.Dockerfile"), repoRoot)
	out, err = image.CombinedOutput()
	require.NoError(t, err, "build skalid image: %s", out)

	// The example project in a scratch copy so source edits are safe.
	projectDir := filepath.Join(t.TempDir(), "hello-world")
	require.NoError(t, exec.Command("cp", "-R",
		filepath.Join(repoRoot, "examples", "hello-world"), projectDir).Run())
	require.NoError(t, os.WriteFile(filepath.Join(projectDir, ".env"),
		[]byte("APP_DOMAIN=hello-world.localhost\n"), 0o644))

	harness := &e2eHarness{
		t:          t,
		binary:     binary,
		projectDir: projectDir,
		env: append(os.Environ(),
			"SKALI_DEV_CLUSTER="+e2eCluster,
			fmt.Sprintf("SKALI_DEV_HTTP_PORT=%d", e2eHTTPPort),
			fmt.Sprintf("SKALI_DEV_REGISTRY_PORT=%d", e2eRegistryPort),
			"XDG_STATE_HOME="+stateHome,
			"XDG_CONFIG_HOME="+configHome,
		),
	}
	t.Cleanup(func() {
		_ = exec.Command("k3d", "cluster", "delete", e2eCluster).Run()
	})
	return harness
}

// run executes the CLI in the project directory and returns its combined
// output; fatal on unexpected exit codes unless wantErr.
func (h *e2eHarness) run(wantErr bool, stdin string, args ...string) string {
	h.t.Helper()
	command := exec.Command(h.binary, args...)
	command.Dir = h.projectDir
	command.Env = h.env
	if stdin != "" {
		command.Stdin = strings.NewReader(stdin)
	}
	out, err := command.CombinedOutput()
	if wantErr {
		require.Error(h.t, err, "expected failure; output:\n%s", out)
	} else {
		require.NoError(h.t, err, "skali %s failed:\n%s", strings.Join(args, " "), out)
	}
	return string(out)
}

// syncBuffer guards concurrent writes from the child's pipe copiers against
// the test's polling reads.
type syncBuffer struct {
	mu  sync.Mutex
	buf bytes.Buffer
}

func (b *syncBuffer) Write(p []byte) (int, error) {
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.buf.Write(p)
}

func (b *syncBuffer) String() string {
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.buf.String()
}

// runInterrupt starts the CLI, waits until its output contains marker, sends
// SIGINT, and requires a clean exit: the compose-like detach contract.
func (h *e2eHarness) runInterrupt(marker string, wait time.Duration, args ...string) string {
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
	}, wait, 200*time.Millisecond, "output never contained the marker")
	require.NoError(h.t, command.Process.Signal(os.Interrupt))
	err := command.Wait()
	require.NoError(h.t, err, "skali %s after interrupt:\n%s", strings.Join(args, " "), output.String())
	return output.String()
}

// route fetches the deployed application through the local edge.
func (h *e2eHarness) route(path string) (int, string) {
	h.t.Helper()
	request, err := http.NewRequest(http.MethodGet,
		fmt.Sprintf("http://127.0.0.1:%d%s", e2eHTTPPort, path), nil)
	require.NoError(h.t, err)
	request.Host = "hello-world.localhost"
	client := &http.Client{Timeout: 5 * time.Second}
	response, err := client.Do(request)
	if err != nil {
		return 0, ""
	}
	defer response.Body.Close()
	body := make([]byte, 4096)
	read, _ := response.Body.Read(body)
	return response.StatusCode, string(body[:read])
}

func (h *e2eHarness) waitRoute(contains string, timeout time.Duration) {
	h.t.Helper()
	require.Eventually(h.t, func() bool {
		status, body := h.route("/")
		return status == http.StatusOK && strings.Contains(body, contains)
	}, timeout, 2*time.Second, "route never served %q", contains)
}

// TestDevEndToEnd walks the R3 exit criteria on one throwaway
// installation: fresh-machine paved path to a healthy route, unchanged
// repeat reusing the artifact, skalid restart during a rollout resuming
// toward the same revision, stop/start retaining state, and the explicit
// destructive reset.
func TestDevEndToEnd(t *testing.T) {
	h := newE2EHarness(t)

	t.Run("FirstRunReachesHealthyRoute", func(t *testing.T) {
		out := h.run(false, "", "dev", "-d", "--skalid-image", "skalid:dev")
		require.Contains(t, out, "Create k3d cluster "+e2eCluster)
		require.Contains(t, out, "run ")
		require.Contains(t, out, "ready")
		h.waitRoute("hello from skali", 2*time.Minute)
	})

	t.Run("RepeatUnchangedReusesArtifact", func(t *testing.T) {
		out := h.run(false, "", "dev", "-d")
		require.Contains(t, out, "nothing to deploy")
		require.NotContains(t, out, "Build locally")
	})

	t.Run("DevStatusShowsHealth", func(t *testing.T) {
		out := h.run(false, "", "dev", "status")
		require.Contains(t, out, "running")
		require.Contains(t, out, "application.web")
		require.Contains(t, out, "healthy")
	})

	t.Run("FollowLogsAndDetach", func(t *testing.T) {
		// Bare dev on an up-to-date project attaches to the runtime logs;
		// Ctrl-C detaches cleanly and the project keeps serving.
		out := h.runInterrupt("following logs", 2*time.Minute, "dev")
		require.Contains(t, out, "detached; the project keeps running")
		status, _ := h.route("/")
		require.Equal(t, http.StatusOK, status, "detaching must not stop the project")
	})

	t.Run("DownKeepsData", func(t *testing.T) {
		out := h.run(false, "", "dev", "down")
		require.Contains(t, out, "is down; its data is retained")

		require.Eventually(t, func() bool {
			status, _ := h.route("/")
			return status != http.StatusOK
		}, 2*time.Minute, 2*time.Second, "the route must stop serving after down")

		// The namespace with its data survives, and ls reports the state.
		kubeconfig := filepath.Join(h.stateDir(), "skali", "kubeconfig")
		require.NoError(t, exec.Command("kubectl", "--kubeconfig", kubeconfig,
			"get", "namespace", "skali-hello-world-local").Run())
		out = h.run(false, "", "dev", "ls")
		require.Contains(t, out, "hello-world")
		require.Contains(t, out, "down")

		// The next dev resurrects the project into the kept namespace.
		out = h.run(false, "", "dev", "-d")
		require.Contains(t, out, "ready")
		h.waitRoute("hello from skali", 2*time.Minute)
	})

	t.Run("SkalidRestartDuringRolloutResumes", func(t *testing.T) {
		source := filepath.Join(h.projectDir, "main.go")
		content, err := os.ReadFile(source)
		require.NoError(t, err)
		require.NoError(t, os.WriteFile(source,
			[]byte(strings.Replace(string(content), "hello from skali", "hello again from skali", 1)), 0o644))

		out := h.run(false, "", "deploy", "--environment", "local",
			"--use-remote-env", "--yes", "--detach")
		require.Contains(t, out, "deployment continues on the server")

		// Kill the control plane while the rollout is in flight; the
		// restarted skalid must resume toward the same revision.
		kubeconfig := filepath.Join(h.stateDir(), "skali", "kubeconfig")
		require.NoError(t, exec.Command("kubectl", "--kubeconfig", kubeconfig,
			"delete", "pod", "-n", "skali-system",
			"-l", "app.kubernetes.io/name=skalid", "--wait=false").Run())

		h.waitRoute("hello again from skali", 5*time.Minute)
	})

	t.Run("StopAndStartRetainsState", func(t *testing.T) {
		out := h.run(false, "", "dev", "stop")
		require.Contains(t, out, "state is retained")
		out = h.run(false, "", "dev", "up")
		require.Contains(t, out, "state retained")
		h.waitRoute("hello again from skali", 3*time.Minute)
	})

	t.Run("PurgeRemovesEverything", func(t *testing.T) {
		// Refused without the typed project name.
		h.run(true, "no\n", "dev", "down", "--purge")

		out := h.run(false, "hello-world\n", "dev", "down", "--purge")
		require.Contains(t, out, "is purged from the local platform")

		kubeconfig := filepath.Join(h.stateDir(), "skali", "kubeconfig")
		require.Error(t, exec.Command("kubectl", "--kubeconfig", kubeconfig,
			"get", "namespace", "skali-hello-world-local").Run(),
			"the purge must delete the namespace")
		out = h.run(false, "", "dev", "ls")
		require.NotContains(t, out, "local", "the purged environment must not be listed")
	})

	t.Run("ResetConfirmsAndRemoves", func(t *testing.T) {
		// Refused without the typed confirmation.
		h.run(true, "no\n", "dev", "reset")

		out := h.run(false, "destroy\n", "dev", "reset")
		require.Contains(t, out, "Delete cluster "+e2eCluster)
		require.Contains(t, out, "Remove local installation record")

		clusters, err := exec.Command("k3d", "cluster", "list", "-o", "json").Output()
		require.NoError(t, err)
		require.NotContains(t, string(clusters), e2eCluster)
	})
}

func (h *e2eHarness) stateDir() string {
	for _, entry := range h.env {
		if value, found := strings.CutPrefix(entry, "XDG_STATE_HOME="); found {
			return value
		}
	}
	return ""
}

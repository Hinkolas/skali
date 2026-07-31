package main

import (
	"bytes"
	"fmt"
	"io"
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
	host       string
	env        []string
}

func newE2EHarness(t *testing.T) *e2eHarness {
	t.Helper()
	return newE2EHarnessFor(t, "hello-world", "hello-world.localhost")
}

func newE2EHarnessFor(t *testing.T, example, host string) *e2eHarness {
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

	// The example project in a scratch copy so source edits are safe. A
	// checkout binding left behind by real deploys of the example must not
	// travel along: the suite asserts bare dev never creates one.
	projectDir := filepath.Join(t.TempDir(), example)
	require.NoError(t, exec.Command("cp", "-R",
		filepath.Join(repoRoot, "examples", example), projectDir).Run())
	require.NoError(t, os.RemoveAll(filepath.Join(projectDir, ".skali")))
	require.NoError(t, os.WriteFile(filepath.Join(projectDir, ".env"),
		[]byte("APP_DOMAIN="+host+"\n"), 0o644))

	harness := &e2eHarness{
		t:          t,
		binary:     binary,
		projectDir: projectDir,
		host:       host,
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
// SIGINT, and requires a clean exit AFTER the session epilogue (the
// compose-like pause-on-exit contract).
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
	return h.request(http.MethodGet, path, "")
}

func (h *e2eHarness) request(method, path, body string) (int, string) {
	h.t.Helper()
	var reader io.Reader
	if body != "" {
		reader = strings.NewReader(body)
	}
	request, err := http.NewRequest(method,
		fmt.Sprintf("http://127.0.0.1:%d%s", e2eHTTPPort, path), reader)
	require.NoError(h.t, err)
	request.Host = h.host
	client := &http.Client{Timeout: 10 * time.Second}
	response, err := client.Do(request)
	if err != nil {
		return 0, ""
	}
	defer response.Body.Close()
	buffer := make([]byte, 4096)
	read, _ := response.Body.Read(buffer)
	return response.StatusCode, string(buffer[:read])
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
		// Every public platform image lands via the host cache and one
		// batched import; nothing pulls from the internet mid-deploy.
		require.Contains(t, out, "Pull platform images")
		require.Contains(t, out, "run ")
		require.Contains(t, out, "ready")
		h.waitRoute("hello from skali", 2*time.Minute)
		// dev never writes the checkout binding.
		require.NoDirExists(t, filepath.Join(h.projectDir, ".skali"))
	})

	t.Run("RepeatUnchangedReusesArtifact", func(t *testing.T) {
		out := h.run(false, "", "dev", "-d")
		require.Contains(t, out, "nothing to deploy")
		require.NotContains(t, out, "Build locally")
		// The control-plane image is unchanged, so the repeat run must not
		// pay for a k3d import again, and the recorded platform images must
		// not re-probe or re-pull.
		require.Contains(t, out, "unchanged since last import")
		require.NotContains(t, out, "Pull platform images")
	})

	t.Run("ForceRedeploysUnchanged", func(t *testing.T) {
		out := h.run(false, "", "dev", "-d", "--force")
		require.Contains(t, out, "nothing changed; deploying anyway")
		require.NotContains(t, out, "nothing to deploy")
		require.Contains(t, out, "ready")
		// The restart stamp reached the cluster: the pod template carries
		// the annotation, so the workload rolled to fresh pods.
		kubeconfig := filepath.Join(h.stateDir(), "skali", "kubeconfig")
		workloads, err := exec.Command("kubectl", "--kubeconfig", kubeconfig,
			"get", "deployment", "-A", "-l", "skali.dev/managed=true", "-o", "yaml").CombinedOutput()
		require.NoError(t, err, string(workloads))
		require.Contains(t, string(workloads), "skali.dev/restarted-at")
		// Wait for the roll to fully settle (surge pod promoted, old pod
		// gone) before handing off: the next subtest's single-shot route
		// check must not land in the traffic switchover window.
		names, err := exec.Command("kubectl", "--kubeconfig", kubeconfig,
			"get", "deployment", "-A", "-l", "skali.dev/managed=true",
			"-o", `jsonpath={range .items[*]}{.metadata.namespace} {.metadata.name}{"\n"}{end}`).CombinedOutput()
		require.NoError(t, err, string(names))
		for line := range strings.SplitSeq(strings.TrimSpace(string(names)), "\n") {
			parts := strings.Fields(line)
			if len(parts) != 2 {
				continue
			}
			status, err := exec.Command("kubectl", "--kubeconfig", kubeconfig,
				"rollout", "status", "deployment", parts[1], "-n", parts[0], "--timeout=120s").CombinedOutput()
			require.NoError(t, err, string(status))
		}
		h.waitRoute("hello from skali", 2*time.Minute)
	})

	t.Run("DevStatusShowsHealth", func(t *testing.T) {
		out := h.run(false, "", "dev", "status")
		require.Contains(t, out, "running")
		require.Contains(t, out, "application.web")
		require.Contains(t, out, "healthy")
	})

	t.Run("AttachedExitPausesProject", func(t *testing.T) {
		// Bare dev on an up-to-date project attaches to the runtime logs;
		// ending the session pauses the project (compose semantics): the
		// route stops serving, the data survives, and the next dev brings
		// it back.
		out := h.runInterrupt("following logs", 2*time.Minute, "dev")
		require.Contains(t, out, "is paused; its data is retained")
		require.Eventually(t, func() bool {
			status, _ := h.route("/")
			return status != http.StatusOK
		}, 2*time.Minute, 2*time.Second, "the route must stop serving after the session ends")
		out = h.run(false, "", "dev", "ls")
		require.Contains(t, out, "down")

		// A background project (-d) is not paused by its session ending.
		out = h.run(false, "", "dev", "-d")
		require.Contains(t, out, "ready")
		h.waitRoute("hello from skali", 2*time.Minute)
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
			"--yes", "--detach")
		require.Contains(t, out, "deployment continues on the server")
		// Deploys against the dev-owned local remote are never bound.
		require.NoDirExists(t, filepath.Join(h.projectDir, ".skali"))

		// Kill the control plane while the rollout is in flight; the
		// restarted skalid must resume toward the same revision.
		kubeconfig := filepath.Join(h.stateDir(), "skali", "kubeconfig")
		require.NoError(t, exec.Command("kubectl", "--kubeconfig", kubeconfig,
			"delete", "pod", "-n", "skali-system",
			"-l", "app.kubernetes.io/name=skalid", "--wait=false").Run())

		h.waitRoute("hello again from skali", 5*time.Minute)
	})

	t.Run("InterruptDuringBuildClosesWindow", func(t *testing.T) {
		// A session signaled mid-build must fail its open artifact window
		// on the way out: an immediate explicit down must not be refused
		// with an in-flight deployment (the stale-build sweeper would
		// otherwise hold the window for BUILD_STALE_TIMEOUT).
		source := filepath.Join(h.projectDir, "main.go")
		content, err := os.ReadFile(source)
		require.NoError(t, err)
		require.NoError(t, os.WriteFile(source,
			[]byte(strings.Replace(string(content), "hello again from skali", "hello three from skali", 1)), 0o644))

		out := h.runInterrupt("Build ", 3*time.Minute, "dev")
		require.Contains(t, out, "is paused; its data is retained")
		out = h.run(false, "", "dev", "down")
		require.NotContains(t, out, "in flight",
			"the interrupted window must be closed before the pause")

		// Restore the previous source and bring the project back so the
		// following subtests see the hello-again revision.
		require.NoError(t, os.WriteFile(source, content, 0o644))
		out = h.run(false, "", "dev", "-d")
		require.Contains(t, out, "ready")
		h.waitRoute("hello again from skali", 5*time.Minute)
	})

	t.Run("StopAndStartRetainsState", func(t *testing.T) {
		out := h.run(false, "", "dev", "stop")
		require.Contains(t, out, "state is retained")
		// A stop/start cycle must hit the fast path: no re-import (the node
		// volumes keep containerd's imported image) and no bundle converge,
		// just the restart plus the edge probe window.
		out = h.run(false, "", "dev", "start")
		require.Contains(t, out, "state retained")
		require.Contains(t, out, "unchanged since last import")
		require.Contains(t, out, "unchanged since last converge")
		require.NotContains(t, out, "Bootstrap database",
			"a restart must not pay the full converge")
		h.waitRoute("hello again from skali", 3*time.Minute)

		// dev up stays the explicit full converge.
		out = h.run(false, "", "dev", "up")
		require.Contains(t, out, "Bootstrap database")
	})

	t.Run("PurgeRemovesEverything", func(t *testing.T) {
		// Refused unless explicitly confirmed; No is the default.
		h.run(true, "\n", "dev", "down", "--purge")

		out := h.run(false, "y\n", "dev", "down", "--purge")
		require.Contains(t, out, "is purged from the local platform")

		kubeconfig := filepath.Join(h.stateDir(), "skali", "kubeconfig")
		require.Error(t, exec.Command("kubectl", "--kubeconfig", kubeconfig,
			"get", "namespace", "skali-hello-world-local").Run(),
			"the purge must delete the namespace")
		out = h.run(false, "", "dev", "ls")
		require.NotContains(t, out, "local", "the purged environment must not be listed")
	})

	t.Run("ResetConfirmsAndRemoves", func(t *testing.T) {
		// Refused unless explicitly confirmed; No is the default.
		h.run(true, "\n", "dev", "reset")

		out := h.run(false, "y\n", "dev", "reset")
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

// TestDevGuestbookDatabase is the R5+R6 prototype path on local dev: a
// project bearing a database and a bucket deploys, the application waits
// for both claims, starts with the injected connection outputs, reads its
// own database writes through the shared dev pool, and stores and reads
// objects through the dev object store.
func TestDevGuestbookDatabase(t *testing.T) {
	h := newE2EHarnessFor(t, "guestbook", "guestbook.localhost")

	out := h.run(false, "", "dev", "-d", "--skalid-image", "skalid:dev")
	require.Contains(t, out, "ready")
	// First use brings up the dev pool and the object store (postgres and
	// seaweed image pulls) before the application can pass readiness.
	h.waitRoute("visits: ", 10*time.Minute)
	_, first := h.route("/")
	_, second := h.route("/")
	require.NotEqual(t, first, second, "every visit must insert a row")

	// Objects flow through the injected {{buckets.files.*}} outputs.
	status, body := h.request(http.MethodPut, "/notes/e2e", "stored through skali buckets")
	require.Equal(t, http.StatusOK, status, "store note: %s", body)
	status, body = h.route("/notes/e2e")
	require.Equal(t, http.StatusOK, status)
	require.Equal(t, "stored through skali buckets", body)

	out = h.run(false, "", "dev", "status")
	require.Contains(t, out, "application.web")
	require.Contains(t, out, "database.data")
	require.Contains(t, out, "bucket.files")
	require.Contains(t, out, "healthy")

	var before int
	_, body = h.route("/")
	_, err := fmt.Sscanf(body, "visits: %d", &before)
	require.NoError(t, err, "unexpected body %q", body)

	// Down keeps the data AND the substrate: the project's workloads leave,
	// but the platform postgres and object store keep running (the always-on
	// dev substrate), so the next dev is a fast re-apply, not a cold start.
	out = h.run(false, "", "dev", "down")
	require.Contains(t, out, "is down; its data is retained")
	kubeconfig := filepath.Join(h.stateDir(), "skali", "kubeconfig")
	platformRunning := func() bool {
		pods, err := exec.Command("kubectl", "--kubeconfig", kubeconfig,
			"get", "pods", "-n", "skali-platform", "--no-headers").CombinedOutput()
		return err == nil && strings.Contains(string(pods), "Running")
	}
	require.True(t, platformRunning(), "the substrate must keep running after down")
	for range 10 {
		time.Sleep(3 * time.Second)
		require.True(t, platformRunning(), "the substrate must never quiesce after down")
	}

	// The next dev re-applies onto the warm substrate and both data planes
	// survived.
	out = h.run(false, "", "dev", "-d")
	require.Contains(t, out, "ready")
	h.waitRoute("visits: ", 4*time.Minute)
	var after int
	_, body = h.route("/")
	_, err = fmt.Sscanf(body, "visits: %d", &after)
	require.NoError(t, err, "unexpected body %q", body)
	require.Greater(t, after, before, "the visit history must survive down")
	require.Eventually(t, func() bool {
		status, body := h.route("/notes/e2e")
		return status == http.StatusOK && body == "stored through skali buckets"
	}, 2*time.Minute, 3*time.Second, "the stored note must survive down")
}

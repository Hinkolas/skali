package main

import (
	"bytes"
	"fmt"
	"io"
	"net"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/minio/minio-go/v7"
	miniocredentials "github.com/minio/minio-go/v7/pkg/credentials"
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
	// e2eLoopbackBase shifts the loopback service range away from a real
	// skali-dev installation's identity mapping (30501..30510).
	e2eLoopbackBase = 45001
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
			fmt.Sprintf("SKALI_DEV_LOOPBACK_PORT_BASE=%d", e2eLoopbackBase),
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

	t.Run("DevWhileRolloutInFlightAttaches", func(t *testing.T) {
		// A dev session started while a rollout is already in flight must
		// adopt the running deployment instead of failing with
		// deployment_in_flight: no prompt, no plan, straight to the rollout
		// tree and then the runtime logs. The slow-start source keeps pods
		// unready for the sleep window, so the run is reliably still
		// running when dev looks.
		source := filepath.Join(h.projectDir, "main.go")
		content, err := os.ReadFile(source)
		require.NoError(t, err)
		slow := strings.Replace(string(content), "\"os\"\n)", "\"os\"\n\t\"time\"\n)", 1)
		slow = strings.Replace(slow, "log.Println(\"listening on :8080\")",
			"time.Sleep(20 * time.Second)\n\tlog.Println(\"listening on :8080\")", 1)
		require.NotEqual(t, string(content), slow, "slow-start edit did not apply")
		require.NoError(t, os.WriteFile(source, []byte(slow), 0o644))

		out := h.run(false, "", "deploy", "--remote", "local", "--environment", "local", "--yes", "--detach")
		require.Contains(t, out, "deployment continues on the server")

		out = h.runInterrupt("following logs", 8*time.Minute, "dev")
		require.Contains(t, out, "a deployment is already in flight; attaching to run")
		require.Contains(t, out, "ready")
		require.NotContains(t, out, "deployment_in_flight")
		require.NotContains(t, out, "plan for")
		require.Contains(t, out, "is paused; its data is retained")
	})

	t.Run("DevForceCancelsInFlightRun", func(t *testing.T) {
		// --force takes the slot instead of adopting it: the in-flight run
		// is cancelled and the fresh forced deploy proceeds to ready.
		out := h.run(false, "", "deploy", "--remote", "local", "--environment", "local", "--yes", "--detach")
		require.Contains(t, out, "deployment continues on the server")

		out = h.run(false, "", "dev", "-d", "--force")
		require.Contains(t, out, "cancelling in-flight deployment run")
		require.Contains(t, out, "ready")

		// Restore the fast source and settle back on it so the following
		// subtests see the expected revision.
		source := filepath.Join(h.projectDir, "main.go")
		content, err := os.ReadFile(source)
		require.NoError(t, err)
		fast := strings.Replace(string(content), "time.Sleep(20 * time.Second)\n\t", "", 1)
		fast = strings.Replace(fast, "\"os\"\n\t\"time\"\n)", "\"os\"\n)", 1)
		require.NotEqual(t, string(content), fast, "slow-start edit did not revert")
		require.NoError(t, os.WriteFile(source, []byte(fast), 0o644))
		out = h.run(false, "", "dev", "-d")
		require.Contains(t, out, "ready")
		h.waitRoute("hello from skali", 5*time.Minute)
	})

	t.Run("RollbackRestoresPreviousRevision", func(t *testing.T) {
		// Ship a v2 response, then roll back to the previous revision by
		// its checksum prefix: the stored revision re-applies exactly (same
		// image, same values) and the old response returns without any
		// rebuild.
		source := filepath.Join(h.projectDir, "main.go")
		content, err := os.ReadFile(source)
		require.NoError(t, err)
		v2 := strings.Replace(string(content), "hello from skali", "hello from v2", 1)
		require.NotEqual(t, string(content), v2, "v2 edit did not apply")
		require.NoError(t, os.WriteFile(source, []byte(v2), 0o644))

		out := h.run(false, "", "deploy", "--remote", "local", "--environment", "local", "--yes")
		require.Contains(t, out, "ready")
		match := regexp.MustCompile(`plan against active revision ([0-9a-f]+)`).FindStringSubmatch(out)
		require.NotNil(t, match, "the deploy must print the active revision, got: %s", out)
		previous := match[1]
		h.waitRoute("hello from v2", 2*time.Minute)

		out = h.run(false, "", "rollback", "--remote", "local", "--environment", "local",
			"--revision", previous, "--yes")
		require.Contains(t, out, "roll back local to "+previous)
		require.Contains(t, out, "ready")
		h.waitRoute("hello from skali", 2*time.Minute)

		// The rollback ran as its own journaled run kind.
		out = h.run(false, "", "run", "list", "--remote", "local", "--environment", "local")
		require.Contains(t, out, "rollback")

		// Rolling back to the revision the target already points at is
		// refused with a plain error.
		out = h.run(true, "", "rollback", "--remote", "local", "--environment", "local",
			"--revision", previous, "--yes")
		require.Contains(t, out, "already targets")

		// Restore the original source so the following subtests converge on
		// the revision the cluster is now serving again.
		require.NoError(t, os.WriteFile(source, content, 0o644))
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

		out := h.run(false, "", "deploy", "--remote", "local", "--environment", "local",
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

// TestDevGuestbookBackupRestore is the backup slice's acceptance loop, the
// REWORK_V2 17.3 destroy-and-restore requirement in miniature: snapshot an
// environment bearing all three data kinds to an external S3 target, purge
// the environment completely, redeploy it empty, and restore the data by
// snapshot id, with the fresh installation learning about the snapshot
// purely from the bucket.
func TestDevGuestbookBackupRestore(t *testing.T) {
	h := newE2EHarnessFor(t, "guestbook", "guestbook.localhost")

	// A MinIO container on the host is the external S3 target; pods and
	// skalid reach it as host.k3d.internal.
	const minioName = "skali-e2e-minio"
	const minioPort = 19100
	_ = exec.Command("docker", "rm", "-f", minioName).Run()
	out, err := exec.Command("docker", "run", "-d", "--name", minioName,
		"-p", fmt.Sprintf("127.0.0.1:%d:9000", minioPort),
		"minio/minio", "server", "/data").CombinedOutput()
	require.NoError(t, err, "start minio: %s", out)
	t.Cleanup(func() { _ = exec.Command("docker", "rm", "-f", minioName).Run() })
	makeMinioBucket(t, fmt.Sprintf("127.0.0.1:%d", minioPort), "skali-backups")

	run := h.run(false, "", "dev", "-d", "--skalid-image", "skalid:dev")
	require.Contains(t, run, "ready")
	h.waitRoute("visits: ", 10*time.Minute)

	// Seed all three data kinds: database rows, a bucket object, a file on
	// the persistent volume.
	status, body := h.request(http.MethodPut, "/notes/e2e", "bucket note survives reinstall")
	require.Equal(t, http.StatusOK, status, "store note: %s", body)
	status, body = h.request(http.MethodPut, "/disk/e2e", "disk note survives reinstall")
	require.Equal(t, http.StatusOK, status, "store disk file: %s", body)
	// Pad the history so the fresh environment (whose own health polls
	// insert a few rows) can never catch up to the snapshot's count.
	h.route("/")
	h.route("/")
	var before int
	_, body = h.route("/")
	_, err = fmt.Sscanf(body, "visits: %d", &before)
	require.NoError(t, err, "unexpected body %q", body)

	run = h.run(false, "", "backup", "target", "set", "--remote", "local",
		"--endpoint", fmt.Sprintf("http://host.k3d.internal:%d", minioPort),
		"--bucket", "skali-backups",
		"--access-key", "minioadmin", "--secret-key", "minioadmin")
	require.Contains(t, run, "backup target set")

	run = h.run(false, "", "backup", "create", "--remote", "local", "--environment", "local")
	require.Contains(t, run, "backup complete")

	run = h.run(false, "", "backup", "ls", "--remote", "local", "--environment", "local")
	match := regexp.MustCompile(`(?m)^([0-9a-f]{8}-[0-9a-f]{4}-[0-9a-f]{4}-[0-9a-f]{4}-[0-9a-f]{12})  `).
		FindStringSubmatch(run)
	require.NotNil(t, match, "no snapshot id in:\n%s", run)
	snapshot := match[1]

	// The wipe: purge removes the namespace, volumes, claims, and the
	// environment row itself, the closest local stand-in for a cluster
	// reinstall.
	run = h.run(false, "y\n", "dev", "down", "--purge")
	require.Contains(t, run, "is purged from the local platform")

	// Redeploy fresh and prove all three data kinds are empty.
	run = h.run(false, "", "dev", "-d")
	require.Contains(t, run, "ready")
	h.waitRoute("visits: ", 6*time.Minute)
	var fresh int
	_, body = h.route("/")
	_, err = fmt.Sscanf(body, "visits: %d", &fresh)
	require.NoError(t, err, "unexpected body %q", body)
	require.Less(t, fresh, before, "the purge must reset the visit history")
	_, body = h.route("/notes/e2e")
	require.NotEqual(t, "bucket note survives reinstall", body, "the purge must reset the bucket")
	status, _ = h.route("/disk/e2e")
	require.Equal(t, http.StatusNotFound, status, "the purge must reset the volume")

	// The fresh environment knows nothing in its database; the listing
	// comes purely from the S3 manifests.
	run = h.run(false, "", "backup", "ls", "--remote", "local", "--environment", "local")
	require.Contains(t, run, snapshot)

	run = h.run(false, "", "backup", "restore", snapshot, "--remote", "local", "--environment", "local", "--yes")
	require.Contains(t, run, "restore complete")

	// Every data kind is back: the dump's rows (plus this read's insert),
	// the bucket object, the volume file.
	h.waitRoute("visits: ", 4*time.Minute)
	var after int
	_, body = h.route("/")
	_, err = fmt.Sscanf(body, "visits: %d", &after)
	require.NoError(t, err, "unexpected body %q", body)
	require.Greater(t, after, before, "the restored visit history must include the pre-purge rows")
	require.Eventually(t, func() bool {
		_, body := h.route("/notes/e2e")
		return body == "bucket note survives reinstall"
	}, 2*time.Minute, 3*time.Second, "the bucket note must be restored")
	_, body = h.route("/disk/e2e")
	require.Equal(t, "disk note survives reinstall", body, "the disk note must be restored")
}

// makeMinioBucket waits for the test MinIO to answer and creates the
// backup bucket through its S3 API.
func makeMinioBucket(t *testing.T, endpoint, bucket string) {
	t.Helper()
	client, err := minio.New(endpoint, &minio.Options{
		Creds:        miniocredentials.NewStaticV4("minioadmin", "minioadmin", ""),
		Secure:       false,
		BucketLookup: minio.BucketLookupPath,
	})
	require.NoError(t, err)
	require.Eventually(t, func() bool {
		return client.MakeBucket(t.Context(), bucket, minio.MakeBucketOptions{}) == nil
	}, time.Minute, time.Second, "minio never became ready")
}

// runExit executes the CLI like run but reports the exit code instead of
// asserting on it, for commands whose status IS the result (exec).
func (h *e2eHarness) runExit(stdin string, args ...string) (string, int) {
	h.t.Helper()
	command := exec.Command(h.binary, args...)
	command.Dir = h.projectDir
	command.Env = h.env
	if stdin != "" {
		command.Stdin = strings.NewReader(stdin)
	}
	out, err := command.CombinedOutput()
	if err == nil {
		return string(out), 0
	}
	var exit *exec.ExitError
	require.ErrorAs(h.t, err, &exit, "skali %s did not run:\n%s", strings.Join(args, " "), out)
	return string(out), exit.ExitCode()
}

// TestDevExec drives the interactive exec surface end to end in plain pipe
// mode: output and exit status pass through the full edge path (traefik WS
// upgrade included), and stdin EOF propagates to the remote process.
func TestDevExec(t *testing.T) {
	h := newE2EHarness(t)

	// The stock hello-world image is FROM scratch; exec needs a shell, so
	// the scratch copy trades the final stage for alpine.
	dockerfile := `FROM golang:1.26-alpine AS builder
WORKDIR /src
COPY go.mod main.go ./
RUN CGO_ENABLED=0 go build -o /hello-build .

FROM alpine:3.22
COPY --from=builder /hello-build /hello-build
EXPOSE 8080
ENTRYPOINT ["/hello-build"]
`
	require.NoError(t, os.WriteFile(filepath.Join(h.projectDir, "Dockerfile"), []byte(dockerfile), 0o644))

	h.run(false, "", "dev", "-d", "--skalid-image", "skalid:dev")
	h.waitRoute("hello from skali", 5*time.Minute)

	// One-off command: stdout and the zero exit pass through. The service
	// argument is omitted on purpose: hello-world has exactly one
	// application, so the default must pick it.
	out, code := h.runExit("", "dev", "exec", "--", "sh", "-c", "echo exec-roundtrip")
	require.Zero(t, code, "exec failed:\n%s", out)
	require.Contains(t, out, "exec-roundtrip")

	// The remote exit status becomes the local one, with no error line.
	out, code = h.runExit("", "dev", "exec", "web", "--", "sh", "-c", "exit 7")
	require.Equal(t, 7, code, "output:\n%s", out)
	require.NotContains(t, out, "error:")

	// Piped stdin reaches the remote process and its EOF terminates it.
	out, code = h.runExit("ping-through-exec", "dev", "exec", "web", "--", "cat")
	require.Zero(t, code, "exec cat failed:\n%s", out)
	require.Contains(t, out, "ping-through-exec")

	// An unknown service resolves to no ready pod and hints at dev.
	out, code = h.runExit("", "dev", "exec", "missing", "--", "true")
	require.NotZero(t, code)
	require.Contains(t, out, "no running pod")

	h.run(false, "", "dev", "down")
}

// TestDevLocalDev drives local dev mode end to end: a dev-block application
// is not built, its host dev server answers through the cluster ingress
// (traefik -> selectorless Service -> intercept EndpointSlice -> host),
// skali dev run executes named and raw commands with the resolved
// environment and propagates exit codes, the session epilogue terminates
// the host process before pausing, and --preview restores the full
// in-cluster deployment.
func TestDevLocalDev(t *testing.T) {
	h := newE2EHarnessFor(t, "dev-loop", "dev-loop.localhost")

	// The host port is auto-allocated and handed to the dev command via
	// ${PORT}, so nothing here can collide with a developer's real dev
	// servers.
	manifestFor := func(devBlock string) string {
		return fmt.Sprintf(`version: "1"
name: dev-loop
applications:
  web:
    build:
      context: .
    ports:
      web:
        port: 8080
        protocol: http
    routes:
      public:
        domain: "${APP_DOMAIN}"
        path: /
        port: web
    environment:
      APP_DOMAIN: "${APP_DOMAIN}"
    commands:
      hello: [sh, -c, "echo hello-from-dev-run domain=$APP_DOMAIN"]
    dev:
%s
`, devBlock)
	}
	manifest := manifestFor(`      command: [python3, -m, http.server, "${PORT}", --bind, "0.0.0.0"]`)
	require.NoError(t, os.WriteFile(filepath.Join(h.projectDir, "skali.yml"), []byte(manifest), 0o644))
	require.NoError(t, os.WriteFile(filepath.Join(h.projectDir, "index.html"),
		[]byte("dev-loop-host-marker\n"), 0o644))

	// A pinned port that is already bound fails before any platform or
	// server work instead of drifting away from the intercept.
	blocker, err := net.Listen("tcp", ":0")
	require.NoError(t, err)
	defer blocker.Close()
	blockedPort := blocker.Addr().(*net.TCPAddr).Port
	pinned := manifestFor(fmt.Sprintf(
		"      command: [python3, -m, http.server, \"${PORT}\", --bind, \"0.0.0.0\"]\n      ports:\n        web: %d", blockedPort))
	require.NoError(t, os.WriteFile(filepath.Join(h.projectDir, "skali.yml"), []byte(pinned), 0o644))
	out, code := h.runExit("", "dev", "--skalid-image", "skalid:dev")
	require.NotZero(t, code)
	require.Contains(t, out, "already in use")
	require.NoError(t, os.WriteFile(filepath.Join(h.projectDir, "skali.yml"), []byte(manifest), 0o644))

	// A detached session cannot host local dev processes.
	out, code = h.runExit("", "dev", "-d", "--skalid-image", "skalid:dev")
	require.NotZero(t, code)
	require.Contains(t, out, "--detach cannot host local dev processes")

	// Bare dev: platform bootstrap, no build for web, host server running,
	// route served by it through the cluster edge.
	session := exec.Command(h.binary, "dev", "--skalid-image", "skalid:dev")
	session.Dir = h.projectDir
	session.Env = h.env
	output := &syncBuffer{}
	session.Stdout = output
	session.Stderr = output
	require.NoError(t, session.Start())
	sessionDone := make(chan error, 1)
	go func() { sessionDone <- session.Wait() }()
	stopSession := func() string {
		_ = session.Process.Signal(os.Interrupt)
		select {
		case err := <-sessionDone:
			require.NoError(t, err, "dev session after interrupt:\n%s", output.String())
		case <-time.After(5 * time.Minute):
			_ = session.Process.Kill()
			t.Fatalf("dev session never exited after interrupt:\n%s", output.String())
		}
		return output.String()
	}
	defer func() {
		if session.ProcessState == nil {
			_ = session.Process.Kill()
		}
	}()
	// waitForOutput polls for a marker, failing immediately (with the
	// session's actual output) when the session exits early. Failure
	// messages must read the buffer at failure time; passing
	// output.String() as a message argument would capture it empty at call
	// time.
	waitForOutput := func(marker string, wait time.Duration) {
		t.Helper()
		deadline := time.Now().Add(wait)
		for {
			if strings.Contains(output.String(), marker) {
				return
			}
			select {
			case err := <-sessionDone:
				t.Fatalf("dev session exited before %q (%v):\n%s", marker, err, output.String())
			case <-time.After(time.Second):
			}
			if time.Now().After(deadline) {
				t.Fatalf("session output never contained %q:\n%s", marker, output.String())
			}
		}
	}

	waitForOutput("following logs", 15*time.Minute)
	require.Contains(t, output.String(), "intercepted to the host dev process")
	// The ready line carries the auto-allocated port; it must come from the
	// allocation range.
	portMatch := regexp.MustCompile(`web=localhost:(\d+)`).FindStringSubmatch(output.String())
	require.NotNil(t, portMatch, "no allocated port in session output:\n%s", output.String())
	devPort, err := strconv.Atoi(portMatch[1])
	require.NoError(t, err)
	require.GreaterOrEqual(t, devPort, 20000)
	require.Less(t, devPort, 25000)
	h.waitRoute("dev-loop-host-marker", 3*time.Minute)
	// The host server's access log lines arrive multiplexed with the app
	// prefix (waitRoute above guarantees at least one request).
	waitForOutput("web | ", time.Minute)

	// Named and raw commands run with the resolved environment; exit codes
	// pass through silently.
	out, code = h.runExit("", "dev", "run", "hello")
	require.Zero(t, code, "dev run hello failed:\n%s", out)
	require.Contains(t, out, "hello-from-dev-run domain=dev-loop.localhost")
	out, code = h.runExit("", "dev", "run", "web", "--", "sh", "-c", "exit 7")
	require.Equal(t, 7, code, "output:\n%s", out)
	require.NotContains(t, out, "error:")

	// Ctrl-C: children terminate, the project pauses.
	sessionOut := stopSession()
	require.Contains(t, sessionOut, "paused")
	require.Eventually(t, func() bool {
		_, err := http.Get(fmt.Sprintf("http://127.0.0.1:%d/", devPort))
		return err != nil
	}, time.Minute, time.Second, "the host dev server is still listening")

	// --preview deploys the full in-cluster build: the pod, not a host
	// process, serves the route, and the intercept is cleared.
	require.NoError(t, os.WriteFile(filepath.Join(h.projectDir, "index.html"),
		[]byte("dev-loop-preview-marker\n"), 0o644))
	out = h.run(false, "", "dev", "--preview", "-d", "--skalid-image", "skalid:dev")
	require.NotContains(t, out, "intercepted to the host dev process")
	h.waitRoute("dev-loop-preview-marker", 10*time.Minute)

	h.run(false, "", "dev", "down")
}

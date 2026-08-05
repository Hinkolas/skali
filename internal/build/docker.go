package build

import (
	"bufio"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"sync"

	"github.com/Hinkolas/skali/internal/utils"
	"github.com/google/go-containerregistry/pkg/authn"
)

// Docker builds through BuildKit via the docker buildx CLI: the R0-resolved
// local builder. The result is exported locally (an OCI layout when the
// builder supports that exporter, else through the daemon image store) and
// pushed in-process with Auth, so docker itself never talks to the managed
// registry and never needs a credential for it.
type Docker struct {
	// Binary overrides the docker executable; empty uses "docker".
	Binary string
	// Auth authenticates the push toward the managed registry (the remote
	// session credential); nil falls back to the default docker keychain,
	// which is anonymous without a stored login.
	Auth authn.Authenticator
	// forceDaemonExport skips the OCI exporter and always exports through
	// the daemon image store; tests pin both export paths with it.
	forceDaemonExport bool
}

func (d *Docker) binary() string {
	if d.Binary != "" {
		return d.Binary
	}
	return "docker"
}

// Build implements Engine.
func (d *Docker) Build(ctx context.Context, req BuildRequest, sink ProgressSink) (Result, error) {
	mode := "oci-layout"
	var digest string
	var err error
	if d.forceDaemonExport {
		mode = "daemon-save"
		digest, err = d.buildViaDaemon(ctx, req, sink)
	} else {
		digest, err = d.buildViaLayout(ctx, req, sink)
		if errors.Is(err, errOCIExporterUnsupported) {
			sink.Line("info", "builder does not support the OCI exporter; exporting through the docker image store instead "+
				"(enabling docker's containerd image store makes this faster)")
			mode = "daemon-save"
			digest, err = d.buildViaDaemon(ctx, req, sink)
		}
	}
	if err != nil {
		return Result{}, err
	}
	provenance, err := json.Marshal(map[string]string{
		"engine":    "docker-buildx",
		"reference": req.PushRef,
		"platform":  req.Platform,
		"export":    mode,
	})
	if err != nil {
		return Result{}, fmt.Errorf("build: encode provenance: %w", err)
	}
	return Result{Digest: digest, Provenance: provenance}, nil
}

// errOCIExporterUnsupported marks a build that failed only because the
// selected builder cannot produce an OCI layout: the default docker driver
// on the classic image store. The caller retries through the daemon store.
var errOCIExporterUnsupported = errors.New("build: builder does not support the OCI exporter")

// buildViaLayout exports the build as an OCI layout directory and pushes it.
// BuildKit hands over its already-compressed blobs with attestation
// manifests disabled, so the layout's single artifact digest is the plain
// image manifest digest and unchanged blobs upload without recompression.
func (d *Docker) buildViaLayout(ctx context.Context, req BuildRequest, sink ProgressSink) (string, error) {
	dir, err := os.MkdirTemp("", "skali-build-oci-*")
	if err != nil {
		return "", fmt.Errorf("build: create export dir: %w", err)
	}
	defer os.RemoveAll(dir)
	dest := filepath.Join(dir, "layout")
	probe := &ociUnsupportedSink{inner: sink}
	if err := d.runBuildx(ctx, req, []string{"--output", "type=oci,tar=false,dest=" + dest}, probe); err != nil {
		if probe.matched {
			return "", errOCIExporterUnsupported
		}
		return "", err
	}
	return pushLayout(ctx, dest, req.PushRef, d.Auth, sink)
}

// buildViaDaemon loads the build into the daemon image store, exports it
// with docker save, and pushes the tarball. This works with every buildx
// driver at the cost of a full image round trip through the daemon.
func (d *Docker) buildViaDaemon(ctx context.Context, req BuildRequest, sink ProgressSink) (string, error) {
	if err := d.runBuildx(ctx, req, []string{"--load"}, sink); err != nil {
		return "", err
	}
	dir, err := os.MkdirTemp("", "skali-build-save-*")
	if err != nil {
		return "", fmt.Errorf("build: create export dir: %w", err)
	}
	defer os.RemoveAll(dir)
	tarPath := filepath.Join(dir, "image.tar")
	if out, err := exec.CommandContext(ctx, d.binary(), "save", "--output", tarPath, req.PushRef).CombinedOutput(); err != nil {
		return "", fmt.Errorf("build: docker save %s: %w\n%s", req.PushRef, err, strings.TrimSpace(string(out)))
	}
	// The loaded tag is an implementation detail of the export; drop it
	// again so deploys do not accumulate images in the user's image store.
	_ = exec.CommandContext(ctx, d.binary(), "image", "rm", req.PushRef).Run()
	return pushDockerSave(ctx, tarPath, req.PushRef, d.Auth, sink)
}

// runBuildx executes one buildx build with the given output flags, streaming
// progress line-wise into the sink.
func (d *Docker) runBuildx(ctx context.Context, req BuildRequest, output []string, sink ProgressSink) error {
	args := []string{
		"buildx", "build",
		"--provenance=false", "--sbom=false",
		"--progress=plain",
		"--tag", req.PushRef,
		"--file", req.Dockerfile,
	}
	args = append(args, output...)
	if req.Rebuild {
		args = append(args, "--pull", "--no-cache")
	}
	if req.Target != "" {
		args = append(args, "--target", req.Target)
	}
	if req.Platform != "" {
		args = append(args, "--platform", req.Platform)
	}
	for _, name := range utils.SortedKeys(req.Arguments) {
		args = append(args, "--build-arg", name+"="+req.Arguments[name])
	}
	// Secret values travel only through the child process environment and
	// BuildKit secret mounts; they never appear in the argument list.
	env := os.Environ()
	for index, id := range utils.SortedKeys(req.SecretEnv) {
		variable := fmt.Sprintf("SKALI_BUILD_SECRET_%d", index)
		args = append(args, "--secret", fmt.Sprintf("id=%s,env=%s", id, variable))
		env = append(env, variable+"="+req.SecretEnv[id])
	}
	args = append(args, req.ContextDir)

	cmd := exec.CommandContext(ctx, d.binary(), args...)
	cmd.Env = env
	stdout, err := cmd.StdoutPipe()
	if err != nil {
		return fmt.Errorf("build: pipe stdout: %w", err)
	}
	stderr, err := cmd.StderrPipe()
	if err != nil {
		return fmt.Errorf("build: pipe stderr: %w", err)
	}
	if err := cmd.Start(); err != nil {
		return fmt.Errorf("build: start %s buildx: %w", d.binary(), err)
	}
	var wg sync.WaitGroup
	wg.Add(2)
	go streamLines(&wg, stdout, sink)
	go streamLines(&wg, stderr, sink)
	wg.Wait()
	if err := cmd.Wait(); err != nil {
		return fmt.Errorf("build: buildx build failed: %w%s", err, crossBuildHint(req.Platform))
	}
	return nil
}

// ociUnsupportedSink watches the buildx stream for the driver refusing the
// OCI exporter, so the failure can be told apart from a genuinely broken
// build. Both stream goroutines write; reads happen only after they join.
type ociUnsupportedSink struct {
	inner   ProgressSink
	mu      sync.Mutex
	matched bool
}

func (s *ociUnsupportedSink) Line(level, message string) {
	lower := strings.ToLower(message)
	if strings.Contains(lower, "oci exporter") &&
		(strings.Contains(lower, "not supported") || strings.Contains(lower, "unsupported")) {
		s.mu.Lock()
		s.matched = true
		s.mu.Unlock()
	}
	s.inner.Line(level, message)
}

// crossBuildHint annotates a build failure when the target platforms do not
// include the builder's native one: the most likely extra requirement is
// binfmt emulation, and for multi-platform pushes a docker-container
// builder. The native platform is linux on the host architecture even on
// macOS, where builds run inside the Docker Desktop Linux VM.
func crossBuildHint(platform string) string {
	if platform == "" {
		return ""
	}
	native := "linux/" + runtime.GOARCH
	for target := range strings.SplitSeq(platform, ",") {
		if strings.TrimSpace(target) == native {
			return ""
		}
	}
	return " (building for " + platform + " on a " + runtime.GOARCH + " host may need emulation: " +
		"'docker run --privileged --rm tonistiigi/binfmt --install all'; " +
		"multi-platform builds may also need a docker-container builder: 'docker buildx create --use')"
}

// CheckBuildx probes docker buildx availability; the error carries an
// actionable installation hint.
func CheckBuildx(ctx context.Context, binary string) (string, error) {
	if binary == "" {
		binary = "docker"
	}
	out, err := exec.CommandContext(ctx, binary, "buildx", "version").CombinedOutput()
	if err != nil {
		return "", fmt.Errorf("build: docker buildx is required for local builds "+
			"(install Docker with the buildx plugin, https://docs.docker.com/build/): %w", err)
	}
	return strings.TrimSpace(string(out)), nil
}

func streamLines(wg *sync.WaitGroup, reader io.Reader, sink ProgressSink) {
	defer wg.Done()
	scanner := bufio.NewScanner(reader)
	scanner.Buffer(make([]byte, 0, 64*1024), 1024*1024)
	for scanner.Scan() {
		sink.Line("info", scanner.Text())
	}
}

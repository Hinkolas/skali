package build

import (
	"bufio"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"os/exec"
	"runtime"
	"sort"
	"strings"
	"sync"
)

// Docker builds through BuildKit via the docker buildx CLI: the R0-resolved
// local builder. The result is pushed directly (--push) and the digest is
// read from the metadata file, with attestation manifests disabled so the
// pushed digest is the plain image manifest digest.
type Docker struct {
	// Binary overrides the docker executable; empty uses "docker".
	Binary string
}

func (d *Docker) binary() string {
	if d.Binary != "" {
		return d.Binary
	}
	return "docker"
}

// Build implements Engine.
func (d *Docker) Build(ctx context.Context, req BuildRequest, sink ProgressSink) (Result, error) {
	metadata, err := os.CreateTemp("", "skali-build-metadata-*.json")
	if err != nil {
		return Result{}, fmt.Errorf("build: create metadata file: %w", err)
	}
	metadata.Close()
	defer os.Remove(metadata.Name())

	args := []string{
		"buildx", "build",
		"--push",
		"--provenance=false", "--sbom=false",
		"--progress=plain",
		"--metadata-file", metadata.Name(),
		"--tag", req.PushRef,
		"--file", req.Dockerfile,
	}
	if req.Target != "" {
		args = append(args, "--target", req.Target)
	}
	if req.Platform != "" {
		args = append(args, "--platform", req.Platform)
	}
	for _, name := range sortedNames(req.Arguments) {
		args = append(args, "--build-arg", name+"="+req.Arguments[name])
	}
	// Secret values travel only through the child process environment and
	// BuildKit secret mounts; they never appear in the argument list.
	env := os.Environ()
	for index, id := range sortedNames(req.SecretEnv) {
		variable := fmt.Sprintf("SKALI_BUILD_SECRET_%d", index)
		args = append(args, "--secret", fmt.Sprintf("id=%s,env=%s", id, variable))
		env = append(env, variable+"="+req.SecretEnv[id])
	}
	args = append(args, req.ContextDir)

	cmd := exec.CommandContext(ctx, d.binary(), args...)
	cmd.Env = env
	stdout, err := cmd.StdoutPipe()
	if err != nil {
		return Result{}, fmt.Errorf("build: pipe stdout: %w", err)
	}
	stderr, err := cmd.StderrPipe()
	if err != nil {
		return Result{}, fmt.Errorf("build: pipe stderr: %w", err)
	}
	if err := cmd.Start(); err != nil {
		return Result{}, fmt.Errorf("build: start %s buildx: %w", d.binary(), err)
	}
	var wg sync.WaitGroup
	wg.Add(2)
	go streamLines(&wg, stdout, sink)
	go streamLines(&wg, stderr, sink)
	wg.Wait()
	if err := cmd.Wait(); err != nil {
		return Result{}, fmt.Errorf("build: buildx build failed: %w%s", err, crossBuildHint(req.Platform))
	}

	digest, err := digestFromMetadata(metadata.Name())
	if err != nil {
		return Result{}, err
	}
	provenance, err := json.Marshal(map[string]string{
		"engine":    "docker-buildx",
		"reference": req.PushRef,
		"platform":  req.Platform,
	})
	if err != nil {
		return Result{}, fmt.Errorf("build: encode provenance: %w", err)
	}
	return Result{Digest: digest, Provenance: provenance}, nil
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

func digestFromMetadata(path string) (string, error) {
	payload, err := os.ReadFile(path)
	if err != nil {
		return "", fmt.Errorf("build: read metadata: %w", err)
	}
	var metadata map[string]any
	if err := json.Unmarshal(payload, &metadata); err != nil {
		return "", fmt.Errorf("build: decode metadata: %w", err)
	}
	digest, ok := metadata["containerimage.digest"].(string)
	if !ok || digest == "" {
		return "", fmt.Errorf("build: metadata carries no containerimage.digest")
	}
	return digest, nil
}

func streamLines(wg *sync.WaitGroup, reader io.Reader, sink ProgressSink) {
	defer wg.Done()
	scanner := bufio.NewScanner(reader)
	scanner.Buffer(make([]byte, 0, 64*1024), 1024*1024)
	for scanner.Scan() {
		sink.Line("info", scanner.Text())
	}
}

func sortedNames[T any](m map[string]T) []string {
	names := make([]string, 0, len(m))
	for name := range m {
		names = append(names, name)
	}
	sort.Strings(names)
	return names
}

package build

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"os"
	"os/exec"
	"regexp"
	"strings"
	"testing"
	"time"

	"github.com/google/go-containerregistry/pkg/name"
	"github.com/google/go-containerregistry/pkg/v1/remote"
	"github.com/stretchr/testify/require"

	"github.com/Hinkolas/skali/internal/registry"
)

// Tests below exec docker (buildx, a throwaway Distribution container) and
// pull public base images; they are gated like the live cluster suite.
func dockerGate(t *testing.T) {
	t.Helper()
	if os.Getenv("TEST_DOCKER") == "" {
		t.Skip("set TEST_DOCKER=1 to run tests that exec docker")
	}
	if _, err := CheckBuildx(context.Background(), ""); err != nil {
		t.Skipf("docker buildx unavailable: %v", err)
	}
}

// startRegistry runs a throwaway anonymous Distribution registry published
// on an ephemeral loopback port and returns its host:port. The image is
// pulled explicitly first so a cold machine reports a slow or failing pull
// instead of silently hanging inside docker run.
func startRegistry(t *testing.T) string {
	t.Helper()
	if pull, err := exec.Command("docker", "pull", "--quiet", "registry:2").CombinedOutput(); err != nil {
		t.Fatalf("pull registry:2: %v\n%s", err, pull)
	}
	out, err := exec.Command("docker", "run", "-d", "--rm", "-p", "127.0.0.1:0:5000", "registry:2").Output()
	require.NoError(t, err, "docker run registry:2")
	id := strings.TrimSpace(string(out))
	t.Cleanup(func() { _ = exec.Command("docker", "rm", "-f", id).Run() })

	port, err := exec.Command("docker", "port", id, "5000/tcp").Output()
	require.NoError(t, err)
	addr := strings.TrimSpace(strings.Split(string(port), "\n")[0])
	addr = strings.Replace(addr, "0.0.0.0", "127.0.0.1", 1)

	require.Eventually(t, func() bool {
		res, err := http.Get(fmt.Sprintf("http://%s/v2/", addr))
		if err != nil {
			return false
		}
		res.Body.Close()
		return res.StatusCode == http.StatusOK
	}, 30*time.Second, 250*time.Millisecond, "registry never became ready")
	return addr
}

type collectSink struct{ lines []string }

func (s *collectSink) Line(_, message string) { s.lines = append(s.lines, message) }

func scratchContext(t *testing.T) (string, string) {
	t.Helper()
	dir := t.TempDir()
	dockerfile := "FROM scratch\nCOPY hello.txt /hello.txt\n"
	require.NoError(t, os.WriteFile(dir+"/Dockerfile", []byte(dockerfile), 0o644))
	require.NoError(t, os.WriteFile(dir+"/hello.txt", []byte("hello from skali"), 0o644))
	return dir, dir + "/Dockerfile"
}

func TestDockerBuildPushAndVerify(t *testing.T) {
	dockerGate(t)
	ctx := context.Background()
	addr := startRegistry(t)
	contextDir, dockerfile := scratchContext(t)

	sink := &collectSink{}
	engine := &Docker{}
	result, err := engine.Build(ctx, BuildRequest{
		ContextDir: contextDir,
		Dockerfile: dockerfile,
		PushRef:    addr + "/skali/demo/web:test",
	}, sink)
	require.NoError(t, err, "build output:\n%s", strings.Join(sink.lines, "\n"))
	require.Regexp(t, regexp.MustCompile(`^sha256:[0-9a-f]{64}$`), result.Digest)
	require.NotEmpty(t, sink.lines, "progress must stream")

	// The registry itself confirms the digest; a wrong digest is refused.
	client := &registry.Client{Host: addr}
	require.NoError(t, client.VerifyManifest(ctx, "skali/demo/web", result.Digest))
	missing := "sha256:" + strings.Repeat("0", 64)
	require.ErrorIs(t, client.VerifyManifest(ctx, "skali/demo/web", missing), registry.ErrManifestNotFound)
}

// Secret build inputs pass through BuildKit secret mounts and must be
// absent from the pushed image: not in history, not in config, not in the
// environment (17.6).
func TestDockerBuildSecretNotPersisted(t *testing.T) {
	dockerGate(t)
	ctx := context.Background()
	addr := startRegistry(t)
	secret := "super-secret-build-token-1f9e"

	dir := t.TempDir()
	dockerfile := "FROM busybox\n" +
		"ARG PLAIN_VERSION\n" +
		"RUN --mount=type=secret,id=npm_token wc -c /run/secrets/npm_token\n"
	require.NoError(t, os.WriteFile(dir+"/Dockerfile", []byte(dockerfile), 0o644))

	sink := &collectSink{}
	engine := &Docker{}
	result, err := engine.Build(ctx, BuildRequest{
		ContextDir: dir,
		Dockerfile: dir + "/Dockerfile",
		Arguments:  map[string]string{"PLAIN_VERSION": "1.2.3"},
		SecretEnv:  map[string]string{"npm_token": secret},
		PushRef:    addr + "/skali/demo/secretcheck:test",
	}, sink)
	require.NoError(t, err, "build output:\n%s", strings.Join(sink.lines, "\n"))

	ref, err := name.ParseReference(addr + "/skali/demo/secretcheck@" + result.Digest)
	require.NoError(t, err)
	image, err := remote.Image(ref, remote.WithContext(ctx))
	require.NoError(t, err)
	manifest, err := image.RawManifest()
	require.NoError(t, err)
	config, err := image.RawConfigFile()
	require.NoError(t, err)
	require.NotContains(t, string(manifest), secret)
	require.NotContains(t, string(config), secret)
	require.Contains(t, string(config), "PLAIN_VERSION", "plain args stay visible in history")

	// The engine's own progress may echo Dockerfile commands but never the
	// secret value.
	require.NotContains(t, strings.Join(sink.lines, "\n"), secret)
}

// Imports must preserve manifest bytes and digests exactly; a pull/push
// round trip through a daemon would not.
func TestImportPreservesDigest(t *testing.T) {
	dockerGate(t)
	ctx := context.Background()
	addr := startRegistry(t)
	contextDir, dockerfile := scratchContext(t)

	engine := &Docker{}
	seeded, err := engine.Build(ctx, BuildRequest{
		ContextDir: contextDir,
		Dockerfile: dockerfile,
		PushRef:    addr + "/upstream/app:v1",
	}, &collectSink{})
	require.NoError(t, err)

	sink := &collectSink{}
	imported, err := Import(ctx, addr+"/upstream/app:v1", addr+"/cache/local/app:imported", false, sink)
	require.NoError(t, err, "import output:\n%s", strings.Join(sink.lines, "\n"))
	require.Equal(t, seeded.Digest, imported.Digest, "import must not change the digest")

	client := &registry.Client{Host: addr}
	require.NoError(t, client.VerifyManifest(ctx, "cache/local/app", imported.Digest))

	// The copied manifest is byte-identical upstream and in the cache.
	srcRef, err := name.ParseReference(addr + "/upstream/app@" + seeded.Digest)
	require.NoError(t, err)
	dstRef, err := name.ParseReference(addr + "/cache/local/app@" + imported.Digest)
	require.NoError(t, err)
	srcDesc, err := remote.Get(srcRef, remote.WithContext(ctx))
	require.NoError(t, err)
	dstDesc, err := remote.Get(dstRef, remote.WithContext(ctx))
	require.NoError(t, err)
	require.Equal(t, srcDesc.Manifest, dstDesc.Manifest)
	_ = json.Valid(srcDesc.Manifest)
}

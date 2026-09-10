package installer

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/Hinkolas/skali/internal/installer/host"
)

// fakeRelease serves one release the way GitHub does: raw assets under
// /<repo>/releases/download/<version>/<asset>.
func fakeRelease(t *testing.T, release string, assets map[string][]byte) *httptest.Server {
	t.Helper()
	mux := http.NewServeMux()
	prefix := "/Hinkolas/skali/releases/download/" + release + "/"
	mux.HandleFunc(prefix, func(w http.ResponseWriter, r *http.Request) {
		body, ok := assets[r.URL.Path[len(prefix):]]
		if !ok {
			http.NotFound(w, r)
			return
		}
		_, _ = w.Write(body)
	})
	server := httptest.NewServer(mux)
	t.Cleanup(server.Close)
	return server
}

func checksumLine(name string, body []byte) string {
	sum := sha256.Sum256(body)
	return hex.EncodeToString(sum[:]) + "  " + name + "\n"
}

func TestReleaseAssetsVerifyAgainstChecksums(t *testing.T) {
	t.Parallel()
	binary := []byte("hostd-arm64-binary")
	checksums := checksumLine("skali-hostd_linux_arm64", binary) +
		checksumLine("skali_linux_amd64", []byte("cli"))
	server := fakeRelease(t, "v0.2.0", map[string][]byte{
		"checksums.txt":           []byte(checksums),
		"skali-hostd_linux_arm64": binary,
		"release.json":            []byte(`{"version":"v0.2.0","k3s":"v1.37.1+k3s1"}`),
	})
	ctx := context.Background()

	sums, err := ReleaseChecksums(ctx, server.Client(), server.URL, "v0.2.0")
	require.NoError(t, err)
	require.Len(t, sums, 2)

	got, err := DownloadHostd(ctx, server.Client(), server.URL, "v0.2.0", "arm64",
		sums[HostdAsset("arm64")])
	require.NoError(t, err)
	require.Equal(t, binary, got)

	_, err = DownloadHostd(ctx, server.Client(), server.URL, "v0.2.0", "arm64",
		sums["skali_linux_amd64"])
	require.ErrorContains(t, err, "does not match its published checksum")
	_, err = DownloadHostd(ctx, server.Client(), server.URL, "v0.2.0", "arm64", "")
	require.ErrorContains(t, err, "no expected checksum")
	_, err = DownloadHostd(ctx, server.Client(), server.URL, "v0.2.0", "amd64", sums[HostdAsset("arm64")])
	require.ErrorContains(t, err, "release asset is missing")
	require.ErrorIs(t, err, ErrAssetMissing)

	// The CLI asset goes through the same generic, checksum-gated download.
	require.Equal(t, "skali_linux_amd64", CLIAsset("linux", "amd64"))
	_, err = DownloadAsset(ctx, server.Client(), server.URL, "v0.2.0", CLIAsset("linux", "amd64"), sums["skali_linux_amd64"])
	require.ErrorIs(t, err, ErrAssetMissing, "listed in checksums.txt but not published")
	_, err = DownloadAsset(ctx, server.Client(), server.URL, "v0.2.0", CLIAsset("darwin", "arm64"), "")
	require.ErrorContains(t, err, "skali_darwin_arm64 download has no expected checksum")
	got, err = DownloadAsset(ctx, server.Client(), server.URL, "v0.2.0", HostdAsset("arm64"), sums[HostdAsset("arm64")])
	require.NoError(t, err)
	require.Equal(t, binary, got)

	metadata, err := FetchReleaseMetadata(ctx, server.Client(), server.URL, "v0.2.0")
	require.NoError(t, err)
	require.Equal(t, "v1.37.1+k3s1", metadata.K3s)

	// A release published before release.json existed reads as no metadata.
	older := fakeRelease(t, "v0.1.0", map[string][]byte{"checksums.txt": []byte(checksums)})
	metadata, err = FetchReleaseMetadata(ctx, older.Client(), older.URL, "v0.1.0")
	require.NoError(t, err)
	require.Nil(t, metadata)
	_, err = ReleaseChecksums(ctx, older.Client(), older.URL, "v0.3.0")
	require.ErrorContains(t, err, "release asset is missing")
}

func TestCheckK3sMove(t *testing.T) {
	t.Parallel()
	require.NoError(t, CheckK3sMove("v1.36.2+k3s1", "v1.36.3+k3s1"))
	require.NoError(t, CheckK3sMove("v1.36.3+k3s1", "v1.37.0+k3s1"))
	require.NoError(t, CheckK3sMove("v1.36.3+k3s1", "v1.36.3+k3s1"))
	require.NoError(t, CheckK3sMove("", "v1.36.3+k3s1"), "an unknown version is not judged")
	require.ErrorContains(t, CheckK3sMove("v1.36.3+k3s1", "v1.36.2+k3s1"), "downgrade")
	require.ErrorContains(t, CheckK3sMove("v1.35.0+k3s1", "v1.37.0+k3s1"), "at most one Kubernetes minor")
}

func TestReplaceHostdAndRestartSchedulesBothUnitsOnServers(t *testing.T) {
	t.Parallel()
	fake := &host.Fake{
		FS: map[string][]byte{HostdBinaryPath: []byte("old")},
		Handlers: map[string]func(host.Command) (host.Result, error){
			"systemd-run": func(host.Command) (host.Result, error) { return host.Result{}, nil },
		},
	}
	require.NoError(t, ReplaceHostdAndRestart(context.Background(), fake, []byte("new"), true))
	require.Equal(t, []byte("new"), fake.FS[HostdBinaryPath])
	require.Equal(t, []byte("old"), fake.FS[HostdBinaryPath+".prev"])
	require.Len(t, fake.Commands, 1)
	script := fake.Commands[0].Args[len(fake.Commands[0].Args)-1]
	require.Contains(t, script, "systemctl restart "+HostdCoordinatorUnit)
	require.Contains(t, script, "systemctl restart "+HostdAgentUnit)

	agent := &host.Fake{Handlers: fake.Handlers}
	require.NoError(t, ReplaceHostdAndRestart(context.Background(), agent, []byte("new"), false))
	script = agent.Commands[0].Args[len(agent.Commands[0].Args)-1]
	require.NotContains(t, script, HostdCoordinatorUnit)

	require.ErrorContains(t, ReplaceHostdAndRestart(context.Background(), agent, nil, false), "empty")
}

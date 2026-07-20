package registry

import (
	"context"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"testing"

	"github.com/stretchr/testify/require"
)

const knownDigest = "sha256:8f434346648f6b96df89dda901c5176b10a6d83961dd3c1ac88b59b2dc327aa4"

// fakeRegistry serves the two /v2 endpoints VerifyManifest touches.
func fakeRegistry(t *testing.T) (*Client, *httptest.Server) {
	t.Helper()
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/v2/" {
			w.WriteHeader(http.StatusOK)
			return
		}
		if digest, ok := strings.CutPrefix(r.URL.Path, "/v2/skali/demo/web/manifests/"); ok {
			if digest != knownDigest {
				w.WriteHeader(http.StatusNotFound)
				return
			}
			w.Header().Set("Docker-Content-Digest", knownDigest)
			w.Header().Set("Content-Type", "application/vnd.oci.image.manifest.v1+json")
			w.Header().Set("Content-Length", "2")
			w.WriteHeader(http.StatusOK)
			return
		}
		w.WriteHeader(http.StatusNotFound)
	}))
	t.Cleanup(server.Close)
	parsed, err := url.Parse(server.URL)
	require.NoError(t, err)
	return &Client{Host: parsed.Host}, server
}

func TestVerifyManifest(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	client, server := fakeRegistry(t)

	require.NoError(t, client.VerifyManifest(ctx, "skali/demo/web", knownDigest))

	missing := "sha256:" + strings.Repeat("0", 64)
	require.ErrorIs(t, client.VerifyManifest(ctx, "skali/demo/web", missing), ErrManifestNotFound)
	require.ErrorIs(t, client.VerifyManifest(ctx, "skali/other/app", knownDigest), ErrManifestNotFound)

	server.Close()
	require.ErrorIs(t, client.VerifyManifest(ctx, "skali/demo/web", knownDigest), ErrUnavailable)

	var disabled *Client
	require.ErrorIs(t, disabled.VerifyManifest(ctx, "skali/demo/web", knownDigest), ErrDisabled)
	require.True(t, (&Client{}).Disabled())
}

// The Endpoint field lets skalid dial a different address than the one
// artifact references carry.
func TestVerifyManifestUsesEndpoint(t *testing.T) {
	t.Parallel()
	client, _ := fakeRegistry(t)
	client.Endpoint = client.Host
	client.Host = "localhost:5510"
	require.NoError(t, client.VerifyManifest(context.Background(), "skali/demo/web", knownDigest))
}

func TestRepositoryLayout(t *testing.T) {
	t.Parallel()
	require.Equal(t, "skali/demo/web", ReleaseRepo("demo", "web"))

	repo, err := CacheRepo("ghcr.io/example/worker:1.4")
	require.NoError(t, err)
	require.Equal(t, "cache/ghcr.io/example/worker", repo)

	// Docker Hub short names normalize through their canonical registry.
	repo, err = CacheRepo("nginx")
	require.NoError(t, err)
	require.Equal(t, "cache/index.docker.io/library/nginx", repo)

	repo, err = CacheRepo("ghcr.io/example/worker@" + knownDigest)
	require.NoError(t, err)
	require.Equal(t, "cache/ghcr.io/example/worker", repo)

	_, err = CacheRepo("not a reference !!")
	require.Error(t, err)

	client := &Client{Host: "localhost:5510"}
	require.Equal(t, "localhost:5510/skali/demo/web:release", client.PushRef("skali/demo/web", "release"))
	require.Equal(t, "localhost:5510/cache/ghcr.io/x/y:latest", client.PushRef("cache/ghcr.io/x/y", ""))
}

package client

import (
	"context"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/stretchr/testify/require"
)

func TestCLIListingAndDownload(t *testing.T) {
	var gotAuth, gotVersion, gotPath string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set(VersionHeader, "v0.4.0")
		switch r.URL.Path {
		case "/v1/system/cli":
			_, _ = w.Write([]byte(`{"version":"v0.4.0","enabled":true,"platforms":[{"platform":"darwin_arm64","sha256":"abc","size":3}]}`))
		case "/v1/system/cli/darwin_arm64":
			gotAuth = r.Header.Get("Authorization")
			gotVersion = r.Header.Get(ClientVersionHeader)
			gotPath = r.URL.Path
			w.Header().Set("Content-Type", "application/octet-stream")
			_, _ = w.Write([]byte("bin"))
		default:
			http.NotFound(w, r)
		}
	}))
	defer srv.Close()

	c := New(srv.URL, "tok", Caller{UserAgent: "skali/test", Version: "v0.3.2"})
	listing, err := c.CLIListing(context.Background())
	require.NoError(t, err)
	require.Equal(t, "v0.4.0", listing.Version)
	require.True(t, listing.Enabled)
	asset, ok := listing.Platform("darwin_arm64")
	require.True(t, ok)
	require.Equal(t, "abc", asset.SHA256)
	_, ok = listing.Platform("linux_amd64")
	require.False(t, ok)

	body, err := c.DownloadCLI(context.Background(), "darwin_arm64")
	require.NoError(t, err)
	require.Equal(t, "bin", string(body))
	require.Equal(t, "Bearer tok", gotAuth)
	require.Equal(t, "v0.3.2", gotVersion)
	require.Equal(t, "/v1/system/cli/darwin_arm64", gotPath)
	require.Equal(t, "v0.4.0", c.ObservedVersion())
}

func TestDownloadCLINotServed(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set(VersionHeader, "v0.4.0")
		w.WriteHeader(http.StatusNotFound)
		_, _ = w.Write([]byte(`{"error":{"code":"cli_not_served","message":"this cluster does not serve the skali CLI; install skali v0.4.0 from the release feed or your own distribution"}}`))
	}))
	defer srv.Close()

	c := New(srv.URL, "tok", Caller{})
	_, err := c.DownloadCLI(context.Background(), "darwin_arm64")
	var apiErr *APIError
	require.ErrorAs(t, err, &apiErr)
	require.Equal(t, CodeCLINotServed, apiErr.Code)
	require.Equal(t, http.StatusNotFound, apiErr.Status)
	require.Contains(t, apiErr.Message, "v0.4.0")
	require.Equal(t, "v0.4.0", c.ObservedVersion())
}

func TestDownloadCLIRejectsOversizedBody(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		// Declare more than the cap; the body is capped on the client side
		// without buffering the whole thing.
		w.Header().Set("Content-Type", "application/octet-stream")
		chunk := []byte(strings.Repeat("x", 1<<20))
		for written := 0; written <= maxCLIBytes; written += len(chunk) {
			if _, err := w.Write(chunk); err != nil {
				return
			}
		}
	}))
	defer srv.Close()

	c := New(srv.URL, "", Caller{})
	_, err := c.DownloadCLI(context.Background(), "linux_amd64")
	require.Error(t, err)
	require.Contains(t, err.Error(), "exceeds")
}

func TestCLIPlatform(t *testing.T) {
	require.Equal(t, "linux_amd64", CLIPlatform("linux", "amd64"))
}

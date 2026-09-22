package main

import (
	"context"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/Hinkolas/skali/internal/installer"
	versionpkg "github.com/Hinkolas/skali/internal/version"
)

func TestGeneratedReleaseMetadataConsumer(t *testing.T) {
	const version = "v0.1.0-rc.4"
	data, err := releaseMetadata(version)
	require.NoError(t, err)
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		require.Equal(t, "/"+versionpkg.ReleaseRepo+"/releases/download/"+version+"/release.json", r.URL.Path)
		_, _ = w.Write(data)
	}))
	defer server.Close()
	metadata, err := installer.FetchReleaseMetadata(context.Background(), server.Client(), server.URL, version)
	require.NoError(t, err)
	require.Equal(t, &installer.ReleaseMetadata{Version: version, K3s: installer.K3sVersion}, metadata)
}

func TestReleaseMetadataAcceptsSnapshotVersions(t *testing.T) {
	data, err := releaseMetadata("v0.0.1-next-780a4e6")
	require.NoError(t, err)
	require.Contains(t, string(data), "v0.0.1-next-780a4e6")
}

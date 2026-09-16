package main

import (
	"context"
	"github.com/Hinkolas/skali/internal/installer"
	versionpkg "github.com/Hinkolas/skali/internal/version"
	"github.com/stretchr/testify/require"
	"net/http"
	"net/http/httptest"
	"testing"
)

func TestGeneratedReleaseMetadataConsumer(t *testing.T) {
	const version = "v0.1.0-rc.5"
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

// A release older than the newest ledger entry would ship binaries whose
// ledger describes a change as landing in the future.
func TestReleaseMetadataRefusesLedgerAhead(t *testing.T) {
	_, err := releaseMetadata("v0.1.0-rc.1")
	require.ErrorContains(t, err, "the manifest ledger names")
	require.ErrorContains(t, err, "v0.1.0-rc.1")
	_, err = releaseMetadata("v0.2.0")
	require.NoError(t, err)
}

// A snapshot rehearsal has no tag: goreleaser derives a pseudo-version from
// the commit, and the ledger has no planned release to be measured against.
func TestReleaseMetadataAcceptsSnapshotVersions(t *testing.T) {
	data, err := releaseMetadata("v0.0.1-next-780a4e6")
	require.NoError(t, err)
	require.Contains(t, string(data), "v0.0.1-next-780a4e6")
}

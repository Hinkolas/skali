package main

import (
	"context"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/Hinkolas/skali/internal/installer"
	"github.com/Hinkolas/skali/internal/manifest"
	versionpkg "github.com/Hinkolas/skali/internal/version"
)

// withLedger swaps the compiled-in manifest ledger for the test, so the
// assertions do not depend on whether an entry is pending right now.
func withLedger(t *testing.T, ledger []manifest.Change) {
	t.Helper()
	previous := manifest.Ledger
	manifest.Ledger = ledger
	t.Cleanup(func() { manifest.Ledger = previous })
}

func shipped(release, path string) manifest.Change {
	return manifest.Change{Release: release, Kind: manifest.ChangeAdded, Path: path, Message: "m", Hint: "h"}
}

func TestGeneratedReleaseMetadataConsumer(t *testing.T) {
	withLedger(t, []manifest.Change{shipped("v0.1.0-rc.3", "version")})
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

// A release older than the newest ledger entry would ship binaries whose
// ledger describes a change as landing in the future.
func TestReleaseMetadataRefusesLedgerAhead(t *testing.T) {
	ledger := []manifest.Change{shipped("v0.1.0-rc.3", "version"), shipped("v0.1.0-rc.7", "applications.*.routes.*.compress")}
	err := ledgerWithin(ledger, "v0.1.0-rc.1")
	require.ErrorContains(t, err, "the manifest ledger names v0.1.0-rc.3")
	require.ErrorContains(t, err, "v0.1.0-rc.1")
	require.NoError(t, ledgerWithin(ledger, "v0.1.0-rc.7"))
	require.NoError(t, ledgerWithin(ledger, "v0.2.0"))
}

// An entry still written as Next has not been stamped with the release
// being cut; the release stops until task release:stamp has landed.
func TestReleaseMetadataRefusesPendingEntries(t *testing.T) {
	ledger := []manifest.Change{shipped("v0.1.0-rc.7", "x"), {Release: manifest.Next, Kind: manifest.ChangeRemoved, Path: "backups", Message: "m", Hint: "h"}}
	err := ledgerWithin(ledger, "v0.1.0-rc.8")
	require.ErrorContains(t, err, "the manifest ledger entry for backups is still pending")
	require.ErrorContains(t, err, "task release:stamp V=v0.1.0-rc.8")
	withLedger(t, ledger)
	_, err = releaseMetadata("v0.1.0-rc.8")
	require.ErrorContains(t, err, "still pending")
}

// A snapshot rehearsal has no tag: goreleaser derives a pseudo-version from
// the commit, and the ledger has no planned release to be measured against.
func TestReleaseMetadataAcceptsSnapshotVersions(t *testing.T) {
	data, err := releaseMetadata("v0.0.1-next-780a4e6")
	require.NoError(t, err)
	require.Contains(t, string(data), "v0.0.1-next-780a4e6")
}

const ledgerSource = `package manifest

const Next = "next"

var Ledger = []Change{
	{
		Release: "v0.1.0-rc.7",
		Kind:    ChangeAdded,
		Path:    "applications.*.routes.*.compress",
	},
	{
		Release: Next,
		Kind:    ChangeRemoved,
		Path:    "backups",
		Message: "Release: Next is text here, not an entry",
	},
	{Release: Next, Kind: ChangeAdded, Path: "x"},
}
`

// Stamping rewrites every pending entry to the release being cut and
// nothing else; the release must be newer than every stamped entry.
func TestStampLedger(t *testing.T) {
	withLedger(t, []manifest.Change{shipped("v0.1.0-rc.7", "applications.*.routes.*.compress"),
		{Release: manifest.Next, Kind: manifest.ChangeRemoved, Path: "backups", Message: "m", Hint: "h"}})
	stamped, count, err := stampLedger([]byte(ledgerSource), "v0.1.0-rc.8")
	require.NoError(t, err)
	require.Equal(t, 2, count)
	require.Equal(t, `package manifest

const Next = "next"

var Ledger = []Change{
	{
		Release: "v0.1.0-rc.7",
		Kind:    ChangeAdded,
		Path:    "applications.*.routes.*.compress",
	},
	{
		Release: "v0.1.0-rc.8",
		Kind:    ChangeRemoved,
		Path:    "backups",
		Message: "Release: Next is text here, not an entry",
	},
	{Release: "v0.1.0-rc.8", Kind: ChangeAdded, Path: "x"},
}
`, string(stamped))

	again, count, err := stampLedger(stamped, "v0.1.0-rc.9")
	require.NoError(t, err)
	require.Zero(t, count, "nothing pending, nothing to stamp")
	require.Equal(t, stamped, again)

	_, _, err = stampLedger([]byte(ledgerSource), "v0.1.0-rc.7")
	require.ErrorContains(t, err, "already names v0.1.0-rc.7, which is not older than v0.1.0-rc.7")
	_, _, err = stampLedger([]byte(ledgerSource), "v0.1.0-rc.6")
	require.ErrorContains(t, err, "not older than v0.1.0-rc.6")
	_, _, err = stampLedger([]byte(ledgerSource), "rc8")
	require.ErrorContains(t, err, `"rc8" is not a release tag`)
	_, _, err = stampLedger([]byte("var Ledger = []Change{{Release: Next,"), "v0.1.0-rc.8")
	require.ErrorContains(t, err, "does not parse")
}

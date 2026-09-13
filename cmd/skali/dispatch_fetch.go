package main

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"net/http"
	"strings"

	"github.com/Hinkolas/skali/internal/cliconfig"
	"github.com/Hinkolas/skali/internal/client"
	"github.com/Hinkolas/skali/internal/installer"
)

// errSilentFetch marks a fetch failure the command itself is about to
// explain (an unreachable or reinstalled cluster): home runs without a
// warning of its own.
var errSilentFetch = errors.New("dispatch: fetch failed quietly")

// dispatchWarning is a fetch failure worth one stderr line; home runs
// afterwards and the daemon's gate plus the skew hint name the manual fix.
type dispatchWarning struct{ text string }

func (w *dispatchWarning) Error() string { return w.text }

// cliFetch is a runnable binary for one release: its cache path, its bytes
// (for a promotion), and whether this call stored it.
type cliFetch struct {
	version string
	path    string
	binary  []byte
	fetched bool
}

// ensureCLI returns the cached skali for release want, fetching it first
// from the remote's own cluster (the only source guaranteed to hold the
// exact version, and the one that works air-gapped) and second from the
// release feed. A cluster that already moved on again answers with its
// current release; that one is adopted and recorded, since it is what the
// cluster runs now.
func (d *dispatcher) ensureCLI(ctx context.Context, cfg *cliconfig.Config, target *remoteTarget, want string) (*cliFetch, error) {
	path := installer.CLICachePath(d.cacheDir, want)
	if data, ok := installer.CachedBinary(path); ok {
		return &cliFetch{version: want, path: path, binary: data}, nil
	}
	fmt.Fprintf(d.stderr, "fetching skali %s for remote %s\n", want, target.Name)

	binary, sum, got, clusterErr := d.fetchFromCluster(ctx, target, want)
	if errors.Is(clusterErr, errSilentFetch) {
		return nil, clusterErr
	}
	if clusterErr != nil {
		var feedErr error
		binary, sum, feedErr = d.fetchFromFeed(ctx, want)
		if feedErr != nil {
			return nil, fetchFailure(target.Name, want, d.homeVersion, clusterErr, feedErr)
		}
		got = want
		var mismatch *checksumMismatch
		if errors.As(clusterErr, &mismatch) {
			fmt.Fprintln(d.stderr, d.style().Yellow(fmt.Sprintf("warning: skali %s from remote %s did not match its checksum; fetched it from the release feed",
				want, target.Name)))
		}
	}
	if got != want {
		target.Remote.Version = got
		_ = cliconfig.Save(cfg)
		path = installer.CLICachePath(d.cacheDir, got)
	}
	if err := installer.StoreBinary(path, binary, sum); err != nil {
		return nil, &dispatchWarning{fmt.Sprintf("cannot cache skali %s under %s: %v; running skali %s",
			got, installer.CLICacheDir(d.cacheDir), err, d.homeVersion)}
	}
	return &cliFetch{version: got, path: path, binary: binary, fetched: true}, nil
}

// checksumMismatch is a download whose bytes did not match the digest its
// source published; nothing is written from it.
type checksumMismatch struct{ source string }

func (e *checksumMismatch) Error() string { return "did not match its checksum from " + e.source }

// clusterRefusal is a cluster that answered but did not hand out the
// binary; notServed marks the operator switch (or an image without
// binaries), which has its own wording.
type clusterRefusal struct {
	reason    string
	notServed bool
}

func (e *clusterRefusal) Error() string { return e.reason }

// fetchFromCluster downloads want from the remote's cluster with the
// remote's session and verifies it against the listing's digest. got is
// the release actually served.
func (d *dispatcher) fetchFromCluster(ctx context.Context, target *remoteTarget, want string) (binary []byte, sum, got string, err error) {
	remote := target.Remote
	if remote.Token == "" {
		return nil, "", "", &clusterRefusal{reason: "no session for remote " + target.Name}
	}
	c := client.New(remote.Master, remote.Token, caller())
	c.PinInstance(remote.Instance, nil)
	listing, err := c.CLIListing(ctx)
	if err != nil {
		return nil, "", "", classifyClusterError(target.Name, err)
	}
	if !listing.Enabled {
		return nil, "", "", &clusterRefusal{reason: "remote " + target.Name + " does not serve its CLI", notServed: true}
	}
	platform := client.CLIPlatform(d.goos, d.goarch)
	asset, ok := listing.Platform(platform)
	if !ok {
		return nil, "", "", &clusterRefusal{reason: fmt.Sprintf("remote %s ships no skali for %s", target.Name, platform)}
	}
	got = listing.Version
	if got == "" {
		got = want
	}
	binary, err = c.DownloadCLI(ctx, platform)
	if err != nil {
		return nil, "", "", classifyClusterError(target.Name, err)
	}
	digest := sha256.Sum256(binary)
	if hex.EncodeToString(digest[:]) != asset.SHA256 {
		return nil, "", "", &checksumMismatch{"remote " + target.Name}
	}
	return binary, asset.SHA256, got, nil
}

func classifyClusterError(remoteName string, err error) error {
	if _, ok := errors.AsType[*client.InstanceMismatchError](err); ok {
		return errSilentFetch
	}
	if api, ok := errors.AsType[*client.APIError](err); ok {
		switch {
		case api.Code == client.CodeCLINotServed:
			return &clusterRefusal{reason: "remote " + remoteName + " does not serve its CLI", notServed: true}
		case api.Status == http.StatusUnauthorized || api.Status == http.StatusForbidden:
			return &clusterRefusal{reason: "the session for remote " + remoteName + " is not valid"}
		}
		return &clusterRefusal{reason: fmt.Sprintf("remote %s answered %s", remoteName, api.Message)}
	}
	// Transport failures: the command itself reports the outage.
	return &unreachable{err}
}

// unreachable is a cluster that did not answer at all.
type unreachable struct{ err error }

func (e *unreachable) Error() string { return e.err.Error() }

func isUnreachable(err error) bool {
	_, ok := errors.AsType[*unreachable](err)
	return ok
}

// fetchFromFeed downloads want from the release feed, verified against
// the release's checksums.txt.
func (d *dispatcher) fetchFromFeed(ctx context.Context, want string) ([]byte, string, error) {
	return installer.FetchCLI(ctx, d.feedClient, d.releaseBase, want, d.goos, d.goarch)
}

// fetchFailure phrases a double failure in one line, or stays silent when
// the cluster was unreachable and the command will say so itself.
func fetchFailure(remoteName, want, home string, clusterErr, feedErr error) error {
	feedMissing := errors.Is(feedErr, installer.ErrAssetMissing)
	feedChecksum := strings.Contains(feedErr.Error(), "does not match its published checksum")
	var refusal *clusterRefusal
	var mismatch *checksumMismatch
	switch {
	case feedChecksum:
		return &dispatchWarning{fmt.Sprintf("skali %s from the release feed did not match its published checksum; running skali %s",
			want, home)}
	case isUnreachable(clusterErr):
		return errSilentFetch
	case errors.As(clusterErr, &refusal) && refusal.notServed && feedMissing:
		return &dispatchWarning{fmt.Sprintf("remote %s does not serve its CLI and the release feed has no skali %s; "+
			"run skali upgrade --version %s or install it from your own distribution; running skali %s",
			remoteName, want, want, home)}
	case feedMissing:
		reason := clusterErr.Error()
		if errors.As(clusterErr, &mismatch) {
			reason = "its copy " + mismatch.Error()
		}
		return &dispatchWarning{fmt.Sprintf("release %s is not on the release feed and remote %s did not serve it (%s); running skali %s",
			want, remoteName, reason, home)}
	}
	return &dispatchWarning{fmt.Sprintf("could not fetch skali %s for remote %s: %v; %v; running skali %s",
		want, remoteName, clusterErr, feedErr, home)}
}

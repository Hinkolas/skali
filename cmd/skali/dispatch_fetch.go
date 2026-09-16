package main

import (
	"context"
	"errors"
	"fmt"
	"strings"

	"github.com/Hinkolas/skali/internal/filelock"
	"github.com/Hinkolas/skali/internal/installer"
)

// cliFetch is a runnable binary for one release: its cache path, its bytes
// (for a required home upgrade), and whether this call stored it.
type cliFetch struct {
	version string
	path    string
	binary  []byte
	fetched bool
}

// ensureCLI returns the cached skali for release want, fetching it from
// the release feed first when the cache has none (docs/versioning.md,
// decision 2: the cluster names its version, the feed holds every
// release). The download is verified against the release's checksums.txt
// before anything is written.
func (d *dispatcher) ensureCLI(ctx context.Context, remoteName, want string) (*cliFetch, error) {
	path := installer.CLICachePath(d.cacheDir, want)
	if data, ok := installer.CachedBinary(path); ok {
		return &cliFetch{version: want, path: path, binary: data}, nil
	}
	unlock, err := filelock.Acquire(ctx, installer.CLILockPath(d.cacheDir, want))
	if err != nil {
		return nil, err
	}
	defer unlock()
	if data, ok := installer.CachedBinary(path); ok {
		return &cliFetch{version: want, path: path, binary: data}, nil
	}
	fmt.Fprintf(d.stderr, "fetching skali %s for remote %s\n", want, remoteName)

	binary, sum, err := installer.FetchCLI(ctx, d.feedClient, d.releaseBase, want, d.goos, d.goarch)
	if err != nil {
		return nil, fetchFailure(want, err)
	}
	if err := installer.StoreBinary(path, binary, sum); err != nil {
		return nil, fmt.Errorf("cannot cache skali %s under %s: %v", want, installer.CLICacheDir(d.cacheDir), err)
	}
	return &cliFetch{version: want, path: path, binary: binary, fetched: true}, nil
}

// fetchFailure describes why the matching CLI cannot be obtained.
func fetchFailure(want string, err error) error {
	switch {
	case strings.Contains(err.Error(), "does not match its published checksum"):
		return fmt.Errorf("skali %s from the release feed did not match its published checksum",
			want)
	case errors.Is(err, installer.ErrAssetMissing):
		return fmt.Errorf("the release feed has no skali %s (%v); install it from your own distribution",
			want, err)
	}
	return fmt.Errorf("could not fetch skali %s from the release feed: %v",
		want, err)
}

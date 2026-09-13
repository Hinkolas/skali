package main

import (
	"context"
	"errors"
	"fmt"
	"strings"

	"github.com/Hinkolas/skali/internal/installer"
)

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
	fmt.Fprintf(d.stderr, "fetching skali %s for remote %s\n", want, remoteName)

	binary, sum, err := installer.FetchCLI(ctx, d.feedClient, d.releaseBase, want, d.goos, d.goarch)
	if err != nil {
		return nil, fetchFailure(want, d.homeVersion, err)
	}
	if err := installer.StoreBinary(path, binary, sum); err != nil {
		return nil, &dispatchWarning{fmt.Sprintf("cannot cache skali %s under %s: %v; running skali %s",
			want, installer.CLICacheDir(d.cacheDir), err, d.homeVersion)}
	}
	return &cliFetch{version: want, path: path, binary: binary, fetched: true}, nil
}

// fetchFailure phrases a feed failure in one line. Home runs next and the
// daemon's refusal names the manual fix, so every line ends with what runs.
func fetchFailure(want, home string, err error) error {
	switch {
	case strings.Contains(err.Error(), "does not match its published checksum"):
		return &dispatchWarning{fmt.Sprintf("skali %s from the release feed did not match its published checksum; running skali %s",
			want, home)}
	case errors.Is(err, installer.ErrAssetMissing):
		return &dispatchWarning{fmt.Sprintf("the release feed has no skali %s (%v); install it from your own distribution; running skali %s",
			want, err, home)}
	}
	return &dispatchWarning{fmt.Sprintf("could not fetch skali %s from the release feed: %v; running skali %s",
		want, err, home)}
}

// Package cliassets serves the skali CLI binaries a skalid image ships: a
// cluster hands authenticated members the exact release it runs, so
// dispatch works air-gapped and independently of the public release feed
// (docs/versioning.md, decision 2). The release image copies every
// platform build to one directory as skali_<goos>_<goarch>; the daemon
// hashes them once at boot and streams them on request.
package cliassets

import (
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"io"
	"io/fs"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strings"
)

// ErrNotShipped reports a daemon without CLI binaries: the directory is
// absent or holds none, which is the working-tree image's normal state.
var ErrNotShipped = errors.New("no skali CLI binaries are shipped with this daemon")

// platformPattern is the <goos>_<goarch> token a platform is named by, in
// file names and in the download route.
var platformPattern = regexp.MustCompile(`^[a-z0-9]+_[a-z0-9]+$`)

// ValidPlatform reports whether a platform token has the <goos>_<goarch>
// shape; the route uses it before touching the store.
func ValidPlatform(platform string) bool {
	return platformPattern.MatchString(platform)
}

// Platform names the <goos>_<goarch> token for one build.
func Platform(goos, goarch string) string {
	return goos + "_" + goarch
}

// Asset is one shipped CLI binary.
type Asset struct {
	Platform string
	SHA256   string
	Size     int64
	Path     string
}

// Store is the set of shipped binaries, hashed once.
type Store struct {
	dir    string
	assets map[string]Asset
}

// Load reads dir, hashing every skali_<goos>_<goarch> file in it; other
// names are ignored. A missing or empty directory is ErrNotShipped.
func Load(dir string) (*Store, error) {
	entries, err := os.ReadDir(dir)
	if errors.Is(err, fs.ErrNotExist) {
		return nil, ErrNotShipped
	}
	if err != nil {
		return nil, err
	}
	store := &Store{dir: dir, assets: map[string]Asset{}}
	for _, entry := range entries {
		platform, ok := strings.CutPrefix(entry.Name(), "skali_")
		if !ok || entry.IsDir() || !ValidPlatform(platform) {
			continue
		}
		path := filepath.Join(dir, entry.Name())
		sum, size, err := digest(path)
		if err != nil {
			return nil, err
		}
		store.assets[platform] = Asset{Platform: platform, SHA256: sum, Size: size, Path: path}
	}
	if len(store.assets) == 0 {
		return nil, ErrNotShipped
	}
	return store, nil
}

// Assets lists the shipped binaries sorted by platform.
func (s *Store) Assets() []Asset {
	assets := make([]Asset, 0, len(s.assets))
	for _, asset := range s.assets {
		assets = append(assets, asset)
	}
	sort.Slice(assets, func(i, j int) bool { return assets[i].Platform < assets[j].Platform })
	return assets
}

// Lookup finds one platform's asset.
func (s *Store) Lookup(platform string) (Asset, bool) {
	asset, ok := s.assets[platform]
	return asset, ok
}

// Open opens one platform's binary for streaming; the caller closes it.
func (s *Store) Open(platform string) (*os.File, Asset, error) {
	asset, ok := s.assets[platform]
	if !ok {
		return nil, Asset{}, fs.ErrNotExist
	}
	file, err := os.Open(asset.Path)
	if err != nil {
		return nil, Asset{}, err
	}
	return file, asset, nil
}

func digest(path string) (string, int64, error) {
	file, err := os.Open(path)
	if err != nil {
		return "", 0, err
	}
	defer file.Close()
	hash := sha256.New()
	size, err := io.Copy(hash, file)
	if err != nil {
		return "", 0, err
	}
	return hex.EncodeToString(hash.Sum(nil)), size, nil
}

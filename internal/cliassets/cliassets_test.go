package cliassets

import (
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"io"
	"os"
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/require"
)

func TestLoadHashesShippedBinaries(t *testing.T) {
	dir := t.TempDir()
	linux := []byte("#!/bin/sh\necho linux\n")
	darwin := []byte("#!/bin/sh\necho darwin\n")
	require.NoError(t, os.WriteFile(filepath.Join(dir, "skali_linux_amd64"), linux, 0o755))
	require.NoError(t, os.WriteFile(filepath.Join(dir, "skali_darwin_arm64"), darwin, 0o755))
	require.NoError(t, os.WriteFile(filepath.Join(dir, "checksums.txt"), []byte("ignored\n"), 0o644))
	require.NoError(t, os.WriteFile(filepath.Join(dir, "skali_bad-name"), []byte("x"), 0o755))
	require.NoError(t, os.Mkdir(filepath.Join(dir, "skali_linux_arm64"), 0o755))

	store, err := Load(dir)
	require.NoError(t, err)
	assets := store.Assets()
	require.Len(t, assets, 2)
	require.Equal(t, "darwin_arm64", assets[0].Platform)
	require.Equal(t, "linux_amd64", assets[1].Platform)
	sum := sha256.Sum256(linux)
	require.Equal(t, hex.EncodeToString(sum[:]), assets[1].SHA256)
	require.Equal(t, int64(len(linux)), assets[1].Size)

	file, asset, err := store.Open("darwin_arm64")
	require.NoError(t, err)
	defer file.Close()
	require.Equal(t, "darwin_arm64", asset.Platform)
	body, err := io.ReadAll(file)
	require.NoError(t, err)
	require.Equal(t, darwin, body)

	_, _, err = store.Open("windows_amd64")
	require.Error(t, err)
	_, ok := store.Lookup("linux_amd64")
	require.True(t, ok)
}

func TestLoadNotShipped(t *testing.T) {
	_, err := Load(filepath.Join(t.TempDir(), "missing"))
	require.True(t, errors.Is(err, ErrNotShipped))

	empty := t.TempDir()
	require.NoError(t, os.WriteFile(filepath.Join(empty, "notes.txt"), []byte("x"), 0o644))
	_, err = Load(empty)
	require.True(t, errors.Is(err, ErrNotShipped))
}

func TestPlatformTokens(t *testing.T) {
	require.Equal(t, "darwin_arm64", Platform("darwin", "arm64"))
	require.True(t, ValidPlatform("linux_amd64"))
	require.False(t, ValidPlatform("linux"))
	require.False(t, ValidPlatform("../etc"))
	require.False(t, ValidPlatform("Linux_AMD64"))
}

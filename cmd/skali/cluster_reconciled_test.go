package main

import (
	"bytes"
	"context"
	"os"
	"path/filepath"
	"runtime"
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/Hinkolas/skali/internal/installer"
)

func TestHostdSiblingPaths(t *testing.T) {
	t.Parallel()
	require.Equal(t, []string{
		"/opt/skali/bin/skali-hostd",
		"/opt/skali/bin/" + installer.HostdAsset(runtime.GOARCH),
	}, hostdSiblingPaths("/opt/skali/bin/skali"))
}

func TestLoadHostdBinaryDevBuildNeedsLocalCopy(t *testing.T) {
	withCLIVersion(t, "v0.1.0-3-gabc1234-dirty")
	original := hostdBinFlag
	t.Cleanup(func() { hostdBinFlag = original })

	hostdBinFlag = ""
	_, _, err := loadHostdBinary(context.Background(), nil)
	if err == nil {
		t.Skip("a skali-hostd sits next to the test binary")
	}
	require.ErrorContains(t, err, "development build")
	require.ErrorContains(t, err, "--hostd-bin")

	explicit := filepath.Join(t.TempDir(), "hostd")
	require.NoError(t, os.WriteFile(explicit, []byte("built here"), 0o755))
	hostdBinFlag = explicit
	var out bytes.Buffer
	data, path, err := loadHostdBinary(context.Background(), &out)
	require.NoError(t, err)
	require.Equal(t, []byte("built here"), data)
	require.Equal(t, explicit, path)
	require.Empty(t, out.String(), "a local copy downloads nothing")
}

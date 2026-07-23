package build

import (
	"runtime"
	"testing"

	"github.com/stretchr/testify/require"
)

func TestCrossBuildHint(t *testing.T) {
	native := "linux/" + runtime.GOARCH
	foreign := "linux/amd64"
	if runtime.GOARCH == "amd64" {
		foreign = "linux/arm64"
	}

	require.Empty(t, crossBuildHint(""), "no platform means the builder default")
	require.Empty(t, crossBuildHint(native))
	require.Empty(t, crossBuildHint(foreign+","+native),
		"a multi-platform build including the native platform needs no arch hint")

	hint := crossBuildHint(foreign)
	require.Contains(t, hint, foreign)
	require.Contains(t, hint, "emulation")
	require.Contains(t, hint, "tonistiigi/binfmt")
}

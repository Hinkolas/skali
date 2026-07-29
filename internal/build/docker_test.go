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

// The fallback trigger must recognize buildx refusing the OCI exporter
// (classic-store docker driver) without firing on ordinary build failures.
func TestOCIUnsupportedSink(t *testing.T) {
	for line, matches := range map[string]bool{
		"ERROR: OCI exporter is not supported for the docker driver": true,
		"error: oci exporter is currently unsupported":               true,
		"ERROR: process \"/bin/sh -c false\" did not complete":       false,
		"#10 pushing layers": false,
	} {
		sink := &ociUnsupportedSink{inner: DiscardSink{}}
		sink.Line("info", line)
		require.Equal(t, matches, sink.matched, "line: %s", line)
	}
}

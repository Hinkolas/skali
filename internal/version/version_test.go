package version

import (
	"testing"

	"github.com/stretchr/testify/require"
)

func TestOlder(t *testing.T) {
	cases := []struct {
		a, b string
		want bool
	}{
		{"v0.1.0", "v0.2.0", true},
		{"v0.2.0", "v0.1.0", false},
		{"v0.2.0", "v0.2.0", false},
		{"v0.2.0", "v0.10.0", true}, // numeric, not lexical
		{"0.1.0", "v0.2.0", true},   // leading v optional
		{"v0.9.9", "v1.0.0", true},
		{"v0.2.0-rc.1", "v0.2.0", true}, // prerelease precedes its release
		{"v0.2.0", "v0.2.0-rc.1", false},
		{"v0.2.0-rc.1", "v0.2.0-rc.2", true},
		{"v0.2.0-beta.3", "v0.2.0-rc.1", true},
		{"v0.2.0-rc1", "v0.2.0-rc.2", false}, // undotted suffix is not a release shape
		{"v0.2.0-rc.1", "v0.3.0-alpha.1", true},
		{"v0.2.0-3-gabc1234", "v0.2.0", false}, // git describe is not comparable
		{"weird", "v0.2.0", false},             // unjudgeable versions never report drift
		{"v0.1.0", "weird", false},
		{"", "v0.2.0", false},
	}
	for _, c := range cases {
		require.Equal(t, c.want, Older(c.a, c.b), "Older(%q, %q)", c.a, c.b)
	}
}

func TestReleaseShapes(t *testing.T) {
	require.True(t, IsRelease("v0.1.0"))
	require.True(t, IsRelease("v0.1.0-rc.1"))
	require.True(t, IsPrerelease("v0.1.0-rc.1"))
	require.False(t, IsPrerelease("v0.1.0"))
	require.False(t, IsRelease("v0.1.0-3-gabc1234"))
	require.False(t, IsRelease("v0.0.0-dev"))
	require.False(t, IsRelease("v0.1.0-dirty"))
	require.False(t, IsRelease("v0.1.0-rc1"))
	require.False(t, IsRelease("v0.1.0-rc.1.2"))

	got, ok := PublishedSkalidVersion(PublishedSkalidImage("v0.1.0"))
	require.True(t, ok)
	require.Equal(t, "v0.1.0", got)
	_, ok = PublishedSkalidVersion("skalid:dev")
	require.False(t, ok)

	require.Equal(t, "https://github.com/Hinkolas/skali/releases/download/v0.1.0/checksums.txt",
		ReleaseAssetURL(DefaultReleaseBase, "v0.1.0", "checksums.txt"))
}

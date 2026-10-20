package devremote

import (
	"testing"

	"github.com/stretchr/testify/require"
)

func TestParseHost(t *testing.T) {
	t.Parallel()
	for input, want := range map[string]string{
		"mac-mini":                   "mac-mini",
		"Mac-Mini.local.":            "mac-mini.local",
		"192.168.69.100":             "192.168.69.100",
		"100.101.102.103":            "100.101.102.103",
		"fd7a:115c:a1e0::1":          "fd7a:115c:a1e0::1",
		"[fd7a:115c:a1e0::1]":        "fd7a:115c:a1e0::1",
		" kilohertz-mac-mini.ts.net": "kilohertz-mac-mini.ts.net",
		"localhost":                  "localhost",
	} {
		got, err := ParseHost(input)
		require.NoError(t, err, input)
		require.Equal(t, want, got, input)
	}
	for input, mention := range map[string]string{
		"":                         "pass the host",
		"mac-mini:8443":            "carries a port",
		"[fd7a::1]:443":            "carries a port",
		"https://mac-mini":         "not a host",
		"mac-mini/path":            "not a host",
		"user@mac-mini":            "not a host",
		"mac mini":                 "not a host name",
		"192.168.69.100:443":       "carries a port",
		"ssh://kilohertz@mac-mini": "not a host",
	} {
		_, err := ParseHost(input)
		require.ErrorContains(t, err, mention, input)
	}
}

func TestParseExposeAddress(t *testing.T) {
	t.Parallel()
	for input, want := range map[string]string{
		"192.168.69.100":      "192.168.69.100",
		"100.101.102.103":     "100.101.102.103",
		"fd7a:115c:a1e0::1":   "fd7a:115c:a1e0::1",
		"[fd7a:115c:a1e0::1]": "fd7a:115c:a1e0::1",
		"::ffff:10.0.0.2":     "10.0.0.2",
	} {
		got, err := ParseExposeAddress(input)
		require.NoError(t, err, input)
		require.Equal(t, want, got.String(), input)
	}
	for input, mention := range map[string]string{
		"0.0.0.0":   "every interface",
		"::":        "every interface",
		"127.0.0.1": "loopback",
		"::1":       "loopback",
		"127.0.0.2": "loopback",
		"mac-mini":  "not an IP address",
		"":          "not an IP address",
	} {
		_, err := ParseExposeAddress(input)
		require.ErrorContains(t, err, mention, input)
	}
}

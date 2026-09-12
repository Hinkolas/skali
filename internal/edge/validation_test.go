package edge

import (
	"strings"
	"testing"

	"github.com/stretchr/testify/require"
)

func TestCanonicalDomainAcceptsLocalhostRoutes(t *testing.T) {
	t.Parallel()
	for _, host := range []string{"b1.eplan.localhost", "skali.localhost", "app.example.com.", "A-1.Example.COM"} {
		got, err := CanonicalDomain(host)
		require.NoError(t, err, host)
		require.Equal(t, strings.ToLower(strings.TrimSuffix(host, ".")), got)
	}
}

// The error names the offending character and where it sits, never the
// value: route domains resolve from stored values, which are secret.
func TestCanonicalDomainNamesTheOffendingCharacter(t *testing.T) {
	t.Parallel()
	cases := []struct{ value, want string }{
		{"b1.eplan.localhost\r", "character 19 is a carriage return (CRLF line ending?)"},
		{"b1.eplan.localhost ", "character 19 is a non-breaking space (U+00A0)"},
		{"b1.eplan.localhost​", "character 19 is an invisible non-ASCII character (U+200B)"},
		{"b1.eplan.localhost ", "character 19 is a space"},
		{"b1 eplan.localhost", "character 3 is a space"},
		{"é.localhost", "character 1 is a non-ASCII character (U+00E9)"},
		{"bad`host", "character 4 is '`'"},
		{"app_web.localhost", "character 4 is '_'"},
		{"https://a.localhost", "character 6 is ':'"},
		{"a..localhost", "empty label at position 3"},
		{"-a.localhost", "label at position 1 starts or ends with a hyphen"},
		{"127.0.0.1", "not an IP address"},
		{"", "the value is empty"},
	}
	for _, c := range cases {
		_, err := CanonicalDomain(c.value)
		require.Error(t, err, "%q", c.value)
		require.Contains(t, err.Error(), c.want, "%q", c.value)
		if len(c.value) > 1 {
			require.NotContains(t, err.Error(), c.value, "the error must not repeat the value")
		}
	}
}

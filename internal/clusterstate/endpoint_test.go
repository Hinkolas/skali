package clusterstate

import (
	"testing"

	"github.com/stretchr/testify/require"
)

func TestNormalizeEndpoint(t *testing.T) {
	t.Parallel()
	for input, expected := range map[string]string{
		"10.1.0.3":              "https://10.1.0.3:6444",
		"10.1.0.3:7444":         "https://10.1.0.3:7444",
		"https://10.1.0.3":      "https://10.1.0.3:6444",
		"https://node.internal": "https://node.internal:6444",
		"[fd00::3]":             "https://[fd00::3]:6444",
	} {
		actual, err := NormalizeEndpoint(input)
		require.NoError(t, err, input)
		require.Equal(t, expected, actual, input)
	}
}

func TestNormalizeEndpointRejectsInsecureOrNonOriginInputs(t *testing.T) {
	t.Parallel()
	for _, input := range []string{
		"http://10.1.0.3", "https://user@node", "https://node/path",
		"https://node?x=1", "https://node#fragment",
	} {
		_, err := NormalizeEndpoint(input)
		require.Error(t, err, input)
	}
}

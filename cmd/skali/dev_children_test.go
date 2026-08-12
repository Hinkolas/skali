package main

import (
	"slices"
	"testing"

	"github.com/stretchr/testify/require"
)

func TestPortEnvName(t *testing.T) {
	t.Parallel()
	require.Equal(t, "WEB", portEnvName("web"))
	require.Equal(t, "ADMIN_UI", portEnvName("admin-ui"))
	require.Equal(t, "ROUTE_API", portEnvName("route-api"))
}

func TestDevChildEnviron(t *testing.T) {
	resolved := map[string]string{"DATABASE_URL": "postgres://x", "PATH": "/resolved"}
	ports := map[string]int{"web": 20417}
	environ, lookup := devChildEnviron(resolved, ports)

	// Resolved values and injected ports append after the session
	// environment, so os/exec's last-duplicate-wins gives them precedence.
	require.Greater(t, slices.Index(environ, "PATH=/resolved"), 0)
	require.Contains(t, environ, "SKALI_PORT_WEB=20417")
	require.Contains(t, environ, "PORT=20417")
	require.Equal(t, "/resolved", lookup["PATH"])
	require.Equal(t, "20417", lookup["PORT"])
	require.Equal(t, "20417", lookup["SKALI_PORT_WEB"])

	// Two ports: no ambiguous plain PORT.
	environ, lookup = devChildEnviron(nil, map[string]int{"web": 20417, "metrics": 20418})
	require.Contains(t, environ, "SKALI_PORT_WEB=20417")
	require.Contains(t, environ, "SKALI_PORT_METRICS=20418")
	require.NotContains(t, lookup, "PORT")
	for _, entry := range environ {
		require.NotEqual(t, "PORT=20417", entry)
		require.NotEqual(t, "PORT=20418", entry)
	}

	// No ports (skali dev run): nothing injected.
	_, lookup = devChildEnviron(map[string]string{"A": "b"}, nil)
	require.Equal(t, "b", lookup["A"])
	require.NotContains(t, lookup, "PORT")
}

func TestExpandDevCommand(t *testing.T) {
	t.Parallel()
	lookup := map[string]string{"PORT": "20417", "SKALI_PORT_WEB": "20417"}
	ports := map[string]int{"web": 20417}

	argv, err := expandDevCommand("web",
		[]string{"vite", "--port", "${PORT}", "--host"}, lookup, ports)
	require.NoError(t, err)
	require.Equal(t, []string{"vite", "--port", "20417", "--host"}, argv)

	_, err = expandDevCommand("web", []string{"vite", "--port", "${PROT}"}, lookup, ports)
	require.ErrorContains(t, err, "dev command for web")
	require.ErrorContains(t, err, "unknown variable ${PROT}")
	require.ErrorContains(t, err, "SKALI_PORT_WEB")
}

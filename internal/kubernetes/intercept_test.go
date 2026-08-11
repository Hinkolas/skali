package kubernetes

import (
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/Hinkolas/skali/internal/compiler"
)

func TestResolveInterceptPorts(t *testing.T) {
	t.Parallel()
	application := compiler.Application{
		Ports: map[string]compiler.Port{
			"web":     {Port: 3000, Protocol: "http"},
			"metrics": {Port: 9090, Protocol: "http"},
		},
		Routes: map[string]compiler.Route{
			// Numeric route target on the same number as "web": the
			// synthesized port is suppressed by renderServicePorts.
			"main": {Port: compiler.PortTarget{Number: 3000}},
			// Numeric route target with no named port: synthesizes route-api.
			"api": {Port: compiler.PortTarget{Number: 4000}},
		},
	}

	// Full coverage: named ports direct, synthesized route port through the
	// equally-numbered declaration.
	resolved, err := ResolveInterceptPorts(application, map[string]int32{
		"web": 5173, "metrics": 9464,
	})
	require.ErrorContains(t, err, "route-api")
	require.Nil(t, resolved)

	application.Routes["api"] = compiler.Route{Port: compiler.PortTarget{Number: 9090}}
	resolved, err = ResolveInterceptPorts(application, map[string]int32{
		"web": 5173, "metrics": 9464,
	})
	require.NoError(t, err)
	require.Equal(t, map[string]int32{"web": 5173, "metrics": 9464}, resolved)

	// Unknown declared name fails.
	_, err = ResolveInterceptPorts(application, map[string]int32{"ghost": 1})
	require.ErrorContains(t, err, "ghost")

	// Missing coverage names the uncovered port.
	_, err = ResolveInterceptPorts(application, map[string]int32{"web": 5173})
	require.ErrorContains(t, err, "metrics")
}

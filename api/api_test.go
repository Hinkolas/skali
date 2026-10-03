package api

import (
	"testing"

	"github.com/stretchr/testify/require"
	"gopkg.in/yaml.v3"
)

// The daemon serves this file at /openapi.yaml, so it must at least parse.
// An unquoted ": " inside a plain description once made it invalid YAML.
func TestOpenAPIParses(t *testing.T) {
	var spec struct {
		OpenAPI string         `yaml:"openapi"`
		Paths   map[string]any `yaml:"paths"`
	}
	require.NoError(t, yaml.Unmarshal(OpenAPI, &spec))
	require.Equal(t, "3.1.0", spec.OpenAPI)
	for _, path := range []string{"/healthz", "/openapi.yaml", "/token", "/v1/auth/login"} {
		require.Contains(t, spec.Paths, path)
	}
}

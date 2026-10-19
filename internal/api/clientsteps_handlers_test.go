package api

import (
	"testing"

	"github.com/stretchr/testify/require"
)

func TestClientStepService(t *testing.T) {
	for key, service := range map[string]string{
		"artifacts.web":        "applications.web",
		"artifacts.web.build":  "applications.web",
		"artifacts.api.verify": "applications.api",
		"artifacts.":           "",
		"artifacts":            "",
		"rollout:web":          "",
	} {
		require.Equal(t, service, clientStepService(key), key)
	}
}

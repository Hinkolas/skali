package reconcile

import (
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/Hinkolas/skali/internal/module"
)

func TestHealthFieldsMirrorsSummary(t *testing.T) {
	t.Parallel()
	statuses := []ServiceStatus{
		{Key: "web", Type: "application", Health: module.HealthProgressing, Diagnostics: []module.Diagnostic{
			{Severity: "info", Code: "certificate-pending", Message: "certificate not ready"}}},
		{Key: "data", Type: "database", Health: module.HealthHealthy},
	}
	fields := healthFields(statuses)
	require.Equal(t, true, fields["health"])
	services := fields["services"].([]map[string]any)
	require.Len(t, services, 2)
	require.Equal(t, "data", services[0]["key"], "services are sorted by key like the summary lines")
	require.Equal(t, "healthy", services[0]["health"])
	require.Nil(t, services[0]["message"])
	require.Equal(t, "waiting for the TLS certificate", services[1]["message"])
	require.Equal(t, "certificate-pending", services[1]["code"])
	require.Equal(t, []string{"data: healthy", "web: progressing (see TLS certificate checkpoint)"}, healthSummary(statuses))
}

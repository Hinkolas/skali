package module

import (
	"testing"

	"github.com/stretchr/testify/require"
)

func TestWorstHealth(t *testing.T) {
	t.Parallel()
	cases := []struct {
		name    string
		healths []Health
		want    Health
	}{
		{"nothing to evaluate", nil, HealthUnknown},
		{"single healthy", []Health{HealthHealthy}, HealthHealthy},
		{"unknown outranks healthy", []Health{HealthHealthy, HealthUnknown}, HealthUnknown},
		{"progressing outranks unknown", []Health{HealthUnknown, HealthProgressing}, HealthProgressing},
		{"degraded outranks healthy", []Health{HealthHealthy, HealthDegraded, HealthHealthy}, HealthDegraded},
		{"unhealthy outranks degraded", []Health{HealthDegraded, HealthUnhealthy}, HealthUnhealthy},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			require.Equal(t, tc.want, WorstHealth(tc.healths))
		})
	}
}

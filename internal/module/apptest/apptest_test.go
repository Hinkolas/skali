package apptest

import (
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/Hinkolas/skali/internal/compiler"
	"github.com/Hinkolas/skali/internal/manifest"
	"github.com/Hinkolas/skali/internal/module"
)

const testManifest = `version: "1"
name: demo
applications:
  api:
    image: ghcr.io/example/api:1.0.0
    ports:
      http:
        port: 8080
        protocol: http
    environment:
      DATABASE_URL: "{{ databases.main.url }}"
databases:
  main:
    engine: postgres
    version: 17
`

func compile(t *testing.T) compiler.ProjectDefinition {
	t.Helper()
	document, err := manifest.Parse([]byte(testManifest), "skali.yml")
	require.NoError(t, err)
	result, err := compiler.Compile(document)
	require.NoError(t, err)
	return result.Definition
}

func TestDecodeAndContract(t *testing.T) {
	t.Parallel()
	definition := compile(t)
	registry := module.NewRegistry()
	require.NoError(t, registry.Register(Module{}))

	m, ok := registry.Get("application")
	require.True(t, ok)
	svc, err := m.Decode(definition, "api")
	require.NoError(t, err)
	require.Equal(t, "api", svc.Key())
	require.Equal(t, "application", svc.Type())
	require.Equal(t, []string{"databases.main"}, svc.Dependencies())

	artifacts := svc.Artifacts()
	require.Len(t, artifacts, 1)
	require.Equal(t, "ghcr.io/example/api:1.0.0", artifacts[0].Source.Image)

	steps := svc.Steps()
	require.Len(t, steps, 3)
	require.Equal(t, "prepare-artifact:api", steps[0].Key)

	removal := svc.Removal()
	require.False(t, removal.DataLoss)

	_, err = m.Decode(definition, "missing")
	require.Error(t, err)
}

func TestOrderOverCompiledDependencies(t *testing.T) {
	t.Parallel()
	definition := compile(t)
	services := []string{"applications.api", "databases.main"}
	batches, err := module.Order(services, definition.Dependencies)
	require.NoError(t, err)
	require.Equal(t, [][]string{{"databases.main"}, {"applications.api"}}, batches)
}

func TestEvaluateMatrix(t *testing.T) {
	t.Parallel()
	definition := compile(t)
	svc, err := Module{}.Decode(definition, "api")
	require.NoError(t, err)

	workload := func(ready, desired int64) []module.ObservedResource {
		return []module.ObservedResource{{
			Kind: "workload", Name: "api",
			Fields: map[string]int64{"readyReplicas": ready, "replicas": desired},
		}}
	}
	testCases := []struct {
		name     string
		observed []module.ObservedResource
		health   module.Health
	}{
		{"missing resource", nil, module.HealthUnknown},
		{"zero desired", workload(0, 0), module.HealthUnknown},
		{"all ready", workload(3, 3), module.HealthHealthy},
		{"none ready", workload(0, 3), module.HealthUnhealthy},
		{"partially ready", workload(2, 3), module.HealthDegraded},
	}
	for _, testCase := range testCases {
		t.Run(testCase.name, func(t *testing.T) {
			t.Parallel()
			evaluation := svc.Evaluate(testCase.observed)
			require.Equal(t, testCase.health, evaluation.Health)
			if evaluation.Health != module.HealthHealthy {
				require.NotEmpty(t, evaluation.Diagnostics)
			}
		})
	}
}

package kubernetes

import (
	"testing"

	"github.com/stretchr/testify/require"
	corev1 "k8s.io/api/core/v1"

	"github.com/Hinkolas/skali/internal/compiler"
	"github.com/Hinkolas/skali/internal/manifest"
)

func TestRenderNamespace(t *testing.T) {
	t.Parallel()
	namespace := RenderNamespace("hello-world", "production", "0198f2f4-0000-7000-8000-000000000001")
	require.Equal(t, "skali-0198f2f4-0000-7000-8000-000000000001", namespace.Name)
	require.Equal(t, "true", namespace.Labels[LabelManaged])
	require.Equal(t, "hello-world", namespace.Labels[LabelProject])
	require.Equal(t, "0198f2f4-0000-7000-8000-000000000001", namespace.Labels[LabelEnvironment])
	require.NotContains(t, namespace.Labels, LabelRevision)
}

func TestRenderEnvironmentSecret(t *testing.T) {
	t.Parallel()
	data := map[string][]byte{
		"APP_DOMAIN": []byte("hello.localhost"),
		"API_KEY":    []byte("fixture-not-a-real-secret"),
	}
	secret := RenderEnvironmentSecret("hello-world", "production",
		"0198f2f4-0000-7000-8000-000000000001",
		"6ee3b68d021fb92ebccc3ea7c5bfab6c88d85dae5970aa5c92a7a74e99b2cef2", data)
	require.Equal(t, EnvironmentSecretName, secret.Name)
	require.Equal(t, "skali-0198f2f4-0000-7000-8000-000000000001", secret.Namespace)
	require.Equal(t, corev1.SecretTypeOpaque, secret.Type)
	require.Equal(t, data, secret.Data)
	require.Equal(t, "6ee3b68d021fb92e", secret.Labels[LabelRevision])
	require.Equal(t, "true", secret.Labels[LabelManaged])
}

// The environment Secret carries one composed, fully resolved entry per
// application env var that references project values: concatenation and
// inline defaults resolve here, pure literals and service outputs stay out.
func TestEnvironmentSecretData(t *testing.T) {
	t.Parallel()
	document, err := manifest.Parse([]byte(`
version: "1"
name: composed
applications:
  web:
    image: example.invalid/web:1
    environment:
      NODE_ENV: production
      DATABASE_URL: "postgres://app:${DB_PASSWORD}@db:5432/app"
      SESSION_SECRET: "${SESSION_SECRET}"
      LOG_LEVEL: "${LOG_LEVEL:-info}"
      POSTGRES_HOST: "{{databases.data.host}}"
databases:
  data:
    engine: postgres
    version: 17
`), "skali.yml")
	require.NoError(t, err)
	result, err := compiler.Compile(document)
	require.NoError(t, err)

	data, err := EnvironmentSecretData(result.Definition, map[string]string{
		"DB_PASSWORD":    "hunter2-plant-value",
		"SESSION_SECRET": "",
	})
	require.NoError(t, err)
	require.Equal(t, map[string][]byte{
		"web.DATABASE_URL":   []byte("postgres://app:hunter2-plant-value@db:5432/app"),
		"web.SESSION_SECRET": []byte(""),
		"web.LOG_LEVEL":      []byte("info"),
	}, data)

	// A missing variable without a default fails with the name only.
	_, err = EnvironmentSecretData(result.Definition, map[string]string{"SESSION_SECRET": "x"})
	require.ErrorContains(t, err, "environment web.DATABASE_URL")
	require.ErrorContains(t, err, "DB_PASSWORD")
	require.NotContains(t, err.Error(), "hunter2")
}

// Environment variables referencing project values bind to their composed
// Secret entry; outputs and literals keep their bindings.
func TestRenderEnvironmentBindings(t *testing.T) {
	t.Parallel()
	document, err := manifest.Parse([]byte(`
version: "1"
name: bindings
applications:
  web:
    image: example.invalid/web:1
    environment:
      NODE_ENV: production
      DATABASE_URL: "postgres://app:${DB_PASSWORD}@db:5432/app"
      POSTGRES_HOST: "{{databases.data.host}}"
databases:
  data:
    engine: postgres
    version: 17
`), "skali.yml")
	require.NoError(t, err)
	result, err := compiler.Compile(document)
	require.NoError(t, err)

	variables := renderEnvironment("web", result.Definition.Applications["web"].Environment, EnvironmentSecretName)
	byName := map[string]corev1.EnvVar{}
	for _, variable := range variables {
		byName[variable.Name] = variable
	}
	require.Equal(t, "production", byName["NODE_ENV"].Value)
	require.Equal(t, "web.DATABASE_URL", byName["DATABASE_URL"].ValueFrom.SecretKeyRef.Key)
	require.Equal(t, EnvironmentSecretName, byName["DATABASE_URL"].ValueFrom.SecretKeyRef.Name)
	require.Equal(t, "host", byName["POSTGRES_HOST"].ValueFrom.SecretKeyRef.Key)
	require.Equal(t, OutputSecretName("databases", "data"), byName["POSTGRES_HOST"].ValueFrom.SecretKeyRef.Name)
}

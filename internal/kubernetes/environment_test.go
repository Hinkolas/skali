package kubernetes

import (
	"testing"

	"github.com/stretchr/testify/require"
	corev1 "k8s.io/api/core/v1"
)

func TestRenderNamespace(t *testing.T) {
	t.Parallel()
	namespace := RenderNamespace("hello-world", "production", "0198f2f4-0000-7000-8000-000000000001")
	require.Equal(t, "skali-hello-world-production", namespace.Name)
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
	require.Equal(t, "skali-hello-world-production", secret.Namespace)
	require.Equal(t, corev1.SecretTypeOpaque, secret.Type)
	require.Equal(t, data, secret.Data)
	require.Equal(t, "6ee3b68d021fb92e", secret.Labels[LabelRevision])
	require.Equal(t, "true", secret.Labels[LabelManaged])
}

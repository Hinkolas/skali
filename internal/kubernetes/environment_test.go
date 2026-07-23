package kubernetes

import (
	"encoding/base64"
	"encoding/json"
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

func TestRenderPullSecret(t *testing.T) {
	t.Parallel()
	secret := RenderPullSecret("hello-world", "production",
		"0198f2f4-0000-7000-8000-000000000001", "6ee3b68d021fb92e",
		"registry.example.com", "skali-node", "node-secret")
	require.Equal(t, PullSecretName, secret.Name)
	require.Equal(t, "skali-hello-world-production", secret.Namespace)
	require.Equal(t, corev1.SecretTypeDockerConfigJson, secret.Type)
	require.Equal(t, "true", secret.Labels[LabelManaged])
	require.Equal(t, "6ee3b68d021fb92e", secret.Labels[LabelRevision])

	var config struct {
		Auths map[string]struct {
			Username, Password, Auth string
		} `json:"auths"`
	}
	require.NoError(t, json.Unmarshal(secret.Data[corev1.DockerConfigJsonKey], &config))
	entry, ok := config.Auths["registry.example.com"]
	require.True(t, ok)
	require.Equal(t, "skali-node", entry.Username)
	require.Equal(t, "node-secret", entry.Password)
	require.Equal(t, base64.StdEncoding.EncodeToString([]byte("skali-node:node-secret")), entry.Auth)
}

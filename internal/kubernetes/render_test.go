package kubernetes

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/require"
	appsv1 "k8s.io/api/apps/v1"
	corev1 "k8s.io/api/core/v1"

	"github.com/Hinkolas/skali/internal/compiler"
	"github.com/Hinkolas/skali/internal/manifest"
)

func TestRenderHelloWorldGolden(t *testing.T) {
	t.Parallel()
	document, err := manifest.ParseFile(filepath.Join("..", "..", "examples", "hello-world", "skali.yml"))
	require.NoError(t, err)
	result, err := compiler.Compile(document)
	require.NoError(t, err)

	objects, err := Render(result, Options{
		Namespace: "skali-hello-world",
		Variables: map[string]string{"APP_DOMAIN": "hello.localhost"},
	})
	require.NoError(t, err)
	require.Len(t, objects, 3)
	require.Equal(t, "Deployment", objects[0].GetObjectKind().GroupVersionKind().Kind)
	require.Equal(t, "Service", objects[1].GetObjectKind().GroupVersionKind().Kind)
	require.Equal(t, "Ingress", objects[2].GetObjectKind().GroupVersionKind().Kind)

	actual, err := MarshalYAML(objects)
	require.NoError(t, err)
	expected, err := os.ReadFile(filepath.Join("testdata", "hello-world.golden.yaml"))
	require.NoError(t, err)
	require.Equal(t, string(expected), string(actual))
}

func TestBuildApplicationRequiresPreparedArtifact(t *testing.T) {
	t.Parallel()
	document, err := manifest.ParseFile(filepath.Join("..", "..", "examples", "file-sharing", "skali.yml"))
	require.NoError(t, err)
	result, err := compiler.Compile(document)
	require.NoError(t, err)

	_, err = Render(result, Options{
		Namespace: "skali-file-sharing",
		Variables: map[string]string{
			"APP_DOMAIN":     "files.localhost",
			"SESSION_SECRET": "test-only",
		},
	})
	require.ErrorContains(t, err, "build source has no prepared image")
}

func TestRenderBuildApplicationWithManagedOutputs(t *testing.T) {
	t.Parallel()
	document, err := manifest.ParseFile(filepath.Join("..", "..", "examples", "file-sharing", "skali.yml"))
	require.NoError(t, err)
	result, err := compiler.Compile(document)
	require.NoError(t, err)

	objects, err := Render(result, Options{
		Namespace: "skali-file-sharing",
		Variables: map[string]string{
			"APP_DOMAIN":     "files.localhost",
			"SESSION_SECRET": "test-only",
		},
		BuildImages: map[string]string{"web": "registry.local/web@sha256:test"},
	})
	require.NoError(t, err)
	require.Len(t, objects, 4)
	deployment, ok := objects[0].(*appsv1.Deployment)
	require.True(t, ok)
	require.Nil(t, deployment.Spec.Replicas, "the HPA must exclusively own Deployment.spec.replicas")
	require.Len(t, deployment.Spec.Template.Spec.TopologySpreadConstraints, 1)
	require.Nil(t, deployment.Spec.Template.Spec.TopologySpreadConstraints[0].MinDomains,
		"preferred spreading must not emit Kubernetes' DoNotSchedule-only minDomains")

	environment := deployment.Spec.Template.Spec.Containers[0].Env
	require.Contains(t, environment, environmentVariableFromSecret("POSTGRES_HOST", "skali-output-databases-data", "host"))
	require.Contains(t, environment, environmentVariableFromSecret("S3_ENDPOINT", "skali-output-buckets-files", "endpoint"))
}

func TestRenderVolumeBackedApplicationUsesRecreate(t *testing.T) {
	t.Parallel()
	document, err := manifest.Parse([]byte(`
version: "1"
name: volume-rollout
applications:
  api:
    image: example.invalid/api:1
    volumes:
      data:
        mountPath: /data
        size: 1GB
`), "skali.yml")
	require.NoError(t, err)
	result, err := compiler.Compile(document)
	require.NoError(t, err)

	objects, err := Render(result, Options{Namespace: "skali-volume-rollout"})
	require.NoError(t, err)
	require.Len(t, objects, 2)

	deployment, ok := objects[1].(*appsv1.Deployment)
	require.True(t, ok)
	require.Equal(t, appsv1.RecreateDeploymentStrategyType, deployment.Spec.Strategy.Type)
	require.Nil(t, deployment.Spec.Strategy.RollingUpdate)
}

func environmentVariableFromSecret(variable, secret, key string) corev1.EnvVar {
	return corev1.EnvVar{
		Name: variable,
		ValueFrom: &corev1.EnvVarSource{SecretKeyRef: &corev1.SecretKeySelector{
			LocalObjectReference: corev1.LocalObjectReference{Name: secret},
			Key:                  key,
		}},
	}
}

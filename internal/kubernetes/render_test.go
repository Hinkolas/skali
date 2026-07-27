package kubernetes

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/require"
	appsv1 "k8s.io/api/apps/v1"
	corev1 "k8s.io/api/core/v1"
	networkingv1 "k8s.io/api/networking/v1"

	"github.com/Hinkolas/skali/internal/compiler"
	"github.com/Hinkolas/skali/internal/layout"
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
		// The example is build-sourced; rendering always receives the
		// prepared digest-pinned image.
		BuildImages: map[string]string{
			"web": "localhost:5510/skali/hello-world/web@sha256:1111111111111111111111111111111111111111111111111111111111111111",
		},
	})
	require.NoError(t, err)
	require.Len(t, objects, 3)
	require.Equal(t, "Deployment", objects[0].GetObjectKind().GroupVersionKind().Kind)
	require.Equal(t, "Service", objects[1].GetObjectKind().GroupVersionKind().Kind)
	require.Equal(t, "Ingress", objects[2].GetObjectKind().GroupVersionKind().Kind)

	actual, err := MarshalYAML(objects)
	require.NoError(t, err)
	golden := filepath.Join("testdata", "hello-world.golden.yaml")
	// Refresh deliberately with UPDATE_GOLDEN=1 go test ./internal/kubernetes.
	if os.Getenv("UPDATE_GOLDEN") != "" {
		require.NoError(t, os.WriteFile(golden, actual, 0o644))
	}
	expected, err := os.ReadFile(golden)
	require.NoError(t, err)
	require.Equal(t, string(expected), string(actual))
}

func TestRenderClusterPlacementOptions(t *testing.T) {
	t.Parallel()
	document, err := manifest.ParseFile(filepath.Join("..", "..", "examples", "hello-world", "skali.yml"))
	require.NoError(t, err)
	result, err := compiler.Compile(document)
	require.NoError(t, err)

	// Local dev: no pull secret, no capability placement, traefik routes.
	managed, err := Render(result, Options{
		Namespace:   "skali-hello-world",
		Variables:   map[string]string{"APP_DOMAIN": "hello.localhost"},
		BuildImages: map[string]string{"web": "localhost:5510/skali/hello-world/web@sha256:2222222222222222222222222222222222222222222222222222222222222222"},
	})
	require.NoError(t, err)
	require.Nil(t, managed[0].(*appsv1.Deployment).Spec.Template.Spec.ImagePullSecrets)
	require.Equal(t, "traefik", *managed[2].(*networkingv1.Ingress).Spec.IngressClassName)

	managed, err = Render(result, Options{
		Namespace: "skali-hello-world", ManagedCluster: true,
		Variables: map[string]string{"APP_DOMAIN": "hello.localhost"},
		BuildImages: map[string]string{
			"web": "localhost:5510/skali/hello-world/web@sha256:3333333333333333333333333333333333333333333333333333333333333333",
		},
	})
	require.NoError(t, err)
	require.Equal(t, map[string]string{
		layout.CapabilityLabel(layout.CapabilityApplication): layout.CapabilityLabelValue,
	}, managed[0].(*appsv1.Deployment).Spec.Template.Spec.NodeSelector)
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

// Selector labels are baked into immutable Kubernetes selectors, so two
// revisions of the same application must produce byte-identical selectors
// while their object labels carry the changing revision identity. Breaking
// this makes every second deploy fail on selector immutability.
func TestRenderSelectorStableAcrossRevisions(t *testing.T) {
	t.Parallel()
	document, err := manifest.ParseFile(filepath.Join("..", "..", "examples", "hello-world", "skali.yml"))
	require.NoError(t, err)
	result, err := compiler.Compile(document)
	require.NoError(t, err)

	render := func(checksum string) *appsv1.Deployment {
		objects, err := Render(result, Options{
			Namespace: "skali-hello-world",
			Variables: map[string]string{"APP_DOMAIN": "hello.localhost"},
			BuildImages: map[string]string{
				"web": "localhost:5510/skali/hello-world/web@sha256:1111111111111111111111111111111111111111111111111111111111111111",
			},
			EnvironmentID:    "0198f2f4-0000-7000-8000-000000000001",
			RevisionChecksum: checksum,
		})
		require.NoError(t, err)
		deployment, ok := objects[0].(*appsv1.Deployment)
		require.True(t, ok)
		return deployment
	}

	first := render("6ee3b68d021fb92ebccc3ea7c5bfab6c88d85dae5970aa5c92a7a74e99b2cef2")
	second := render("2a91a76be9e54c04a85c2c115e72066b63a07f917fbb633a562305ab3315f035")

	require.Equal(t, first.Spec.Selector, second.Spec.Selector)
	for _, label := range []string{LabelEnvironment, LabelRevision, LabelService} {
		require.NotContains(t, first.Spec.Selector.MatchLabels, label)
	}
	require.Equal(t, "6ee3b68d021fb92e", first.Labels[LabelRevision])
	require.Equal(t, "2a91a76be9e54c04", second.Labels[LabelRevision])

	// Pod template labels must satisfy the selector and carry the identity.
	for name, value := range first.Spec.Selector.MatchLabels {
		require.Equal(t, value, first.Spec.Template.Labels[name])
	}
	require.Equal(t, first.Labels[LabelRevision], first.Spec.Template.Labels[LabelRevision])
	require.Equal(t, first.Labels[LabelEnvironment], first.Spec.Template.Labels[LabelEnvironment])
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

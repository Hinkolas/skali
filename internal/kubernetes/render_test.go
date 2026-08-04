package kubernetes

import (
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
	appsv1 "k8s.io/api/apps/v1"
	batchv1 "k8s.io/api/batch/v1"
	corev1 "k8s.io/api/core/v1"
	networkingv1 "k8s.io/api/networking/v1"
	"k8s.io/apimachinery/pkg/runtime"

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

// A forced deployment's restart stamp becomes a pod-template annotation so
// the workload rolls; without a stamp the template carries no annotations at
// all, so existing objects do not change shape.
func TestRenderRestartStampAnnotation(t *testing.T) {
	t.Parallel()
	document, err := manifest.ParseFile(filepath.Join("..", "..", "examples", "hello-world", "skali.yml"))
	require.NoError(t, err)
	result, err := compiler.Compile(document)
	require.NoError(t, err)
	options := Options{
		Namespace:   "skali-hello-world",
		Variables:   map[string]string{"APP_DOMAIN": "hello.localhost"},
		BuildImages: map[string]string{"web": "localhost:5510/skali/hello-world/web@sha256:4444444444444444444444444444444444444444444444444444444444444444"},
	}

	objects, err := Render(result, options)
	require.NoError(t, err)
	require.Nil(t, objects[0].(*appsv1.Deployment).Spec.Template.Annotations)

	options.RestartedAt = "2026-07-29T12:00:00Z"
	objects, err = Render(result, options)
	require.NoError(t, err)
	require.Equal(t, map[string]string{AnnotationRestartedAt: "2026-07-29T12:00:00Z"},
		objects[0].(*appsv1.Deployment).Spec.Template.Annotations)
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
	require.Len(t, objects, 5)
	release, ok := objects[0].(*batchv1.Job)
	require.True(t, ok, "the example's releaseCommand renders a Job ahead of the workload")
	require.Equal(t, []string{"/app/file-sharing", "migrate", "up"},
		release.Spec.Template.Spec.Containers[0].Args)
	deployment, ok := objects[1].(*appsv1.Deployment)
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

	// Pod template labels must satisfy the selector but never carry the
	// revision: a template that changes with the checksum would roll every
	// application on every revision. Two renders differing only in checksum
	// must therefore produce identical templates.
	for name, value := range first.Spec.Selector.MatchLabels {
		require.Equal(t, value, first.Spec.Template.Labels[name])
	}
	require.NotContains(t, first.Spec.Template.Labels, LabelRevision)
	require.Equal(t, first.Labels[LabelEnvironment], first.Spec.Template.Labels[LabelEnvironment])
	require.Equal(t, first.Spec.Template, second.Spec.Template)
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

// A release command renders as a per-revision single-attempt Job with the
// manifest timeout as its deadline, and its pods must never match the
// application's immutable selectors: a migration pod receiving route
// traffic would be a production incident.
func TestRenderReleaseJob(t *testing.T) {
	t.Parallel()
	render := func(t *testing.T, releaseYAML string) []runtime.Object {
		t.Helper()
		document, err := manifest.Parse([]byte(`
version: "1"
name: shop
applications:
  web:
    image: example.invalid/web:1
    ports:
      http:
        port: 3000
    deployment:
      releaseCommand:
`+releaseYAML), "skali.yml")
		require.NoError(t, err)
		result, err := compiler.Compile(document)
		require.NoError(t, err)
		objects, err := Render(result, Options{
			Namespace:        "skali-shop-production",
			EnvironmentID:    "0198f2f4-0000-7000-8000-000000000002",
			RevisionChecksum: "6ee3b68d021fb92ebccc3ea7c5bfab6c88d85dae5970aa5c92a7a74e99b2cef2",
		})
		require.NoError(t, err)
		return objects
	}

	objects := render(t, `
        command: ["bun", "scripts/migrate.ts"]
        timeout: 30m
`)
	var job *batchv1.Job
	var deployment *appsv1.Deployment
	var service *corev1.Service
	for _, obj := range objects {
		switch typed := obj.(type) {
		case *batchv1.Job:
			job = typed
		case *appsv1.Deployment:
			deployment = typed
		case *corev1.Service:
			service = typed
		}
	}
	require.NotNil(t, job)
	require.NotNil(t, deployment)
	require.NotNil(t, service)

	require.Equal(t, ReleaseJobName("shop", "web",
		"6ee3b68d021fb92ebccc3ea7c5bfab6c88d85dae5970aa5c92a7a74e99b2cef2"), job.Name)
	require.Equal(t, "shop-web-release-6ee3b68d021fb92e", job.Name)
	require.Equal(t, []string{"bun", "scripts/migrate.ts"}, job.Spec.Template.Spec.Containers[0].Args)
	require.Equal(t, deployment.Spec.Template.Spec.Containers[0].Image, job.Spec.Template.Spec.Containers[0].Image)
	require.Equal(t, deployment.Spec.Template.Spec.Containers[0].Env, job.Spec.Template.Spec.Containers[0].Env)
	require.Equal(t, int32(0), *job.Spec.BackoffLimit)
	require.Equal(t, int64(1800), *job.Spec.ActiveDeadlineSeconds)
	require.Equal(t, corev1.RestartPolicyNever, job.Spec.Template.Spec.RestartPolicy)

	// The Job object belongs to the application's service; its pods carry a
	// distinct identity for observation.
	require.Equal(t, "web", job.Labels[LabelService])
	require.Equal(t, ReleaseServiceIdentity("web"), job.Spec.Template.Labels[LabelService])
	require.Equal(t, "true", job.Spec.Template.Labels[LabelManaged])
	require.Equal(t, "0198f2f4-0000-7000-8000-000000000002", job.Spec.Template.Labels[LabelEnvironment])
	matchesSelector := true
	for name, value := range service.Spec.Selector {
		if job.Spec.Template.Labels[name] != value {
			matchesSelector = false
		}
	}
	require.False(t, matchesSelector, "release pods must not match the application's Service selector")

	// Without an explicit timeout the default bounds the Job.
	objects = render(t, `
        command: ["/bin/migrate"]
`)
	for _, obj := range objects {
		if typed, ok := obj.(*batchv1.Job); ok {
			require.Equal(t, int64(DefaultReleaseTimeout/time.Second), *typed.Spec.ActiveDeadlineSeconds)
		}
	}
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

// The pod template carries a values identity instead of the revision label:
// changing a referenced value rolls exactly the referencing applications,
// secret variables contribute their stored version rather than plaintext,
// and release Jobs (immutable, per-revision named) never carry it.
func TestRenderValuesIdentity(t *testing.T) {
	t.Parallel()
	document, err := manifest.Parse([]byte(`
version: "1"
name: identity
applications:
  web:
    image: example.invalid/web:1
    ports:
      http:
        port: 3000
    environment:
      APP_DOMAIN: ${APP_DOMAIN}
      SESSION_SECRET: ${SESSION_SECRET}
    deployment:
      releaseCommand:
        command: ["/bin/migrate"]
  worker:
    image: example.invalid/worker:1
    environment:
      UNUSED: ${UNUSED}
  cron:
    image: example.invalid/cron:1
`), "skali.yml")
	require.NoError(t, err)
	result, err := compiler.Compile(document)
	require.NoError(t, err)

	deployments := map[string]*appsv1.Deployment{}
	var release *batchv1.Job
	render := func(t *testing.T, options Options) {
		t.Helper()
		options.Namespace = "skali-identity"
		options.EnvironmentID = "0198f2f4-0000-7000-8000-000000000003"
		options.RevisionChecksum = "6ee3b68d021fb92ebccc3ea7c5bfab6c88d85dae5970aa5c92a7a74e99b2cef2"
		objects, err := Render(result, options)
		require.NoError(t, err)
		deployments = map[string]*appsv1.Deployment{}
		release = nil
		for _, obj := range objects {
			switch typed := obj.(type) {
			case *appsv1.Deployment:
				deployments[typed.Labels[LabelApplication]] = typed
			case *batchv1.Job:
				release = typed
			}
		}
		require.Len(t, deployments, 3)
		require.NotNil(t, release)
	}
	valuesHash := func(application string) string {
		return deployments[application].Spec.Template.Annotations[AnnotationValuesHash]
	}

	base := Options{
		Variables:      map[string]string{"APP_DOMAIN": "id.localhost", "SESSION_SECRET": "plaintext-one", "UNUSED": "0"},
		SecretVersions: map[string]int{"APP_DOMAIN": 1, "SESSION_SECRET": 1, "UNUSED": 1},
	}
	render(t, base)

	// An application referencing no project variables renders without any
	// template annotations at all.
	require.Nil(t, deployments["cron"].Spec.Template.Annotations)

	webHash, workerHash := valuesHash("web"), valuesHash("worker")
	require.Len(t, webHash, 16)
	require.Len(t, workerHash, 16)
	releaseTemplate := release.Spec.Template

	// A changed value (a new stored generation) rolls exactly the
	// applications referencing it.
	changed := base
	changed.SecretVersions = map[string]int{"APP_DOMAIN": 1, "SESSION_SECRET": 1, "UNUSED": 2}
	render(t, changed)
	require.Equal(t, webHash, valuesHash("web"))
	require.NotEqual(t, workerHash, valuesHash("worker"))

	// A different plaintext at the same version never enters the hash: only
	// version bumps roll pods.
	rotated := base
	rotated.SecretVersions = map[string]int{"APP_DOMAIN": 1, "SESSION_SECRET": 2, "UNUSED": 1}
	render(t, rotated)
	require.NotEqual(t, webHash, valuesHash("web"))
	replaintexted := base
	replaintexted.Variables = map[string]string{"APP_DOMAIN": "id.localhost", "SESSION_SECRET": "plaintext-two", "UNUSED": "0"}
	render(t, replaintexted)
	require.Equal(t, webHash, valuesHash("web"))

	// The restart stamp and the values identity share one annotations map.
	stamped := base
	stamped.RestartedAt = "2026-08-02T00:00:00Z"
	render(t, stamped)
	require.Equal(t, webHash, valuesHash("web"))
	require.Equal(t, "2026-08-02T00:00:00Z",
		deployments["web"].Spec.Template.Annotations[AnnotationRestartedAt])
	require.Equal(t, map[string]string{AnnotationRestartedAt: "2026-08-02T00:00:00Z"},
		deployments["cron"].Spec.Template.Annotations)

	// Release Job pods keep the revision label (the Job is per-revision and
	// immutable) and never carry the values identity.
	require.NotContains(t, releaseTemplate.Annotations, AnnotationValuesHash)
	require.Equal(t, "6ee3b68d021fb92e", releaseTemplate.Labels[LabelRevision])

	// Application pod templates never carry the revision label.
	render(t, base)
	for _, deployment := range deployments {
		require.NotContains(t, deployment.Spec.Template.Labels, LabelRevision)
	}
}

// The rollout deadline mirrors onto rendered Deployments and ReplicaSet
// history stays bounded; offline rendering leaves the deadline to the
// Kubernetes default.
func TestRenderProgressDeadlineAndHistoryLimit(t *testing.T) {
	t.Parallel()
	document, err := manifest.Parse([]byte(`
version: "1"
name: deadline
applications:
  api:
    image: example.invalid/api:1
`), "skali.yml")
	require.NoError(t, err)
	result, err := compiler.Compile(document)
	require.NoError(t, err)

	objects, err := Render(result, Options{Namespace: "skali-deadline"})
	require.NoError(t, err)
	deployment, ok := objects[0].(*appsv1.Deployment)
	require.True(t, ok)
	require.Nil(t, deployment.Spec.ProgressDeadlineSeconds)
	require.Equal(t, int32(3), *deployment.Spec.RevisionHistoryLimit)

	objects, err = Render(result, Options{Namespace: "skali-deadline", ProgressDeadlineSeconds: 600})
	require.NoError(t, err)
	deployment, ok = objects[0].(*appsv1.Deployment)
	require.True(t, ok)
	require.Equal(t, int32(600), *deployment.Spec.ProgressDeadlineSeconds)
}

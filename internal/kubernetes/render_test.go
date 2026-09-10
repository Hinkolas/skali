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
	discoveryv1 "k8s.io/api/discovery/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/labels"
	"k8s.io/apimachinery/pkg/runtime"

	"github.com/Hinkolas/skali/internal/compiler"
	"github.com/Hinkolas/skali/internal/edge"
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
		// The golden freezes the TLS-capable shape: the shared redirect
		// Middleware, both routers, and the explicit Certificate.
		Certificates: true,
		// The kernel always names a class; the golden freezes the normal one.
		PriorityClassName: layout.PriorityClassNormal,
	})
	require.NoError(t, err)
	kinds := make([]string, 0, len(objects))
	for _, object := range objects {
		kinds = append(kinds, object.GetObjectKind().GroupVersionKind().Kind)
	}
	require.Equal(t, []string{"Middleware", "Deployment", "Service",
		"IngressRoute", "IngressRoute", "Certificate"}, kinds)

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

	// Local dev: no pull secret, no capability placement, and the HTTP-only
	// edge shape (a single web-entrypoint IngressRoute, no Certificate, no
	// redirect Middleware) even though the route declares tls: automatic.
	managed, err := Render(result, Options{
		Namespace:   "skali-hello-world",
		Variables:   map[string]string{"APP_DOMAIN": "hello.localhost"},
		BuildImages: map[string]string{"web": "localhost:5510/skali/hello-world/web@sha256:2222222222222222222222222222222222222222222222222222222222222222"},
	})
	require.NoError(t, err)
	require.Len(t, managed, 3)
	require.Nil(t, managed[0].(*appsv1.Deployment).Spec.Template.Spec.ImagePullSecrets)
	route := managed[2].(*unstructured.Unstructured)
	require.Equal(t, edge.IngressRouteGVK, route.GroupVersionKind())
	points, _, err := unstructured.NestedStringSlice(route.Object, "spec", "entryPoints")
	require.NoError(t, err)
	require.Equal(t, []string{edge.EntryPointWeb}, points)
	_, hasTLS, err := unstructured.NestedMap(route.Object, "spec", "tls")
	require.NoError(t, err)
	require.False(t, hasTLS, "the local edge never terminates TLS")

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
	require.Nil(t, managed[0].(*appsv1.Deployment).Spec.Template.Spec.Affinity,
		"an unknown platform set renders no arch constraint")
}

// Artifact platforms become a required kubernetes.io/arch affinity on
// managed clusters: pods never schedule where their image cannot run.
// Unmanaged clusters and unknown platform sets render no constraint.
func TestRenderArchAffinity(t *testing.T) {
	t.Parallel()
	document, err := manifest.ParseFile(filepath.Join("..", "..", "examples", "hello-world", "skali.yml"))
	require.NoError(t, err)
	result, err := compiler.Compile(document)
	require.NoError(t, err)
	render := func(managedCluster bool, platforms map[string][]string) []runtime.Object {
		objects, err := Render(result, Options{
			Namespace:      "skali-hello-world",
			ManagedCluster: managedCluster,
			Variables:      map[string]string{"APP_DOMAIN": "hello.localhost"},
			BuildImages:    map[string]string{"web": "registry.local/web@sha256:test"},
			AppPlatforms:   platforms,
		})
		require.NoError(t, err)
		return objects
	}

	pinned := render(true, map[string][]string{"web": {"linux/arm64"}})
	affinity := pinned[0].(*appsv1.Deployment).Spec.Template.Spec.Affinity
	require.NotNil(t, affinity)
	terms := affinity.NodeAffinity.RequiredDuringSchedulingIgnoredDuringExecution.NodeSelectorTerms
	require.Len(t, terms, 1)
	require.Equal(t, []corev1.NodeSelectorRequirement{{
		Key: corev1.LabelArchStable, Operator: corev1.NodeSelectorOpIn, Values: []string{"arm64"},
	}}, terms[0].MatchExpressions)

	multi := render(true, map[string][]string{"web": {"linux/arm64", "linux/amd64"}})
	terms = multi[0].(*appsv1.Deployment).Spec.Template.Spec.Affinity.
		NodeAffinity.RequiredDuringSchedulingIgnoredDuringExecution.NodeSelectorTerms
	require.Equal(t, []string{"amd64", "arm64"}, terms[0].MatchExpressions[0].Values,
		"archs are sorted so equal sets render identically")

	require.Nil(t, render(false, map[string][]string{"web": {"linux/arm64"}})[0].(*appsv1.Deployment).
		Spec.Template.Spec.Affinity, "unmanaged clusters never render the constraint")
	require.Nil(t, render(true, nil)[0].(*appsv1.Deployment).Spec.Template.Spec.Affinity)
}

// The release Job carries the same arch affinity as the Deployment: the
// release command runs from the same image.
func TestRenderArchAffinityReleaseJob(t *testing.T) {
	t.Parallel()
	document, err := manifest.ParseFile(filepath.Join("..", "..", "examples", "file-sharing", "skali.yml"))
	require.NoError(t, err)
	result, err := compiler.Compile(document)
	require.NoError(t, err)
	objects, err := Render(result, Options{
		Namespace:      "skali-file-sharing",
		ManagedCluster: true,
		Variables:      map[string]string{"APP_DOMAIN": "files.localhost", "SESSION_SECRET": "test-only"},
		BuildImages:    map[string]string{"web": "registry.local/web@sha256:test"},
		AppPlatforms:   map[string][]string{"web": {"linux/amd64"}},
	})
	require.NoError(t, err)
	job := objects[0].(*batchv1.Job)
	require.NotNil(t, job.Spec.Template.Spec.Affinity)
	values := job.Spec.Template.Spec.Affinity.NodeAffinity.
		RequiredDuringSchedulingIgnoredDuringExecution.NodeSelectorTerms[0].MatchExpressions[0].Values
	require.Equal(t, []string{"amd64"}, values)
}

// The environment's priority reaches every application pod as its
// PriorityClass: Deployments and release Jobs alike. Without a class
// (offline rendering) the field stays absent.
func TestRenderPriorityClass(t *testing.T) {
	t.Parallel()
	document, err := manifest.ParseFile(filepath.Join("..", "..", "examples", "file-sharing", "skali.yml"))
	require.NoError(t, err)
	result, err := compiler.Compile(document)
	require.NoError(t, err)
	render := func(class string) []runtime.Object {
		objects, err := Render(result, Options{
			Namespace:         "skali-file-sharing",
			Variables:         map[string]string{"APP_DOMAIN": "files.localhost", "SESSION_SECRET": "test-only"},
			BuildImages:       map[string]string{"web": "registry.local/web@sha256:test"},
			PriorityClassName: class,
		})
		require.NoError(t, err)
		return objects
	}
	high := render(layout.PriorityClassHigh)
	require.Equal(t, layout.PriorityClassHigh, high[0].(*batchv1.Job).Spec.Template.Spec.PriorityClassName)
	require.Equal(t, layout.PriorityClassHigh, high[1].(*appsv1.Deployment).Spec.Template.Spec.PriorityClassName)
	plain := render("")
	require.Empty(t, plain[0].(*batchv1.Job).Spec.Template.Spec.PriorityClassName)
	require.Empty(t, plain[1].(*appsv1.Deployment).Spec.Template.Spec.PriorityClassName)
}

// The three TLS policies and the strategy knob shape the edge objects: an
// automatic route redirects plain HTTP, an optional route serves it, a
// disabled route never leaves the web entrypoint, and least-requests becomes
// Traefik's p2c on the backend reference.
func TestRenderRoutePolicies(t *testing.T) {
	t.Parallel()
	document, err := manifest.Parse([]byte(`
version: "1"
name: policies
applications:
  api:
    image: example.invalid/api:1
    ports:
      http:
        port: 8080
    routes:
      public:
        domain: api.example.com
        port: http
        strategy: least-requests
      relaxed:
        domain: relaxed.example.com
        port: http
        tls: optional
      internal:
        domain: internal.example.com
        port: http
        tls: disabled
`), "skali.yml")
	require.NoError(t, err)
	result, err := compiler.Compile(document)
	require.NoError(t, err)

	objects, err := Render(result, Options{Namespace: "skali-policies", Certificates: true})
	require.NoError(t, err)
	byName := map[string]*unstructured.Unstructured{}
	for _, object := range objects {
		if typed, ok := object.(*unstructured.Unstructured); ok {
			byName[typed.GetKind()+"/"+typed.GetName()] = typed
		}
	}

	// tls automatic + least-requests: websecure router with p2c, redirecting
	// http companion, certificate.
	public := byName["IngressRoute/"+RouteName("policies", "api", "public", "primary")]
	require.NotNil(t, public)
	routes, _, err := unstructured.NestedSlice(public.Object, "spec", "routes")
	require.NoError(t, err)
	service := routes[0].(map[string]any)["services"].([]any)[0].(map[string]any)
	require.Equal(t, "p2c", service["strategy"])
	secret, _, err := unstructured.NestedString(public.Object, "spec", "tls", "secretName")
	require.NoError(t, err)
	require.Equal(t, RouteTLSName("policies", "api", "public"), secret)
	publicHTTP := byName["IngressRoute/"+RouteName("policies", "api", "public", "http")]
	require.NotNil(t, publicHTTP)
	httpRoutes, _, err := unstructured.NestedSlice(publicHTTP.Object, "spec", "routes")
	require.NoError(t, err)
	require.Contains(t, httpRoutes[0].(map[string]any)["match"], `!PathPrefix("/.well-known/acme-challenge/")`)
	require.Contains(t, httpRoutes[0].(map[string]any), "middlewares",
		"automatic routes redirect plain HTTP")
	require.NotNil(t, byName["Certificate/"+RouteTLSName("policies", "api", "public")])
	require.NotNil(t, byName["Middleware/redirect-https"])

	// tls optional: both routers serve, no redirect.
	relaxedHTTP := byName["IngressRoute/"+RouteName("policies", "api", "relaxed", "http")]
	require.NotNil(t, relaxedHTTP)
	relaxedRoutes, _, err := unstructured.NestedSlice(relaxedHTTP.Object, "spec", "routes")
	require.NoError(t, err)
	require.Contains(t, relaxedRoutes[0].(map[string]any)["match"], `!PathPrefix("/.well-known/acme-challenge/")`)
	require.NotContains(t, relaxedRoutes[0].(map[string]any), "middlewares",
		"optional routes keep serving plain HTTP")
	require.NotNil(t, byName["Certificate/"+RouteTLSName("policies", "api", "relaxed")])

	// tls disabled: one web router, no certificate, default strategy.
	internal := byName["IngressRoute/"+RouteName("policies", "api", "internal", "primary")]
	require.NotNil(t, internal)
	points, _, err := unstructured.NestedStringSlice(internal.Object, "spec", "entryPoints")
	require.NoError(t, err)
	require.Equal(t, []string{edge.EntryPointWeb}, points)
	require.Nil(t, byName["IngressRoute/"+RouteName("policies", "api", "internal", "http")])
	require.Nil(t, byName["Certificate/"+RouteTLSName("policies", "api", "internal")])
	internalRoutes, _, err := unstructured.NestedSlice(internal.Object, "spec", "routes")
	require.NoError(t, err)
	internalService := internalRoutes[0].(map[string]any)["services"].([]any)[0].(map[string]any)
	require.NotContains(t, internalService, "strategy")

	// RouteTLSName mirrors the renderer's composition.
	require.Equal(t, RouteTLSName("policies", "api", "public"), RouteTLSName("policies", "api", "public"))
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
	require.Contains(t, environment, environmentVariableFromSecret("POSTGRES_HOST", OutputSecretName("databases", "data"), "host"))
	require.Contains(t, environment, environmentVariableFromSecret("S3_ENDPOINT", OutputSecretName("buckets", "files"), "endpoint"))
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

	// Blue-green (the default): identical templates share a color, so the
	// two revisions name the same Deployment and nothing rolls.
	require.Equal(t, first.Name, second.Name)
	require.NotEmpty(t, first.Spec.Selector.MatchLabels[LabelColor])
	require.Equal(t, first.Spec.Selector.MatchLabels[LabelColor], first.Spec.Template.Labels[LabelColor])
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

	// Without a declared storage class the claim keeps the cluster default
	// (dev and offline rendering stay byte-identical to before the field).
	claim, ok := objects[0].(*corev1.PersistentVolumeClaim)
	require.True(t, ok)
	require.Nil(t, claim.Spec.StorageClassName)

	// Managed clusters name the class explicitly.
	objects, err = Render(result, Options{Namespace: "skali-volume-rollout", StorageClass: "skali-app"})
	require.NoError(t, err)
	claim, ok = objects[0].(*corev1.PersistentVolumeClaim)
	require.True(t, ok)
	require.NotNil(t, claim.Spec.StorageClassName)
	require.Equal(t, "skali-app", *claim.Spec.StorageClassName)
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
	require.LessOrEqual(t, len(job.Name), 63)
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

// Long project and application keys make the application name maximal
// (30-byte prefix plus hash). The release pod's name label is derived
// through the same helper rather than by appending a suffix, so it stays
// within the 63-byte label limit like every other object name.
func TestRenderReleaseJobLongNamesKeepLabelsValid(t *testing.T) {
	t.Parallel()
	document, err := manifest.Parse([]byte(`
version: "1"
name: trackr-escpos
applications:
  proxy:
    image: example.invalid/proxy:1
    ports:
      http:
        port: 3000
    deployment:
      releaseCommand:
        command: ["/bin/migrate"]
`), "skali.yml")
	require.NoError(t, err)
	result, err := compiler.Compile(document)
	require.NoError(t, err)
	objects, err := Render(result, Options{
		Namespace:        "skali-trackr-escpos-production",
		EnvironmentID:    "0198f2f4-0000-7000-8000-000000000002",
		RevisionChecksum: "6ee3b68d021fb92ebccc3ea7c5bfab6c88d85dae5970aa5c92a7a74e99b2cef2",
	})
	require.NoError(t, err)

	var job *batchv1.Job
	var service *corev1.Service
	for _, obj := range objects {
		switch typed := obj.(type) {
		case *batchv1.Job:
			job = typed
		case *corev1.Service:
			service = typed
		}
	}
	require.NotNil(t, job)
	require.NotNil(t, service)

	name := job.Spec.Template.Labels["app.kubernetes.io/name"]
	require.Equal(t, objectName("release", "trackr-escpos", "proxy"), name)
	require.LessOrEqual(t, len(name), 63, "release pod name label must fit the Kubernetes label limit")
	for key, value := range job.Spec.Template.Labels {
		require.LessOrEqual(t, len(value), 63, "label %s exceeds 63 bytes", key)
	}
	require.NotEqual(t, service.Spec.Selector["app.kubernetes.io/name"], name,
		"release pods must not match the application's Service selector")
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

// An intercepted application renders no workload: Service without selector,
// a managed EndpointSlice targeting the host, and its IngressRoute;
// Deployment, HPA, and release Job are absent. A build source needs no
// prepared image.
func TestRenderInterceptedApplication(t *testing.T) {
	t.Parallel()
	document, err := manifest.ParseFile(filepath.Join("..", "..", "examples", "hello-world", "skali.yml"))
	require.NoError(t, err)
	result, err := compiler.Compile(document)
	require.NoError(t, err)

	objects, err := Render(result, Options{
		Namespace:       "skali-hello-world",
		Variables:       map[string]string{"APP_DOMAIN": "hello.localhost"},
		Intercepts:      map[string]map[string]int32{"web": {"http": 5173}},
		InterceptHostIP: "192.0.2.10",
	})
	require.NoError(t, err)
	kinds := make([]string, 0, len(objects))
	for _, object := range objects {
		kinds = append(kinds, object.GetObjectKind().GroupVersionKind().Kind)
	}
	require.Equal(t, []string{"Service", "EndpointSlice", "IngressRoute"}, kinds)

	service := objects[0].(*corev1.Service)
	require.Nil(t, service.Spec.Selector, "intercepted Services drop their selector")

	slice := objects[1].(*discoveryv1.EndpointSlice)
	require.Equal(t, objectName("intercept", "hello-world", "web"), slice.Name)
	require.Equal(t, "true", slice.Labels[LabelManaged])
	require.Equal(t, service.Name, slice.Labels["kubernetes.io/service-name"])
	require.Equal(t, "skali.dev", slice.Labels["endpointslice.kubernetes.io/managed-by"])
	// The observe informer lists exactly these slices; a controller-managed
	// slice (every Service label copied, the controller's own managed-by)
	// must stay outside the selector or the kernel prunes it every pass.
	selector, err := labels.Parse(InterceptEndpointSliceSelector)
	require.NoError(t, err)
	require.True(t, selector.Matches(labels.Set(slice.Labels)))
	controllerSlice := labels.Set{LabelManaged: "true", LabelEnvironment: "x",
		"kubernetes.io/service-name": service.Name,
		LabelEndpointSliceManagedBy:  "endpointslice-controller.k8s.io"}
	require.False(t, selector.Matches(controllerSlice))
	require.Equal(t, discoveryv1.AddressTypeIPv4, slice.AddressType)
	require.Equal(t, []string{"192.0.2.10"}, slice.Endpoints[0].Addresses)
	require.True(t, *slice.Endpoints[0].Conditions.Ready)
	require.Len(t, slice.Ports, len(service.Spec.Ports))
	for index, port := range service.Spec.Ports {
		require.Equal(t, port.Name, *slice.Ports[index].Name)
		require.EqualValues(t, 5173, *slice.Ports[index].Port)
	}

	// A missing host IP fails the render instead of publishing a dead slice.
	_, err = Render(result, Options{
		Namespace:  "skali-hello-world",
		Variables:  map[string]string{"APP_DOMAIN": "hello.localhost"},
		Intercepts: map[string]map[string]int32{"web": {"http": 5173}},
	})
	require.ErrorContains(t, err, "host gateway")
}

// Blue-green colors follow the pod template: anything that must roll the
// application (image, values identity, restart stamp, priority) yields a
// new color and therefore a new Deployment beside the old one, while the
// Service, the HPA name, and the route backends keep the application name.
func TestRenderBlueGreenColorChangesWithTemplate(t *testing.T) {
	t.Parallel()
	document, err := manifest.ParseFile(filepath.Join("..", "..", "examples", "hello-world", "skali.yml"))
	require.NoError(t, err)
	result, err := compiler.Compile(document)
	require.NoError(t, err)

	base := Options{
		Namespace: "skali-hello-world",
		Variables: map[string]string{"APP_DOMAIN": "hello.localhost"},
		BuildImages: map[string]string{
			"web": "localhost:5510/skali/hello-world/web@sha256:1111111111111111111111111111111111111111111111111111111111111111",
		},
		EnvironmentID:    "0198f2f4-0000-7000-8000-000000000001",
		RevisionChecksum: "6ee3b68d021fb92ebccc3ea7c5bfab6c88d85dae5970aa5c92a7a74e99b2cef2",
	}
	render := func(t *testing.T, options Options) (*appsv1.Deployment, *corev1.Service) {
		t.Helper()
		objects, err := Render(result, options)
		require.NoError(t, err)
		var deployment *appsv1.Deployment
		var service *corev1.Service
		for _, object := range objects {
			switch typed := object.(type) {
			case *appsv1.Deployment:
				deployment = typed
			case *corev1.Service:
				service = typed
			}
		}
		require.NotNil(t, deployment)
		require.NotNil(t, service)
		return deployment, service
	}

	deployment, service := render(t, base)
	colors, err := ApplicationColors(result, base)
	require.NoError(t, err)
	color := colors["web"]
	require.Len(t, color, 10)
	require.Equal(t, ColoredApplicationName("hello-world", "web", color), deployment.Name)
	require.Equal(t, color, deployment.Labels[LabelColor])
	require.Equal(t, color, deployment.Spec.Selector.MatchLabels[LabelColor])
	require.Equal(t, color, deployment.Spec.Template.Labels[LabelColor])
	require.Equal(t, color, deployment.Spec.Template.Spec.TopologySpreadConstraints[0].LabelSelector.MatchLabels[LabelColor])
	require.Equal(t, appsv1.RecreateDeploymentStrategyType, deployment.Spec.Strategy.Type)
	require.Equal(t, ApplicationName("hello-world", "web"), service.Name)
	require.Equal(t, color, service.Spec.Selector[LabelColor], "a converged Service selects the rendered color")
	require.NotContains(t, service.Labels, LabelColor, "the color is a selector, never a Service label")

	variants := map[string]func(*Options){
		"image": func(o *Options) {
			o.BuildImages = map[string]string{"web": "localhost:5510/skali/hello-world/web@sha256:2222222222222222222222222222222222222222222222222222222222222222"}
		},
		"restart":  func(o *Options) { o.RestartedAt = "2026-09-10T10:00:00Z" },
		"priority": func(o *Options) { o.PriorityClassName = "skali-high" },
	}
	for name, mutate := range variants {
		options := base
		mutate(&options)
		changed, changedService := render(t, options)
		require.NotEqual(t, deployment.Name, changed.Name, name)
		require.NotEqual(t, color, changed.Labels[LabelColor], name)
		require.Equal(t, service.Name, changedService.Name, name)
	}

	// The revision checksum alone changes neither the template nor the color.
	options := base
	options.RevisionChecksum = "2a91a76be9e54c04a85c2c115e72066b63a07f917fbb633a562305ab3315f035"
	same, _ := render(t, options)
	require.Equal(t, deployment.Name, same.Name)
}

// While a switch is pending the kernel pins the Service to the serving
// color; the empty string keeps the selector uncolored so a legacy or
// rolling Deployment serves until its blue-green replacement is ready.
func TestRenderTrafficColorsHold(t *testing.T) {
	t.Parallel()
	document, err := manifest.ParseFile(filepath.Join("..", "..", "examples", "hello-world", "skali.yml"))
	require.NoError(t, err)
	result, err := compiler.Compile(document)
	require.NoError(t, err)
	base := Options{
		Namespace: "skali-hello-world",
		Variables: map[string]string{"APP_DOMAIN": "hello.localhost"},
		BuildImages: map[string]string{
			"web": "localhost:5510/skali/hello-world/web@sha256:1111111111111111111111111111111111111111111111111111111111111111",
		},
	}
	serviceOf := func(t *testing.T, options Options) *corev1.Service {
		t.Helper()
		objects, err := Render(result, options)
		require.NoError(t, err)
		for _, object := range objects {
			if service, ok := object.(*corev1.Service); ok {
				return service
			}
		}
		t.Fatal("no Service rendered")
		return nil
	}

	held := base
	held.TrafficColors = map[string]string{"web": "0123456789"}
	require.Equal(t, "0123456789", serviceOf(t, held).Spec.Selector[LabelColor])

	legacy := base
	legacy.TrafficColors = map[string]string{"web": ""}
	require.NotContains(t, serviceOf(t, legacy).Spec.Selector, LabelColor)

	// Rolling applications honor the pin too: opting out of blue-green
	// keeps the colored Deployment serving until the rolling one is ready.
	rollingDoc, err := manifest.Parse([]byte(`
version: "1"
name: opt-out
applications:
  api:
    image: example.invalid/api:1
    ports:
      http:
        port: 8080
    deployment:
      rollout:
        strategy: rolling
`), "skali.yml")
	require.NoError(t, err)
	rolling, err := compiler.Compile(rollingDoc)
	require.NoError(t, err)
	objects, err := Render(rolling, Options{Namespace: "skali-opt-out", TrafficColors: map[string]string{"api": "0123456789"}})
	require.NoError(t, err)
	for _, object := range objects {
		if service, ok := object.(*corev1.Service); ok {
			require.Equal(t, "0123456789", service.Spec.Selector[LabelColor])
		}
		if deployment, ok := object.(*appsv1.Deployment); ok {
			require.Equal(t, ApplicationName("opt-out", "api"), deployment.Name)
			require.NotContains(t, deployment.Spec.Selector.MatchLabels, LabelColor)
		}
	}
}

// The manifest's rollout timeout becomes the Deployment's progress deadline,
// overriding the kernel-wide default; without it the default stands.
func TestRenderRolloutTimeoutOverridesDeadline(t *testing.T) {
	t.Parallel()
	render := func(t *testing.T, rollout string) *appsv1.Deployment {
		t.Helper()
		document, err := manifest.Parse([]byte(`
version: "1"
name: deadline
applications:
  api:
    image: example.invalid/api:1
`+rollout), "skali.yml")
		require.NoError(t, err)
		result, err := compiler.Compile(document)
		require.NoError(t, err)
		objects, err := Render(result, Options{Namespace: "skali-deadline", ProgressDeadlineSeconds: 600})
		require.NoError(t, err)
		deployment, ok := objects[0].(*appsv1.Deployment)
		require.True(t, ok)
		return deployment
	}
	require.EqualValues(t, 600, *render(t, "").Spec.ProgressDeadlineSeconds)
	require.EqualValues(t, 90, *render(t, "    deployment:\n      rollout:\n        timeout: 90s\n").Spec.ProgressDeadlineSeconds)
}

// An unknown strategy in a stored definition (a daemon older than the
// manifest vocabulary) fails the render instead of applying an invalid
// rolling update with both bounds at zero.
func TestRenderRejectsUnknownStrategy(t *testing.T) {
	t.Parallel()
	document, err := manifest.Parse([]byte(`
version: "1"
name: unknown
applications:
  api:
    image: example.invalid/api:1
`), "skali.yml")
	require.NoError(t, err)
	result, err := compiler.Compile(document)
	require.NoError(t, err)
	api := result.Definition.Applications["api"]
	api.Deployment.Rollout.Strategy = "canary"
	result.Definition.Applications["api"] = api
	_, err = Render(result, Options{Namespace: "skali-unknown"})
	require.ErrorContains(t, err, `unsupported rollout strategy "canary"`)
}

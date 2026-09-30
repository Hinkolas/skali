package seaweed

import (
	"encoding/json"
	"strings"
	"testing"

	"github.com/stretchr/testify/require"
	appsv1 "k8s.io/api/apps/v1"
	corev1 "k8s.io/api/core/v1"
	policyv1 "k8s.io/api/policy/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/runtime"
)

func TestRenderProductionShape(t *testing.T) {
	t.Parallel()
	spec := StoreSpec{Namespace: "skali-platform", Masters: 3, Replication: "001", Managed: true}
	objects := RenderProduction(spec)

	var sts *appsv1.StatefulSet
	var daemon *appsv1.DaemonSet
	var filer *appsv1.Deployment
	services := map[string]*corev1.Service{}
	for _, object := range objects {
		switch typed := object.(type) {
		case *appsv1.StatefulSet:
			sts = typed
		case *appsv1.DaemonSet:
			daemon = typed
		case *appsv1.Deployment:
			filer = typed
		case *corev1.Service:
			services[typed.Name] = typed
		}
	}

	require.NotNil(t, sts)
	require.EqualValues(t, 3, *sts.Spec.Replicas)
	require.Contains(t, sts.Spec.Template.Spec.Containers[0].Args, "-defaultReplication=001")
	require.Contains(t, sts.Spec.Template.Spec.Containers[0].Args,
		"-peers=seaweed-master-0.seaweed-master.skali-platform.svc.cluster.local:9333,"+
			"seaweed-master-1.seaweed-master.skali-platform.svc.cluster.local:9333,"+
			"seaweed-master-2.seaweed-master.skali-platform.svc.cluster.local:9333")
	require.Equal(t, "true", sts.Spec.Template.Spec.NodeSelector["skali.dev/capability-object-storage"])
	require.Equal(t, "skali-critical", sts.Spec.Template.Spec.PriorityClassName)

	require.NotNil(t, daemon)
	require.Equal(t, "skali-critical", daemon.Spec.Template.Spec.PriorityClassName)
	require.Equal(t, VolumeHostPath, daemon.Spec.Template.Spec.Volumes[0].HostPath.Path)
	require.Equal(t, "true", daemon.Spec.Template.Spec.NodeSelector["skali.dev/capability-object-storage"])

	require.NotNil(t, filer)
	require.EqualValues(t, 2, *filer.Spec.Replicas)
	require.Contains(t, filer.Spec.Template.Spec.Containers[0].Args, "-s3")
	require.Equal(t, FilerStoreSecret, filer.Spec.Template.Spec.Containers[0].EnvFrom[0].SecretRef.Name)

	// The managed label never appears: platform components must not enter
	// environment pruning or the managed informers.
	for _, object := range objects {
		switch typed := object.(type) {
		case *appsv1.Deployment:
			require.NotContains(t, typed.Labels, "skali.dev/managed")
			require.NotContains(t, typed.Spec.Template.Labels, "skali.dev/managed")
		case *appsv1.StatefulSet:
			require.NotContains(t, typed.Labels, "skali.dev/managed")
		case *appsv1.DaemonSet:
			require.NotContains(t, typed.Labels, "skali.dev/managed")
		}
	}
	require.Equal(t, map[string]string{"app": FilerService}, services[S3Service].Spec.Selector)
}

func TestRenderDevServicesSelectAllInOne(t *testing.T) {
	t.Parallel()
	spec := StoreSpec{Namespace: "skali-platform", Masters: 1, Replication: "000"}
	names := map[string]bool{}
	for _, object := range RenderDev(spec) {
		if service, ok := object.(*corev1.Service); ok {
			names[service.Name] = true
			require.Equal(t, map[string]string{"app": AllInOneApp}, service.Spec.Selector,
				"dev services select the all-in-one pod under the same names")
		}
	}
	require.True(t, names[MasterService] && names[FilerService] && names[S3Service])
}

func TestRenderS3Edge(t *testing.T) {
	t.Parallel()
	objects := RenderS3Edge("skali-platform", "s3.example.com")
	byName := map[string]*unstructured.Unstructured{}
	for _, object := range objects {
		typed := object.(*unstructured.Unstructured)
		byName[typed.GetKind()+"/"+typed.GetName()] = typed
	}
	require.Len(t, byName, 4)

	route := byName["IngressRoute/seaweed-s3"]
	require.NotNil(t, route)
	points, _, err := unstructured.NestedStringSlice(route.Object, "spec", "entryPoints")
	require.NoError(t, err)
	require.Equal(t, []string{"websecure"}, points)
	secret, _, err := unstructured.NestedString(route.Object, "spec", "tls", "secretName")
	require.NoError(t, err)
	require.Equal(t, "seaweed-s3-tls", secret)
	routes, _, err := unstructured.NestedSlice(route.Object, "spec", "routes")
	require.NoError(t, err)
	rule := routes[0].(map[string]any)
	require.Equal(t, `Host("s3.example.com") && PathPrefix("/")`, rule["match"])
	backend := rule["services"].([]any)[0].(map[string]any)
	require.Equal(t, S3Service, backend["name"])
	require.EqualValues(t, S3Port, backend["port"])

	certificate := byName["Certificate/seaweed-s3-tls"]
	require.NotNil(t, certificate)
	issuer, _, err := unstructured.NestedString(certificate.Object, "spec", "issuerRef", "name")
	require.NoError(t, err)
	require.Equal(t, "skali", issuer)
	names, _, err := unstructured.NestedStringSlice(certificate.Object, "spec", "dnsNames")
	require.NoError(t, err)
	require.Equal(t, []string{"s3.example.com"}, names)

	httpRoute := byName["IngressRoute/seaweed-s3-http"]
	require.NotNil(t, httpRoute)
	httpRoutes, _, err := unstructured.NestedSlice(httpRoute.Object, "spec", "routes")
	require.NoError(t, err)
	require.Contains(t, httpRoutes[0].(map[string]any), "middlewares",
		"plain HTTP redirects to the S3 endpoint's canonical scheme")
	require.NotNil(t, byName["Middleware/redirect-https"])
}

// TestRenderS3EdgeDomainChange: a hostname change re-applies the same
// object names with the new host, so server-side apply replaces the old
// domain in place and no object of the new rendering mentions it.
func TestRenderS3EdgeDomainChange(t *testing.T) {
	t.Parallel()
	names := func(objects []runtime.Object) []string {
		var out []string
		for _, object := range objects {
			typed := object.(*unstructured.Unstructured)
			out = append(out, typed.GetKind()+"/"+typed.GetName())
		}
		return out
	}
	before := RenderS3Edge("skali-platform", "s3.old.example.com")
	after := RenderS3Edge("skali-platform", "s3.new.example.com")
	require.Equal(t, names(before), names(after), "the same objects are replaced in place")
	for _, object := range after {
		encoded, err := json.Marshal(object.(*unstructured.Unstructured).Object)
		require.NoError(t, err)
		require.NotContains(t, string(encoded), "s3.old.example.com")
	}
	// The renamed host reaches every place a domain appears: both route
	// matches and the certificate's names.
	mentions := 0
	for _, object := range after {
		encoded, err := json.Marshal(object.(*unstructured.Unstructured).Object)
		require.NoError(t, err)
		mentions += strings.Count(string(encoded), "s3.new.example.com")
	}
	require.Equal(t, 3, mentions, "TLS route match, HTTP route match, certificate dnsNames")
}

func TestTopologyDerivation(t *testing.T) {
	t.Parallel()
	require.Equal(t, 1, MastersForNodes(1))
	require.Equal(t, 1, MastersForNodes(2))
	require.Equal(t, 3, MastersForNodes(3))
	require.Equal(t, 3, MastersForNodes(5))
	require.Equal(t, "000", ReplicationForNodes(1))
	require.Equal(t, "001", ReplicationForNodes(2))
	require.Equal(t, "001", ReplicationForNodes(4))
}

// The filer store Secret reaches the process as env read at start; the
// spec's config hash on the pod template is what rolls the filers when
// the store config changes, in both shapes.
func TestRenderStoreConfigHashRollsFilers(t *testing.T) {
	t.Parallel()
	find := func(objects []runtime.Object, name string) *appsv1.Deployment {
		for _, obj := range objects {
			if deployment, ok := obj.(*appsv1.Deployment); ok && deployment.Name == name {
				return deployment
			}
		}
		return nil
	}
	spec := StoreSpec{Namespace: "skali-platform", Masters: 1, Replication: "000", Managed: true, StoreConfigHash: "abc123"}
	filer := find(RenderProduction(spec), FilerService)
	require.NotNil(t, filer)
	require.Equal(t, "abc123", filer.Spec.Template.Annotations[AnnotationConfigHash])
	spec.Managed = false
	allInOne := find(RenderDev(spec), AllInOneApp)
	require.NotNil(t, allInOne)
	require.Equal(t, "abc123", allInOne.Spec.Template.Annotations[AnnotationConfigHash])
	spec.StoreConfigHash = ""
	require.Nil(t, find(RenderDev(spec), AllInOneApp).Spec.Template.Annotations, "no hash, no annotation")
}

// TestRenderProductionOperability: every component declares resources and
// liveness/startup probes, the filer rolls without a gap and is ready only
// when its S3 gateway answers, disruption budgets guard the quorum and the
// gateway, and the master maintenance loop actually applies replica
// repair (without -apply the pin only reports).
func TestRenderProductionOperability(t *testing.T) {
	t.Parallel()
	three := RenderProduction(StoreSpec{Namespace: "skali-platform", Masters: 3, Replication: "001", Managed: true})
	var sts *appsv1.StatefulSet
	var daemon *appsv1.DaemonSet
	var filer *appsv1.Deployment
	var config *corev1.ConfigMap
	budgets := map[string]*policyv1.PodDisruptionBudget{}
	for _, object := range three {
		switch typed := object.(type) {
		case *appsv1.StatefulSet:
			sts = typed
		case *appsv1.DaemonSet:
			daemon = typed
		case *appsv1.Deployment:
			filer = typed
		case *policyv1.PodDisruptionBudget:
			budgets[typed.Name] = typed
		case *corev1.ConfigMap:
			if typed.Name == "seaweed-master-config" {
				config = typed
			}
		}
	}
	for name, container := range map[string]corev1.Container{
		"master": sts.Spec.Template.Spec.Containers[0],
		"volume": daemon.Spec.Template.Spec.Containers[0],
		"filer":  filer.Spec.Template.Spec.Containers[0],
	} {
		require.NotEmpty(t, container.Resources.Requests, "%s declares requests", name)
		require.NotEmpty(t, container.Resources.Limits, "%s declares limits", name)
		require.NotNil(t, container.LivenessProbe, "%s has a liveness probe", name)
		require.NotNil(t, container.StartupProbe, "%s has a startup probe", name)
		require.Equal(t, "/healthz", container.LivenessProbe.HTTPGet.Path)
		require.NotNil(t, container.ReadinessProbe, "%s has a readiness probe", name)
	}
	require.EqualValues(t, S3Port, filer.Spec.Template.Spec.Containers[0].ReadinessProbe.HTTPGet.Port.IntValue(),
		"the filer is ready when its S3 gateway answers")
	require.Equal(t, appsv1.RollingUpdateDeploymentStrategyType, filer.Spec.Strategy.Type)
	require.Equal(t, 0, filer.Spec.Strategy.RollingUpdate.MaxUnavailable.IntValue())
	require.Equal(t, 1, filer.Spec.Strategy.RollingUpdate.MaxSurge.IntValue())

	require.Len(t, budgets, 2)
	require.Equal(t, 1, budgets[MasterService].Spec.MaxUnavailable.IntValue())
	require.Equal(t, 1, budgets[FilerService].Spec.MinAvailable.IntValue())
	require.Contains(t, config.Data["master.toml"], "volume.fix.replication -apply")

	// A single master has no budget: zero tolerated disruptions would
	// only block drains.
	one := RenderProduction(StoreSpec{Namespace: "skali-platform", Masters: 1, Replication: "000", Managed: true})
	count := 0
	for _, object := range one {
		if _, ok := object.(*policyv1.PodDisruptionBudget); ok {
			count++
		}
	}
	require.Equal(t, 1, count, "only the filer budget on a single-master store")
}

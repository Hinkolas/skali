package seaweed

import (
	"testing"

	"github.com/stretchr/testify/require"
	appsv1 "k8s.io/api/apps/v1"
	corev1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
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

	require.NotNil(t, daemon)
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
	require.Equal(t, "Host(`s3.example.com`) && PathPrefix(`/`)", rule["match"])
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

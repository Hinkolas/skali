package kubernetes

import (
	"github.com/Hinkolas/skali/internal/compiler"
	"github.com/google/uuid"
	"github.com/stretchr/testify/require"
	corev1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"strings"
	"testing"
)

func TestIdentityBoundaries(t *testing.T) {
	a, b := uuid.NewString(), uuid.NewString()
	require.NotEqual(t, RenderNamespace("acme-prod", "blue", a).Name, RenderNamespace("acme", "prod-blue", b).Name)
	require.Equal(t, RenderNamespace("renamed", "also-renamed", a).Name, NamespaceName(a))
	require.NotEqual(t, VolumeClaimName("acme", "web-data", "cache"), VolumeClaimName("acme", "web", "data-cache"))
	require.NotEqual(t, RouteName("acme", "web", "public-http", "primary"), RouteName("acme", "web", "public", "http"))
	require.NotEqual(t, objectName("app", strings.Repeat("a", 63), "b"), objectName("app", strings.Repeat("a", 62), "a-b"))
	require.LessOrEqual(t, len(VolumeClaimName(strings.Repeat("a", 63), strings.Repeat("b", 63), strings.Repeat("c", 63))), 63)
}

func TestDuplicateDesiredIdentityAndPorts(t *testing.T) {
	ns := RenderNamespace("acme", "prod", uuid.NewString())
	require.ErrorContains(t, ValidateObjects([]runtime.Object{ns, ns.DeepCopy()}), "duplicate Kubernetes identity")
	svc := &corev1.Service{}
	svc.Spec.Ports = []corev1.ServicePort{{Name: "same"}, {Name: "same"}}
	require.ErrorContains(t, ValidateObjects([]runtime.Object{svc}), "duplicate service port")
	application := compiler.Application{Ports: map[string]compiler.Port{"route-api": {Port: 8080}}, Routes: map[string]compiler.Route{"api": {Port: compiler.PortTarget{Number: 9090}}}}
	svc.Spec.Ports = renderServicePorts(application)
	require.ErrorContains(t, ValidateObjects([]runtime.Object{svc}), "duplicate service port")
}

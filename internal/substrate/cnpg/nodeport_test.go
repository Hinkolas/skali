package cnpg

import (
	"testing"

	"github.com/stretchr/testify/require"
	corev1 "k8s.io/api/core/v1"
)

func TestRenderPrimaryNodePortService(t *testing.T) {
	t.Parallel()
	service := RenderPrimaryNodePortService("skali-platform", "pg17-shared", 30501)
	require.Equal(t, "pg17-shared-external", service.Name)
	require.Equal(t, "skali-platform", service.Namespace)
	require.Equal(t, corev1.ServiceTypeNodePort, service.Spec.Type)
	require.Equal(t, map[string]string{
		"cnpg.io/cluster":      "pg17-shared",
		"cnpg.io/instanceRole": "primary",
	}, service.Spec.Selector)
	require.Len(t, service.Spec.Ports, 1)
	require.EqualValues(t, 5432, service.Spec.Ports[0].Port)
	require.EqualValues(t, 30501, service.Spec.Ports[0].NodePort)
	// Platform objects stay out of the managed informers.
	require.NotContains(t, service.Labels, "skali.dev/managed")
}

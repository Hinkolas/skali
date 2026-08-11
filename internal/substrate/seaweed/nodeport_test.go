package seaweed

import (
	"testing"

	"github.com/Hinkolas/skali/internal/bundle"
	"github.com/stretchr/testify/require"
	corev1 "k8s.io/api/core/v1"
)

func TestRenderDevS3NodePort(t *testing.T) {
	t.Parallel()
	service := RenderDevS3NodePort("skali-platform")
	require.Equal(t, "seaweed-s3-external", service.Name)
	require.Equal(t, corev1.ServiceTypeNodePort, service.Spec.Type)
	require.Equal(t, map[string]string{"app": AllInOneApp}, service.Spec.Selector)
	require.Len(t, service.Spec.Ports, 1)
	require.EqualValues(t, S3Port, service.Spec.Ports[0].Port)
	require.EqualValues(t, bundle.S3NodePort, service.Spec.Ports[0].NodePort)
	require.NotContains(t, service.Labels, "skali.dev/managed")
}

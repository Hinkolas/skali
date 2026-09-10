package edgeobserve

import (
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/stretchr/testify/require"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"

	"github.com/Hinkolas/skali/internal/edge"
	"github.com/Hinkolas/skali/internal/module"
)

var testEnvironment = uuid.MustParse("11111111-2222-3333-4444-555555555555")

func testLabels() map[string]any {
	return map[string]any{
		"skali.dev/managed":     "true",
		"skali.dev/environment": testEnvironment.String(),
		"skali.dev/service":     "web",
		"skali.dev/revision":    "rev-1",
	}
}

func TestKindsGateCertificates(t *testing.T) {
	t.Parallel()
	require.Len(t, Kinds(false), 2)
	withCerts := Kinds(true)
	require.Len(t, withCerts, 3)
	require.Equal(t, "Certificate", withCerts[2].Kind)
}

func TestConvertIngressRouteIdentity(t *testing.T) {
	t.Parallel()
	object := &unstructured.Unstructured{Object: map[string]any{
		"apiVersion": "traefik.io/v1alpha1",
		"kind":       "IngressRoute",
		"metadata": map[string]any{
			"name":      "web-public",
			"namespace": "proj-env",
			"labels":    testLabels(),
		},
	}}
	converted, ok := convertIdentity(edge.IngressRouteGVK)(object)
	require.True(t, ok)
	require.Equal(t, module.KindIngress, converted.Kind)
	require.Equal(t, "web-public", converted.Name)
	require.Equal(t, testEnvironment, converted.Environment)
	require.Equal(t, "web", converted.Service)
	require.Equal(t, "rev-1", converted.Revision)
	require.Equal(t, edge.IngressRouteGVK, converted.Ref.GVK)
	require.Nil(t, converted.Certificate)
}

func TestConvertCertificatePending(t *testing.T) {
	t.Parallel()
	object := certificateObject(map[string]any{
		"conditions": []any{
			map[string]any{"type": "Ready", "status": "False", "reason": "DoesNotExist", "message": "certificate does not exist"},
			map[string]any{"type": "Issuing", "status": "True", "reason": "Pending", "message": "waiting for the ACME challenge"},
		},
	})
	converted, ok := ConvertCertificate(object)
	require.True(t, ok)
	require.Equal(t, module.KindCertificate, converted.Kind)
	status := converted.Certificate
	require.NotNil(t, status)
	require.False(t, status.Ready)
	require.True(t, status.Issuing)
	require.Equal(t, "Pending", status.Reason, "the issuing condition explains the wait")
	require.Equal(t, "waiting for the ACME challenge", status.Message)
	require.True(t, status.NotAfter.IsZero())
	require.Equal(t, "web-public-tls", status.SecretName)
	require.Equal(t, []string{"app.example.com"}, status.DNSNames)
}

func TestConvertCertificateIssued(t *testing.T) {
	t.Parallel()
	object := certificateObject(map[string]any{
		"notAfter":    "2026-11-11T10:00:00Z",
		"renewalTime": "2026-10-12T10:00:00Z",
		"conditions": []any{
			map[string]any{"type": "Ready", "status": "True", "reason": "Ready", "message": "certificate is up to date"},
		},
	})
	converted, ok := ConvertCertificate(object)
	require.True(t, ok)
	status := converted.Certificate
	require.True(t, status.Ready)
	require.False(t, status.Issuing)
	require.Equal(t, "Ready", status.Reason)
	require.Equal(t, time.Date(2026, 11, 11, 10, 0, 0, 0, time.UTC), status.NotAfter)
	require.Equal(t, time.Date(2026, 10, 12, 10, 0, 0, 0, time.UTC), status.RenewalTime)
}

func TestConvertCertificateFailing(t *testing.T) {
	t.Parallel()
	object := certificateObject(map[string]any{
		"failedIssuanceAttempts": int64(3),
		"conditions": []any{
			map[string]any{"type": "Ready", "status": "False", "reason": "Failed", "message": "ACME authorization failed"},
			map[string]any{"type": "Issuing", "status": "False", "reason": "Failed", "message": "ACME authorization failed"},
		},
	})
	converted, ok := ConvertCertificate(object)
	require.True(t, ok)
	status := converted.Certificate
	require.False(t, status.Ready)
	require.Equal(t, int32(3), status.FailedAttempts)
	require.Equal(t, "Failed", status.Reason)
	require.Equal(t, "ACME authorization failed", status.Message)
}

func certificateObject(status map[string]any) *unstructured.Unstructured {
	return &unstructured.Unstructured{Object: map[string]any{
		"apiVersion": "cert-manager.io/v1",
		"kind":       "Certificate",
		"metadata": map[string]any{
			"name":      "web-public-tls",
			"namespace": "proj-env",
			"labels":    testLabels(),
		},
		"spec": map[string]any{
			"secretName": "web-public-tls",
			"dnsNames":   []any{"app.example.com"},
		},
		"status": status,
	}}
}

func TestCertificateRetrySchedule(t *testing.T) {
	failure := time.Date(2026, 9, 10, 10, 0, 0, 0, time.UTC)
	for _, tc := range []struct {
		attempts int64
		hours    int
	}{{0, 1}, {1, 1}, {2, 2}, {3, 4}, {6, 32}, {1000000, 32}} {
		obj := certificateObject(map[string]any{"failedIssuanceAttempts": tc.attempts, "lastFailureTime": failure.Format(time.RFC3339)})
		converted, ok := ConvertCertificate(obj)
		require.True(t, ok)
		require.Equal(t, failure, converted.Certificate.LastFailureTime)
		require.Equal(t, failure.Add(time.Duration(tc.hours)*time.Hour), converted.Certificate.NextRetryTime)
	}
	obj := certificateObject(map[string]any{"failedIssuanceAttempts": int64(3), "lastFailureTime": failure.Format(time.RFC3339), "nextPrivateKeySecretName": "next-key", "conditions": []any{map[string]any{"type": "Issuing", "status": "True"}}})
	converted, _ := ConvertCertificate(obj)
	require.True(t, converted.Certificate.NextRetryTime.IsZero(), "an active retry is not in backoff")
	require.Equal(t, "next-key", converted.Certificate.NextPrivateKeySecretName)
}

// Package edgeobserve registers the managed edge's CRDs with the dynamic
// observation watch: Traefik IngressRoutes and Middlewares as identity-only
// ingress projections (pruning and teardown see them, no module evaluates
// them), and cert-manager Certificates as typed status projections the app
// module gates route health on.
package edgeobserve

import (
	"time"

	"github.com/google/uuid"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/runtime/schema"

	"github.com/Hinkolas/skali/internal/edge"
	"github.com/Hinkolas/skali/internal/kube"
	"github.com/Hinkolas/skali/internal/kubernetes"
	"github.com/Hinkolas/skali/internal/module"
	"github.com/Hinkolas/skali/internal/observe"
)

// Kinds returns the dynamic watch registrations for the edge. certificates
// gates the Certificate watch: the CRD exists only on installations that
// run cert-manager, and registering a watch for an absent CRD would keep
// the observation source from ever reporting fresh.
func Kinds(certificates bool) []observe.DynamicKind {
	kinds := []observe.DynamicKind{
		{Kind: "IngressRoute", GVR: edge.IngressRouteGVR, Convert: convertIdentity(edge.IngressRouteGVK)},
		{Kind: "Middleware", GVR: edge.MiddlewareGVR, Convert: convertIdentity(edge.MiddlewareGVK)},
	}
	if certificates {
		kinds = append(kinds, observe.DynamicKind{
			Kind: "Certificate", GVR: edge.CertificateGVR, Convert: ConvertCertificate,
		})
	}
	return kinds
}

// convertIdentity projects a Traefik routing object into the observed store
// with identity but no typed status, under the module ingress kind: exactly
// the role the plain Ingress projection played, so pruning, teardown, and
// the service whitelist stay unchanged.
func convertIdentity(gvk schema.GroupVersionKind) func(*unstructured.Unstructured) (observe.Object, bool) {
	return func(object *unstructured.Unstructured) (observe.Object, bool) {
		name := object.GetName()
		if name == "" {
			return observe.Object{}, false
		}
		environment, service, revision := identity(object)
		return observe.Object{
			Ref: kube.ObjectRef{GVK: gvk, Namespace: object.GetNamespace(),
				Name: name, UID: object.GetUID()},
			Kind:        module.KindIngress,
			Name:        name,
			Labels:      object.GetLabels(),
			Environment: environment,
			Service:     service,
			Revision:    revision,
			Generation:  object.GetGeneration(),
		}, true
	}
}

// ConvertCertificate projects a cert-manager Certificate with the issuance
// status the app module and the status projection read.
func ConvertCertificate(object *unstructured.Unstructured) (observe.Object, bool) {
	name := object.GetName()
	if name == "" {
		return observe.Object{}, false
	}
	environment, service, revision := identity(object)
	status := &module.CertificateStatus{}
	status.SecretName, _, _ = unstructured.NestedString(object.Object, "spec", "secretName")
	if names, _, _ := unstructured.NestedStringSlice(object.Object, "spec", "dnsNames"); len(names) > 0 {
		status.DNSNames = names
	}
	status.NotAfter = timestamp(object, "status", "notAfter")
	status.RenewalTime = timestamp(object, "status", "renewalTime")
	if attempts, found, _ := unstructured.NestedInt64(object.Object, "status", "failedIssuanceAttempts"); found {
		status.FailedAttempts = int32(attempts)
	}
	conditions, _, _ := unstructured.NestedSlice(object.Object, "status", "conditions")
	issuingReason, issuingMessage := "", ""
	for _, entry := range conditions {
		condition, ok := entry.(map[string]any)
		if !ok {
			continue
		}
		conditionType, _ := condition["type"].(string)
		conditionStatus, _ := condition["status"].(string)
		reason, _ := condition["reason"].(string)
		message, _ := condition["message"].(string)
		switch conditionType {
		case "Ready":
			status.Ready = conditionStatus == "True"
			status.Reason = reason
			status.Message = message
		case "Issuing":
			status.Issuing = conditionStatus == "True"
			issuingReason, issuingMessage = reason, message
		}
	}
	// While issuance is in flight its condition explains the wait better
	// than the Ready condition's generic DoesNotExist.
	if !status.Ready && issuingReason != "" {
		status.Reason, status.Message = issuingReason, issuingMessage
	}
	return observe.Object{
		Ref: kube.ObjectRef{GVK: edge.CertificateGVK, Namespace: object.GetNamespace(),
			Name: name, UID: object.GetUID()},
		Kind:        module.KindCertificate,
		Name:        name,
		Labels:      object.GetLabels(),
		Environment: environment,
		Service:     service,
		Revision:    revision,
		Generation:  object.GetGeneration(),
		Certificate: status,
	}, true
}

// identity extracts the shared label-derived identity of a managed object,
// mirroring the kubernetes watch's projection of typed kinds.
func identity(object *unstructured.Unstructured) (environment uuid.UUID, service, revision string) {
	labels := object.GetLabels()
	if parsed, err := uuid.Parse(labels[kubernetes.LabelEnvironment]); err == nil {
		environment = parsed
	}
	service = labels[kubernetes.LabelService]
	if service == "" {
		service = labels[kubernetes.LabelApplication]
	}
	revision = labels[kubernetes.LabelRevision]
	return environment, service, revision
}

func timestamp(object *unstructured.Unstructured, fields ...string) time.Time {
	raw, found, _ := unstructured.NestedString(object.Object, fields...)
	if !found || raw == "" {
		return time.Time{}
	}
	parsed, err := time.Parse(time.RFC3339, raw)
	if err != nil {
		return time.Time{}
	}
	return parsed
}

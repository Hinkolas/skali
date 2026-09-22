package kube

import (
	admissionv1 "k8s.io/api/admissionregistration/v1"
	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
)

const (
	OwnershipPolicyName     = "skali-environment-ownership-v1"
	OwnershipProbeNamespace = "skali-system"
	OwnershipProbeName      = "skali-ownership-probe-v1"
	ownershipDenied         = "skali ownership metadata is immutable"
)

// OwnershipPolicy is versioned because its exact semantics are a prerequisite
// for UID-guarded applies. New semantics require a new name, not an in-place
// weakening of the policy while clients still depend on it.
func OwnershipPolicy() *admissionv1.ValidatingAdmissionPolicy {
	fail := admissionv1.Fail
	equivalent := admissionv1.Equivalent
	scope := admissionv1.AllScopes
	return &admissionv1.ValidatingAdmissionPolicy{
		TypeMeta:   metav1.TypeMeta{APIVersion: "admissionregistration.k8s.io/v1", Kind: "ValidatingAdmissionPolicy"},
		ObjectMeta: metav1.ObjectMeta{Name: OwnershipPolicyName},
		Spec: admissionv1.ValidatingAdmissionPolicySpec{
			FailurePolicy: &fail,
			MatchConstraints: &admissionv1.MatchResources{
				MatchPolicy:       &equivalent,
				NamespaceSelector: &metav1.LabelSelector{},
				ObjectSelector:    &metav1.LabelSelector{},
				ResourceRules: []admissionv1.NamedRuleWithOperations{{
					RuleWithOperations: admissionv1.RuleWithOperations{
						Operations: []admissionv1.OperationType{admissionv1.Update},
						Rule:       admissionv1.Rule{APIGroups: []string{"*"}, APIVersions: []string{"*"}, Resources: []string{"*/*"}, Scope: &scope},
					},
				}},
			},
			MatchConditions: []admissionv1.MatchCondition{{
				Name: "existing-environment-identity",
				Expression: "oldObject != null && " +
					"has(oldObject.metadata.labels) && " +
					"has(oldObject.metadata.annotations) && " +
					"'skali.dev/managed' in oldObject.metadata.labels && " +
					"oldObject.metadata.labels['skali.dev/managed'] == 'true' && " +
					"'skali.dev/environment' in oldObject.metadata.labels && " +
					"oldObject.metadata.labels['skali.dev/environment'] != '' && " +
					"'skali.dev/resource-identity' in oldObject.metadata.annotations && " +
					"oldObject.metadata.annotations['skali.dev/resource-identity'] != ''"}},
			Validations: []admissionv1.Validation{{
				Expression: "has(object.metadata.labels) && " +
					"has(object.metadata.annotations) && " +
					"['skali.dev/managed', 'skali.dev/environment'].all(k, k in object.metadata.labels && " +
					"object.metadata.labels[k] == oldObject.metadata.labels[k]) && " +
					"'skali.dev/resource-identity' in object.metadata.annotations && " +
					"object.metadata.annotations['skali.dev/resource-identity'] == oldObject.metadata.annotations['skali.dev/resource-identity']",
				Message: ownershipDenied,
				Reason:  new(metav1.StatusReasonForbidden),
			}},
		},
	}
}

func OwnershipPolicyBinding() *admissionv1.ValidatingAdmissionPolicyBinding {
	return &admissionv1.ValidatingAdmissionPolicyBinding{
		TypeMeta:   metav1.TypeMeta{APIVersion: "admissionregistration.k8s.io/v1", Kind: "ValidatingAdmissionPolicyBinding"},
		ObjectMeta: metav1.ObjectMeta{Name: OwnershipPolicyName},
		Spec:       admissionv1.ValidatingAdmissionPolicyBindingSpec{PolicyName: OwnershipPolicyName, ValidationActions: []admissionv1.ValidationAction{admissionv1.Deny}},
	}
}

// The installer creates this harmless object. Verification only performs dry-run
// updates; the daemon never creates probes or changes admission configuration.
func OwnershipProbe() *corev1.ConfigMap {
	return &corev1.ConfigMap{
		TypeMeta: metav1.TypeMeta{APIVersion: "v1", Kind: "ConfigMap"},
		ObjectMeta: metav1.ObjectMeta{Name: OwnershipProbeName, Namespace: OwnershipProbeNamespace,
			Labels:      map[string]string{"skali.dev/managed": "true", "skali.dev/environment": "ownership-probe"},
			Annotations: map[string]string{identityAnnotation: "/ConfigMap/" + OwnershipProbeName}},
	}
}

package kubernetes

import (
	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
)

// EnvironmentSecretName is the fixed name of the per-environment values
// Secret every rendered container references.
const EnvironmentSecretName = "skali-environment"

// NamespaceName derives the deterministic namespace of one environment.
func NamespaceName(project, environment string) string {
	return objectName("skali", project, environment)
}

// RenderNamespace renders the environment namespace. It is the first object
// of every desired set; the reconciler never prunes namespaces.
func RenderNamespace(project, environment, environmentID string) *corev1.Namespace {
	return &corev1.Namespace{
		TypeMeta: metav1.TypeMeta{APIVersion: "v1", Kind: "Namespace"},
		ObjectMeta: metav1.ObjectMeta{
			Name: NamespaceName(project, environment),
			Labels: map[string]string{
				LabelManaged:     "true",
				LabelProject:     project,
				LabelEnvironment: environmentID,
			},
		},
	}
}

// RenderEnvironmentSecret renders the values Secret of one environment. The
// caller supplies the merged plain and decrypted secret data; this function
// deliberately never logs and Render itself never sees plaintext.
func RenderEnvironmentSecret(project, environment, environmentID, revisionChecksum string, data map[string][]byte) *corev1.Secret {
	labels := map[string]string{
		LabelManaged:     "true",
		LabelProject:     project,
		LabelEnvironment: environmentID,
	}
	if revisionChecksum != "" {
		labels[LabelRevision] = RevisionLabelValue(revisionChecksum)
	}
	return &corev1.Secret{
		TypeMeta: metav1.TypeMeta{APIVersion: "v1", Kind: "Secret"},
		ObjectMeta: metav1.ObjectMeta{
			Name:      EnvironmentSecretName,
			Namespace: NamespaceName(project, environment),
			Labels:    labels,
		},
		Type: corev1.SecretTypeOpaque,
		Data: data,
	}
}

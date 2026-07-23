package kubernetes

import (
	"encoding/base64"
	"encoding/json"

	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
)

// EnvironmentSecretName is the fixed name of the per-environment values
// Secret every rendered container references.
const EnvironmentSecretName = "skali-environment"

// PullSecretName is the fixed name of the per-environment registry pull
// secret rendered on existing clusters, where nodes have no containerd
// mirror and pull application images through the public registry domain.
const PullSecretName = "skali-registry-pull"

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

// RenderPullSecret renders the per-environment registry pull secret: a
// dockerconfigjson for the given registry host under the shared node
// credential, granted pull-only tokens by skalid's token endpoint. It
// carries the same identity labels as the values Secret.
func RenderPullSecret(project, environment, environmentID, revisionChecksum, host, username, password string) *corev1.Secret {
	auth := map[string]any{"auths": map[string]any{
		host: map[string]any{
			"username": username,
			"password": password,
			"auth":     base64.StdEncoding.EncodeToString([]byte(username + ":" + password)),
		},
	}}
	// A map of strings cannot fail to encode.
	config, _ := json.Marshal(auth)
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
			Name:      PullSecretName,
			Namespace: NamespaceName(project, environment),
			Labels:    labels,
		},
		Type: corev1.SecretTypeDockerConfigJson,
		Data: map[string][]byte{corev1.DockerConfigJsonKey: config},
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

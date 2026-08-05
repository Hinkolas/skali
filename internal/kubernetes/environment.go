package kubernetes

import (
	"fmt"

	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"

	"github.com/Hinkolas/skali/internal/compiler"
	"github.com/Hinkolas/skali/internal/utils"
)

// EnvironmentSecretName is the fixed name of the per-environment values
// Secret every rendered container references.
const EnvironmentSecretName = "skali-environment"

// EnvironmentSecretKey names the composed entry of one application
// environment variable inside the environment Secret. Application keys
// cannot contain dots, so composed keys never collide.
func EnvironmentSecretKey(applicationKey, envName string) string {
	return applicationKey + "." + envName
}

// EnvironmentSecretData computes the full data of the environment Secret:
// one fully resolved entry per application environment variable that
// references project values, keyed by EnvironmentSecretKey. Concatenation
// and inline defaults resolve here, so a defaulted variable that was never
// uploaded still materializes. Service outputs are excluded; they bind to
// the substrate-owned output Secrets. Pure; never logs, and errors carry
// field paths and variable names only.
func EnvironmentSecretData(definition compiler.ProjectDefinition, variables map[string]string) (map[string][]byte, error) {
	data := map[string][]byte{}
	for _, applicationKey := range utils.SortedKeys(definition.Applications) {
		application := definition.Applications[applicationKey]
		for _, name := range utils.SortedKeys(application.Environment) {
			expression := application.Environment[name]
			if _, isOutput := expression.ServiceOutput(); isOutput || !expression.HasProjectVariables() {
				continue
			}
			resolved, err := compiler.ResolveExpression(expression, variables)
			if err != nil {
				return nil, fmt.Errorf("environment %s.%s: %w", applicationKey, name, err)
			}
			data[EnvironmentSecretKey(applicationKey, name)] = []byte(resolved)
		}
	}
	return data, nil
}

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

// OutputSecretName is the deterministic name of the connection-output Secret
// one service publishes into the environment namespace; rendered containers
// reference it through the secretKeyRef bindings emitted for
// {{<collection>.<service>.<output>}} expressions.
func OutputSecretName(collection, service string) string {
	return objectName("skali-output", collection, service)
}

// RenderOutputSecret renders one service's connection-output mirror in the
// environment namespace. The caller supplies the output values (including
// secret ones read from the substrate's credential Secret); like the values
// Secret, this function never logs.
func RenderOutputSecret(project, environment, environmentID, collection, service string, data map[string][]byte) *corev1.Secret {
	return &corev1.Secret{
		TypeMeta: metav1.TypeMeta{APIVersion: "v1", Kind: "Secret"},
		ObjectMeta: metav1.ObjectMeta{
			Name:      OutputSecretName(collection, service),
			Namespace: NamespaceName(project, environment),
			Labels: map[string]string{
				LabelManaged:     "true",
				LabelProject:     project,
				LabelEnvironment: environmentID,
				LabelService:     collection + "." + service,
			},
		},
		Type: corev1.SecretTypeOpaque,
		Data: data,
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

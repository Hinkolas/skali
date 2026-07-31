package cnpg

import (
	"maps"

	corev1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/api/resource"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/runtime/schema"

	"github.com/Hinkolas/skali/internal/kubernetes"
	"github.com/Hinkolas/skali/internal/layout"
)

// HibernationAnnotation is CNPG's declarative hibernation switch. Pools no
// longer hibernate (owner decision 2026-07-31: the dev substrate is always
// on), but the annotation stays rendered as an explicit "off" for one more
// release: server-side apply keeps sole ownership, so an existing
// hibernated dev pool is guaranteed to wake on its next converge. Dropping
// the annotation entirely is the later cleanup.
const HibernationAnnotation = "cnpg.io/hibernation"

var (
	ClusterGVK  = schema.GroupVersionKind{Group: "postgresql.cnpg.io", Version: "v1", Kind: "Cluster"}
	DatabaseGVK = schema.GroupVersionKind{Group: "postgresql.cnpg.io", Version: "v1", Kind: "Database"}
	ClusterGVR  = schema.GroupVersionResource{Group: "postgresql.cnpg.io", Version: "v1", Resource: "clusters"}
	DatabaseGVR = schema.GroupVersionResource{Group: "postgresql.cnpg.io", Version: "v1", Resource: "databases"}
)

// Role is one managed login role on a pool, its password held by a
// basic-auth Secret in the pool's namespace.
type Role struct {
	Name       string
	SecretName string
}

// ClusterSpec is the desired shape of one pool.
type ClusterSpec struct {
	Namespace    string
	Name         string
	Image        string
	Instances    int
	StorageBytes int64
	// Synchronous renders the quorum replication block; the caller sets it
	// only at three or more instances (transactions wait for any one
	// standby).
	Synchronous bool
	// Managed pins the pool to database-capable nodes; local dev renders no
	// selector because its nodes carry no capability labels.
	Managed bool
	Roles   []Role
}

// RenderCluster renders the CNPG Cluster object for one pool.
func RenderCluster(spec ClusterSpec) *unstructured.Unstructured {
	roles := make([]any, 0, len(spec.Roles))
	for _, role := range spec.Roles {
		roles = append(roles, map[string]any{
			"name":           role.Name,
			"ensure":         "present",
			"login":          true,
			"passwordSecret": map[string]any{"name": role.SecretName},
		})
	}
	object := map[string]any{
		"apiVersion": ClusterGVK.GroupVersion().String(),
		"kind":       ClusterGVK.Kind,
		"metadata": map[string]any{
			"name":      spec.Name,
			"namespace": spec.Namespace,
			"labels": map[string]any{
				kubernetes.LabelManaged: "true",
				kubernetes.LabelPool:    spec.Name,
			},
			"annotations": map[string]any{
				HibernationAnnotation: "off",
			},
		},
		"spec": map[string]any{
			"instances": int64(spec.Instances),
			"imageName": spec.Image,
			"storage": map[string]any{
				"size": resource.NewQuantity(spec.StorageBytes, resource.BinarySI).String(),
			},
		},
	}
	clusterSpec := object["spec"].(map[string]any)
	if len(roles) > 0 {
		clusterSpec["managed"] = map[string]any{"roles": roles}
	}
	if spec.Managed {
		clusterSpec["affinity"] = map[string]any{
			"nodeSelector": map[string]any{
				layout.CapabilityLabel(layout.CapabilityDatabase): layout.CapabilityLabelValue,
			},
		}
	}
	if spec.Synchronous {
		clusterSpec["postgresql"] = map[string]any{
			"synchronous": map[string]any{"method": "any", "number": int64(1)},
		}
	}
	return &unstructured.Unstructured{Object: object}
}

// DatabaseSpec is the desired shape of one tenant's logical database.
type DatabaseSpec struct {
	Namespace    string
	ObjectName   string
	ClusterName  string
	DatabaseName string
	Owner        string
	Extensions   []string
	// Absent renders teardown intent; CNPG drops the database because the
	// reclaim policy is delete.
	Absent bool
	// Labels carry the claim's identity (environment/service for user
	// claims, the claim label always) for observation and fan-out.
	Labels map[string]string
}

// RenderDatabase renders the CNPG Database object for one tenant.
func RenderDatabase(spec DatabaseSpec) *unstructured.Unstructured {
	ensure := "present"
	if spec.Absent {
		ensure = "absent"
	}
	labels := map[string]any{
		kubernetes.LabelManaged: "true",
		kubernetes.LabelPool:    spec.ClusterName,
	}
	for key, value := range spec.Labels {
		labels[key] = value
	}
	extensions := make([]any, 0, len(spec.Extensions))
	for _, extension := range spec.Extensions {
		extensions = append(extensions, map[string]any{
			"name":   extension,
			"ensure": "present",
		})
	}
	object := map[string]any{
		"apiVersion": DatabaseGVK.GroupVersion().String(),
		"kind":       DatabaseGVK.Kind,
		"metadata": map[string]any{
			"name":      spec.ObjectName,
			"namespace": spec.Namespace,
			"labels":    labels,
		},
		"spec": map[string]any{
			"cluster":               map[string]any{"name": spec.ClusterName},
			"name":                  spec.DatabaseName,
			"owner":                 spec.Owner,
			"ensure":                ensure,
			"databaseReclaimPolicy": "delete",
		},
	}
	if len(extensions) > 0 {
		object["spec"].(map[string]any)["extensions"] = extensions
	}
	return &unstructured.Unstructured{Object: object}
}

// RenderCredentialSecret renders a tenant's basic-auth credential Secret in
// the platform namespace. Deliberately never logged.
func RenderCredentialSecret(namespace, name, poolName, username, password string, labels map[string]string) *corev1.Secret {
	merged := map[string]string{
		kubernetes.LabelManaged: "true",
		kubernetes.LabelPool:    poolName,
	}
	maps.Copy(merged, labels)
	return &corev1.Secret{
		TypeMeta: metav1.TypeMeta{APIVersion: "v1", Kind: "Secret"},
		ObjectMeta: metav1.ObjectMeta{
			Name:      name,
			Namespace: namespace,
			Labels:    merged,
		},
		Type: corev1.SecretTypeBasicAuth,
		Data: map[string][]byte{
			corev1.BasicAuthUsernameKey: []byte(username),
			corev1.BasicAuthPasswordKey: []byte(password),
		},
	}
}

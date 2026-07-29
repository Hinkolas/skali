package cnpg

import (
	"github.com/google/uuid"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"

	"github.com/Hinkolas/skali/internal/kube"
	"github.com/Hinkolas/skali/internal/kubernetes"
	"github.com/Hinkolas/skali/internal/module"
	"github.com/Hinkolas/skali/internal/observe"
)

// ObserveKinds returns the dynamic watch registrations for CNPG resources:
// pool Clusters project platform-scoped shared objects, tenant Databases
// project environment-owned objects carrying the pool's shared key.
func ObserveKinds() []observe.DynamicKind {
	return []observe.DynamicKind{
		{Kind: "CNPGCluster", GVR: ClusterGVR, Convert: ConvertCluster},
		{Kind: "CNPGDatabase", GVR: DatabaseGVR, Convert: ConvertDatabase},
	}
}

// ConvertCluster projects a CNPG Cluster into the observed store.
func ConvertCluster(object *unstructured.Unstructured) (observe.Object, bool) {
	name := object.GetName()
	if name == "" {
		return observe.Object{}, false
	}
	instances, _, _ := unstructured.NestedInt64(object.Object, "spec", "instances")
	ready, _, _ := unstructured.NestedInt64(object.Object, "status", "readyInstances")
	phase, _, _ := unstructured.NestedString(object.Object, "status", "phase")
	primary, _, _ := unstructured.NestedString(object.Object, "status", "currentPrimary")
	return observe.Object{
		Ref: kube.ObjectRef{GVK: ClusterGVK, Namespace: object.GetNamespace(),
			Name: name, UID: object.GetUID()},
		Kind:      module.KindDatabaseCluster,
		Name:      name,
		Labels:    object.GetLabels(),
		SharedKey: name,
		DatabaseCluster: &module.DatabaseClusterStatus{
			Instances:      int32(instances),
			ReadyInstances: int32(ready),
			Phase:          phase,
			Primary:        primary,
			Hibernated:     object.GetAnnotations()[HibernationAnnotation] == "on",
		},
	}, true
}

// ConvertDatabase projects a CNPG Database (one tenant) into the observed
// store, indexed to its environment and service through the stamped labels.
func ConvertDatabase(object *unstructured.Unstructured) (observe.Object, bool) {
	labels := object.GetLabels()
	environment, err := uuid.Parse(labels[kubernetes.LabelEnvironment])
	if err != nil {
		// System tenants carry no environment; they surface through the
		// platform projection, not a service snapshot.
		environment = uuid.Nil
	}
	applied, found, _ := unstructured.NestedBool(object.Object, "status", "applied")
	message, _, _ := unstructured.NestedString(object.Object, "status", "message")
	if !found {
		message = "waiting for reconciliation"
	}
	databaseName, _, _ := unstructured.NestedString(object.Object, "spec", "name")
	return observe.Object{
		Ref: kube.ObjectRef{GVK: DatabaseGVK, Namespace: object.GetNamespace(),
			Name: object.GetName(), UID: object.GetUID()},
		Kind:        module.KindDatabaseTenant,
		Name:        databaseName,
		Labels:      labels,
		Environment: environment,
		Service:     labels[kubernetes.LabelService],
		SharedKey:   labels[kubernetes.LabelPool],
		DatabaseTenant: &module.DatabaseTenantStatus{
			Applied: applied,
			Message: message,
			Pool:    labels[kubernetes.LabelPool],
		},
	}, true
}

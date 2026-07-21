package installer

import (
	"context"
	"fmt"

	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/runtime/schema"

	"github.com/Hinkolas/skali/internal/bundle"
	"github.com/Hinkolas/skali/internal/installer/host"
	"github.com/Hinkolas/skali/internal/kube"
	"github.com/Hinkolas/skali/internal/version"
)

// ComponentStatus reports one bootstrap component's health.
type ComponentStatus struct {
	Name    string
	Healthy bool
	Detail  string
}

// Status is everything the read-only status view renders. Gathering never
// mutates anything.
type Status struct {
	Host *Host
	// K3sCurrent reports whether the installed k3s matches this
	// installer's pin.
	K3sCurrent bool
	// BundleVersion is the recorded bundle version; BundleCurrent whether
	// it matches this installer and the stamped hash is present.
	BundleVersion string
	BundleCurrent bool
	Initialized   bool
	// Nodes counts cluster members when the API is reachable.
	Nodes int
	// Components lists bootstrap component health; empty when the cluster
	// is unreachable.
	Components []ComponentStatus
	// ClusterReachable reports whether the Kubernetes API answered.
	ClusterReachable bool
}

// GatherStatus probes host, record, and (when reachable) the cluster.
func GatherStatus(ctx context.Context, runner host.Runner) (*Status, error) {
	detected, err := Detect(ctx, runner)
	if err != nil {
		return nil, err
	}
	status := &Status{Host: detected}
	if detected.Record == nil {
		return status, nil
	}
	status.K3sCurrent = detected.K3sVersion == K3sVersion
	status.BundleVersion = detected.Record.Versions.Bundle
	status.Initialized = detected.Record.Versions.Bundle != ""

	client, err := KubeClient(ctx, runner)
	if err != nil {
		return status, nil
	}
	nodes, err := client.Clientset.CoreV1().Nodes().List(ctx, metav1.ListOptions{})
	if err != nil {
		return status, nil
	}
	status.ClusterReachable = true
	status.Nodes = len(nodes.Items)
	if status.Initialized {
		status.BundleCurrent = status.BundleVersion == version.Version &&
			bundle.StampedHash(ctx, client) != ""
	}
	status.Components = gatherComponents(ctx, client)
	return status, nil
}

func gatherComponents(ctx context.Context, client *kube.Client) []ComponentStatus {
	components := []ComponentStatus{
		databaseComponent(ctx, client),
		deploymentComponent(ctx, client, "registry", bundle.Namespace, "skali-registry"),
		deploymentComponent(ctx, client, "skalid", bundle.Namespace, "skalid"),
	}
	return components
}

func deploymentComponent(ctx context.Context, client *kube.Client, label, namespace, name string) ComponentStatus {
	deployment, err := client.Clientset.AppsV1().Deployments(namespace).Get(ctx, name, metav1.GetOptions{})
	if err != nil {
		return ComponentStatus{Name: label, Detail: "not found"}
	}
	desired := int32(1)
	if deployment.Spec.Replicas != nil {
		desired = *deployment.Spec.Replicas
	}
	available := deployment.Status.AvailableReplicas
	if available >= desired {
		return ComponentStatus{Name: label, Healthy: true, Detail: "healthy"}
	}
	return ComponentStatus{Name: label, Detail: fmt.Sprintf("%d/%d replicas available", available, desired)}
}

func databaseComponent(ctx context.Context, client *kube.Client) ComponentStatus {
	resource := client.Dynamic.Resource(schema.GroupVersionResource{
		Group: "postgresql.cnpg.io", Version: "v1", Resource: "clusters",
	}).Namespace(bundle.Namespace)
	cluster, err := resource.Get(ctx, "skali-db", metav1.GetOptions{})
	if err != nil {
		return ComponentStatus{Name: "database", Detail: "not found"}
	}
	phase, _, _ := unstructured.NestedString(cluster.Object, "status", "phase")
	ready, _, _ := unstructured.NestedInt64(cluster.Object, "status", "readyInstances")
	instances, _, _ := unstructured.NestedInt64(cluster.Object, "spec", "instances")
	healthy := phase == "Cluster in healthy state" && ready >= instances
	detail := fmt.Sprintf("%d/%d instances ready", ready, instances)
	if healthy {
		detail = "healthy"
	}
	return ComponentStatus{Name: "database", Healthy: healthy, Detail: detail}
}

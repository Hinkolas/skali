package installer

import (
	"context"
	"fmt"
	"slices"
	"sort"

	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/runtime/schema"

	"github.com/Hinkolas/skali/internal/bundle"
	"github.com/Hinkolas/skali/internal/clusterstate"
	"github.com/Hinkolas/skali/internal/installer/host"
	"github.com/Hinkolas/skali/internal/kube"
	"github.com/Hinkolas/skali/internal/layout"
	"github.com/Hinkolas/skali/internal/version"
)

// ComponentStatus reports one bootstrap component's health.
type ComponentStatus struct {
	Name    string
	Healthy bool
	Detail  string
}

// NodeStatus describes one cluster member for status display and upgrade
// sequencing.
type NodeStatus struct {
	Name string
	Role string
	// Arch is the node's CPU architecture in Go/OCI form (amd64, arm64),
	// from the kubelet's node info.
	Arch string
	// Ready is the node's Ready condition.
	Ready bool
	// K3sVersion is the kubelet version, which on k3s carries the +k3s
	// packaging suffix.
	K3sVersion string
	// Current reports whether the node runs this installer's k3s pin.
	Current bool
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
	// Nodes lists cluster members when the API is reachable.
	Nodes []NodeStatus
	// Components lists bootstrap component health; empty when the cluster
	// is unreachable.
	Components []ComponentStatus
	// ClusterReachable reports whether the Kubernetes API answered.
	ClusterReachable bool
	// Datastore is "etcd" or "sqlite" on servers, empty on agents.
	// Legacy sqlite servers keep working single-node but cannot accept
	// additional servers.
	Datastore string
	// InitOwner names the node whose init maintains the bundle when the
	// in-cluster record was published by a different node; empty when
	// this node owns the bundle or the cluster record is unreadable.
	InitOwner string
	// DeployedTier is the tier the running skali-db instance count
	// represents; AvailableTier the tier the database-capable node count
	// derives. Both empty when the cluster or database is unreadable.
	DeployedTier  layout.Tier
	AvailableTier layout.Tier
	// DatabaseNodes lists the database-capable node names, sorted.
	DatabaseNodes []string
	// DatabaseInstances is the deployed skali-db spec.instances.
	DatabaseInstances int
	// Reconciled is the durable version-2 desired/observed state when this
	// server can read it. CoordinatorError explains why it is unavailable
	// without making local status fail.
	Reconciled       *clusterstate.State
	CoordinatorError string
}

// TierDrift reports whether the deployed and available tiers are both
// known and disagree.
func (s *Status) TierDrift() bool {
	return s.DeployedTier != "" && s.AvailableTier != "" && s.DeployedTier != s.AvailableTier
}

// Servers counts the control-plane members in Nodes.
func (s *Status) Servers() int {
	count := 0
	for _, node := range s.Nodes {
		if node.Role == layout.RoleServer {
			count++
		}
	}
	return count
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
	if detected.Record.Node.Role == layout.RoleServer {
		status.Datastore = "sqlite"
		if datastoreIsEtcd(ctx, runner) {
			status.Datastore = "etcd"
		}
	}

	client, err := KubeClient(ctx, runner)
	if err != nil {
		if detected.Record.Reconciled() && detected.Record.Node.Role == layout.RoleServer {
			status.CoordinatorError = err.Error()
		}
		return status, nil
	}
	if detected.Record.Reconciled() && detected.Record.Node.Role == layout.RoleServer {
		state, stateErr := (&clusterstate.Store{Client: client.Clientset}).Load(ctx)
		if stateErr != nil {
			status.CoordinatorError = stateErr.Error()
		} else {
			status.Reconciled = state
		}
	}
	nodes, err := client.Clientset.CoreV1().Nodes().List(ctx, metav1.ListOptions{})
	if err != nil {
		return status, nil
	}
	status.ClusterReachable = true
	for _, node := range nodes.Items {
		ready := false
		for _, condition := range node.Status.Conditions {
			if condition.Type == "Ready" && condition.Status == "True" {
				ready = true
			}
		}
		kubelet := node.Status.NodeInfo.KubeletVersion
		status.Nodes = append(status.Nodes, NodeStatus{
			Name:       node.Name,
			Role:       layout.RoleFromLabels(node.Labels),
			Arch:       node.Status.NodeInfo.Architecture,
			Ready:      ready,
			K3sVersion: kubelet,
			Current:    kubelet == K3sVersion,
		})
		if slices.Contains(layout.CapabilitiesFromLabels(node.Labels), layout.CapabilityDatabase) {
			status.DatabaseNodes = append(status.DatabaseNodes, node.Name)
		}
	}
	sort.Slice(status.Nodes, func(i, j int) bool { return status.Nodes[i].Name < status.Nodes[j].Name })
	sort.Strings(status.DatabaseNodes)
	status.AvailableTier = layout.DeriveTier(len(status.DatabaseNodes))

	// On a server whose local record never initialized the bundle, the
	// in-cluster record identifies the init owner: the bundle exists and
	// is maintained there, so status must not claim it is missing.
	if record, err := InClusterRecord(ctx, client); err == nil && record != nil {
		if !detected.Record.Reconciled() &&
			record.Node.Name != "" && record.Node.Name != detected.Hostname {
			status.InitOwner = record.Node.Name
		}
		if !status.Initialized && record.Versions.Bundle != "" {
			status.BundleVersion = record.Versions.Bundle
			status.Initialized = true
		}
	}
	if status.Initialized {
		status.BundleCurrent = status.BundleVersion == version.Version &&
			bundle.StampedHash(ctx, client) != ""
	}
	status.Components, status.DatabaseInstances = gatherComponents(ctx, client)
	if status.DatabaseInstances > 0 {
		status.DeployedTier = layout.DeriveTier(status.DatabaseInstances)
	}
	return status, nil
}

func gatherComponents(ctx context.Context, client *kube.Client) ([]ComponentStatus, int) {
	database, instances := databaseComponent(ctx, client)
	components := []ComponentStatus{
		database,
		deploymentComponent(ctx, client, "registry", bundle.Namespace, "skali-registry"),
		deploymentComponent(ctx, client, "skalid", bundle.Namespace, "skalid"),
	}
	return components, instances
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

func databaseComponent(ctx context.Context, client *kube.Client) (ComponentStatus, int) {
	resource := client.Dynamic.Resource(schema.GroupVersionResource{
		Group: "postgresql.cnpg.io", Version: "v1", Resource: "clusters",
	}).Namespace(bundle.Namespace)
	cluster, err := resource.Get(ctx, "skali-db", metav1.GetOptions{})
	if err != nil {
		return ComponentStatus{Name: "database", Detail: "not found"}, 0
	}
	phase, _, _ := unstructured.NestedString(cluster.Object, "status", "phase")
	ready, _, _ := unstructured.NestedInt64(cluster.Object, "status", "readyInstances")
	instances, _, _ := unstructured.NestedInt64(cluster.Object, "spec", "instances")
	healthy := phase == "Cluster in healthy state" && ready >= instances
	detail := fmt.Sprintf("%d/%d instances ready", ready, instances)
	if healthy {
		detail = fmt.Sprintf("healthy (%s)", layout.DeriveTier(int(instances)))
	}
	return ComponentStatus{Name: "database", Healthy: healthy, Detail: detail}, int(instances)
}

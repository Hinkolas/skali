package installer

import (
	"context"
	"errors"
	"fmt"
	"sort"
	"strings"
	"time"

	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"

	"github.com/Hinkolas/skali/internal/bundle"
	"github.com/Hinkolas/skali/internal/installer/host"
	"github.com/Hinkolas/skali/internal/kube"
	"github.com/Hinkolas/skali/internal/kubernetes"
	"github.com/Hinkolas/skali/internal/layout"
)

// BundleInventory lists what removing the Skali bundle destroys: every
// project namespace, the platform namespace, the system namespace with its
// databases and registry contents, and the operator namespaces.
type BundleInventory struct {
	ProjectNamespaces []string
	SystemNamespaces  []string
}

// GatherBundleInventory reads what a bundle uninstall would delete.
func GatherBundleInventory(ctx context.Context, client *kube.Client) (*BundleInventory, error) {
	inventory := &BundleInventory{}
	projects, err := client.Clientset.CoreV1().Namespaces().List(ctx, metav1.ListOptions{
		LabelSelector: kubernetes.ManagedSelector,
	})
	if err != nil {
		return nil, fmt.Errorf("list project namespaces: %w", err)
	}
	for _, namespace := range projects.Items {
		inventory.ProjectNamespaces = append(inventory.ProjectNamespaces, namespace.Name)
	}
	sort.Strings(inventory.ProjectNamespaces)
	inventory.SystemNamespaces = append([]string{"skali-platform", bundle.Namespace}, bundle.OperatorNamespaces...)
	return inventory, nil
}

// UninstallBundle removes Skali and all project workloads and data from
// the cluster, keeping bare k3s running. Deletion order: project
// namespaces, the platform namespace, skali-system, then the operator
// namespaces; each wave waits for termination. The record survives with
// its bundle version cleared, so the host re-detects as an uninitialized
// managed server.
func UninstallBundle(ctx context.Context, runner host.Runner, client *kube.Client, record *Record, progress Progress) error {
	if progress == nil {
		progress = silentProgress{}
	}
	inventory, err := GatherBundleInventory(ctx, client)
	if err != nil {
		return err
	}

	waves := [][]string{
		inventory.ProjectNamespaces,
		{"skali-platform"},
		{bundle.Namespace},
		bundle.OperatorNamespaces,
	}
	titles := []string{
		"Remove project namespaces",
		"Remove platform namespace",
		"Remove skali-system",
		"Remove blessed operators",
	}
	for index, wave := range waves {
		progress.Start(titles[index])
		deleted, err := deleteNamespaces(ctx, client, wave)
		if err != nil {
			return err
		}
		if deleted == 0 {
			progress.Skip("nothing to remove")
			continue
		}
		progress.Done(fmt.Sprintf("%d namespace(s)", deleted))
	}

	progress.Start("Update " + RecordPath)
	record.Versions.Bundle = ""
	if err := SaveRecord(ctx, runner, record); err != nil {
		return err
	}
	progress.Done("")
	return nil
}

// deleteNamespaces deletes the named namespaces and waits for termination;
// absent namespaces are skipped.
func deleteNamespaces(ctx context.Context, client *kube.Client, names []string) (int, error) {
	deleted := 0
	for _, name := range names {
		err := client.Clientset.CoreV1().Namespaces().Delete(ctx, name, metav1.DeleteOptions{})
		if apierrors.IsNotFound(err) {
			continue
		}
		if err != nil {
			return deleted, fmt.Errorf("delete namespace %s: %w", name, err)
		}
		deleted++
	}
	deadline := time.Now().Add(10 * time.Minute)
	for {
		remaining := 0
		for _, name := range names {
			_, err := client.Clientset.CoreV1().Namespaces().Get(ctx, name, metav1.GetOptions{})
			if err == nil {
				remaining++
			} else if !apierrors.IsNotFound(err) {
				return deleted, fmt.Errorf("wait for namespace %s: %w", name, err)
			}
		}
		if remaining == 0 {
			return deleted, nil
		}
		if time.Now().After(deadline) {
			return deleted, fmt.Errorf("timed out waiting for %d namespace(s) to terminate", remaining)
		}
		select {
		case <-ctx.Done():
			return deleted, ctx.Err()
		case <-time.After(3 * time.Second):
		}
	}
}

// NodeRemovalPlan states what removing this node means, computed
// read-only before any confirmation.
type NodeRemovalPlan struct {
	NodeName string
	Role     string
	// Servers, Agents, and Total describe the cluster when the API
	// answered; Total 1 otherwise (agents and dead-API servers fall back
	// to the single-node assumption).
	Servers int
	Agents  int
	Total   int
}

// PlanNodeRemoval computes the removal plan and enforces the multi-node
// guards: the last server never leaves while agents remain, and a server
// carrying skali-system data refuses until it is relocated. Agents have
// no kube API access and plan host-side, exactly like servers whose API
// is unreachable (a single-node cluster with a dead API must still be
// uninstallable; that is the recovery path).
func PlanNodeRemoval(ctx context.Context, runner host.Runner, record *Record) (*NodeRemovalPlan, error) {
	plan := &NodeRemovalPlan{NodeName: record.Node.Name, Role: record.Node.Role, Total: 1}
	if record.Node.Role != layout.RoleServer {
		return plan, nil
	}
	client, err := KubeClient(ctx, runner)
	if err != nil {
		return plan, nil
	}
	return planNodeRemovalWith(ctx, client, plan)
}

// planNodeRemovalWith fills the plan from the cluster; split out so the
// guards are testable against a fake clientset.
func planNodeRemovalWith(ctx context.Context, client *kube.Client, plan *NodeRemovalPlan) (*NodeRemovalPlan, error) {
	nodes, err := client.Clientset.CoreV1().Nodes().List(ctx, metav1.ListOptions{})
	if err != nil {
		return plan, nil
	}
	plan.Total = len(nodes.Items)
	for _, node := range nodes.Items {
		if layout.RoleFromLabels(node.Labels) == layout.RoleServer {
			plan.Servers++
		} else {
			plan.Agents++
		}
	}
	if plan.Total <= 1 {
		return plan, nil
	}
	if plan.Servers <= 1 {
		return nil, errors.New("this is the only server; remove the agents first or destroy the cluster per host")
	}
	blocking, err := blockingSystemWorkloads(ctx, client, plan.NodeName)
	if err != nil {
		return nil, err
	}
	if len(blocking) > 0 {
		return nil, fmt.Errorf("skali-system data lives on this node (%s); relocate it first",
			strings.Join(blocking, ", "))
	}
	return plan, nil
}

// blockingSystemWorkloads lists skali-system pods with persistent volume
// claims scheduled on the node; their data would vanish with the node.
func blockingSystemWorkloads(ctx context.Context, client *kube.Client, nodeName string) ([]string, error) {
	pods, err := client.Clientset.CoreV1().Pods(bundle.Namespace).List(ctx, metav1.ListOptions{})
	if err != nil {
		return nil, fmt.Errorf("list skali-system pods: %w", err)
	}
	var blocking []string
	for _, pod := range pods.Items {
		if pod.Spec.NodeName != nodeName {
			continue
		}
		for _, volume := range pod.Spec.Volumes {
			if volume.PersistentVolumeClaim != nil {
				blocking = append(blocking, pod.Name)
				break
			}
		}
	}
	sort.Strings(blocking)
	return blocking, nil
}

// UninstallNode removes k3s and the skali state from this host. On a
// multi-node cluster the leaving server first drains and deletes its own
// node object: k3s's member controller removes the etcd member on node
// delete, and only while the cluster is functional, so the order is
// delete-then-uninstall (the uninstall script does no member removal, and
// a merely stopped server would rejoin on restart). The record is removed
// last so an interrupted uninstall re-detects as damaged rather than
// fresh.
func UninstallNode(ctx context.Context, runner host.Runner, record *Record, plan *NodeRemovalPlan, progress Progress) error {
	if progress == nil {
		progress = silentProgress{}
	}
	if plan == nil {
		plan = &NodeRemovalPlan{NodeName: record.Node.Name, Role: record.Node.Role, Total: 1}
	}
	if plan.Role == layout.RoleServer && plan.Total > 1 {
		if err := removeSelfFromCluster(ctx, runner, plan.NodeName, progress); err != nil {
			return err
		}
	}

	progress.Start("Uninstall k3s")
	if err := uninstallK3s(ctx, runner, record.Node.Role); err != nil {
		return err
	}
	progress.Done("")

	progress.Start("Remove " + StateDir)
	// Everything under StateDir goes except the record, which goes last.
	entries := []string{CacheDir, LogDir}
	for _, entry := range entries {
		if err := runner.Remove(ctx, entry); err != nil {
			return fmt.Errorf("remove %s: %w", entry, err)
		}
	}
	if err := RemoveRecord(ctx, runner); err != nil {
		return fmt.Errorf("remove installation record: %w", err)
	}
	if err := runner.Remove(ctx, StateDir); err != nil {
		return fmt.Errorf("remove %s: %w", StateDir, err)
	}
	progress.Done("")
	return nil
}

// removeSelfFromCluster drains best-effort, deletes the own node object,
// and waits until the removal takes effect. After a successful delete a
// dead local API also counts as done: retiring this member may take the
// local apiserver's etcd backend with it.
func removeSelfFromCluster(ctx context.Context, runner host.Runner, nodeName string, progress Progress) error {
	progress.Start("Drain node " + nodeName)
	result, err := runner.Run(ctx, host.Command{Name: "k3s", Args: []string{
		"kubectl", "drain", nodeName, "--ignore-daemonsets", "--delete-emptydir-data", "--timeout=120s",
	}})
	if err != nil || result.ExitCode != 0 {
		progress.Skip("drain did not complete; continuing with removal")
	} else {
		progress.Done("")
	}

	progress.Start("Remove node from cluster")
	result, err = runner.Run(ctx, host.Command{Name: "k3s", Args: []string{"kubectl", "delete", "node", nodeName}})
	if err != nil {
		return fmt.Errorf("delete node %s: %w", nodeName, err)
	}
	if result.ExitCode != 0 && !strings.Contains(strings.ToLower(result.Stderr), "not found") {
		return fmt.Errorf("delete node %s: exit %d: %s", nodeName, result.ExitCode,
			strings.TrimSpace(result.Stderr))
	}
	deadline := time.Now().Add(2 * time.Minute)
	for {
		result, err := runner.Run(ctx, host.Command{Name: "k3s", Args: []string{"kubectl", "get", "node", nodeName}})
		if err != nil || result.ExitCode != 0 {
			progress.Done("")
			return nil
		}
		if time.Now().After(deadline) {
			return fmt.Errorf("node %s is still present after its removal; "+
				"verify from another server with k3s kubectl get nodes", nodeName)
		}
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-time.After(3 * time.Second):
		}
	}
}

// DescribeBundleRemoval renders what a bundle uninstall destroys, for the
// confirmation prompt.
func DescribeBundleRemoval(inventory *BundleInventory) string {
	var builder strings.Builder
	builder.WriteString("Removing the Skali bundle destroys, permanently:\n")
	if len(inventory.ProjectNamespaces) == 0 {
		builder.WriteString("  - no project namespaces exist\n")
	} else {
		builder.WriteString("  - project namespaces: " + strings.Join(inventory.ProjectNamespaces, ", ") + "\n")
	}
	builder.WriteString("  - the skali-system namespace: control-plane state, databases, and all registry contents\n")
	builder.WriteString("  - the blessed operators: " + strings.Join(bundle.OperatorNamespaces, ", ") + "\n")
	builder.WriteString("Bare k3s keeps running.")
	return builder.String()
}

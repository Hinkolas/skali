package installer

import (
	"context"
	"errors"
	"fmt"
	"sort"
	"strings"
	"time"

	corev1 "k8s.io/api/core/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/runtime/schema"
	"k8s.io/apimachinery/pkg/types"
	"k8s.io/client-go/util/retry"

	"github.com/Hinkolas/skali/internal/bundle"
	"github.com/Hinkolas/skali/internal/clusterstate"
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

const (
	skalidRBACName      = "skalid"
	skalidDeployment    = "skalid"
	reconcilerStopTitle = "Stop Skali reconciliation"
)

// Namespace-termination pacing. Bundle removal is an explicitly confirmed
// destroy, so a namespace that is still terminating after a short grace
// period is escalated to finalization instead of making the command spin for
// ten minutes. Vars let focused tests collapse the waits.
var (
	namespaceTerminationGrace = 90 * time.Second
	namespaceForceDeadline    = 30 * time.Second
	namespacePollInterval     = 3 * time.Second
)

type progressNoter interface {
	Note(string)
}

func note(progress Progress, line string) {
	if noter, ok := progress.(progressNoter); ok {
		noter.Note(line)
	}
}

// UninstallBundle removes Skali and all project workloads and data from
// the cluster, keeping bare k3s running. The inventory is captured while
// the control plane still exists, then skalid is denied cluster access and
// scaled down before any project namespace is deleted. That ordering is
// essential: namespace deletion events otherwise ask the level-triggered
// reconciler to recreate the active environment immediately. Deletion then
// proceeds through project, platform, system, and operator namespaces. The
// record survives with its bundle version cleared, so the host re-detects
// as an uninitialized managed server.
func UninstallBundle(ctx context.Context, runner host.Runner, client *kube.Client, record *Record, progress Progress) error {
	if progress == nil {
		progress = silentProgress{}
	}
	inventory, err := GatherBundleInventory(ctx, client)
	if err != nil {
		return err
	}

	if err := stopBundleReconciler(ctx, client, progress); err != nil {
		return err
	}
	confirmLonghornDeletion(ctx, client)

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
		deleted, err := deleteNamespaces(ctx, client, wave, progress)
		if err != nil {
			return err
		}
		if deleted == 0 {
			progress.Skip("nothing to remove")
			continue
		}
		progress.Done(fmt.Sprintf("%d namespace(s)", deleted))
	}
	if err := removePriorityClasses(ctx, client, progress); err != nil {
		return err
	}

	progress.Start("Update " + RecordPath)
	record.Versions.Bundle = ""
	if err := SaveRecord(ctx, runner, record); err != nil {
		return err
	}
	progress.Done("")
	return nil
}

// confirmLonghornDeletion flips Longhorn's deleting-confirmation-flag
// setting so its admission webhook stops blocking resource deletion and the
// longhorn-system namespace terminates through the normal path instead of
// the force-finalization escalation (which can strand host-side replica
// data). Best-effort: pre-Longhorn installations and dev clusters have
// neither the CRD nor the setting, and a failed flip only makes the
// namespace wave slower, not wrong.
func confirmLonghornDeletion(ctx context.Context, client *kube.Client) {
	if client.Dynamic == nil {
		return
	}
	settings := client.Dynamic.Resource(schema.GroupVersionResource{
		Group: "longhorn.io", Version: "v1beta2", Resource: "settings",
	}).Namespace("longhorn-system")
	setting, err := settings.Get(ctx, "deleting-confirmation-flag", metav1.GetOptions{})
	if err != nil {
		return
	}
	setting = setting.DeepCopy()
	if err := unstructured.SetNestedField(setting.Object, "true", "value"); err != nil {
		return
	}
	_, _ = settings.Update(ctx, setting, metav1.UpdateOptions{})
}

// removePriorityClasses deletes the bundle's cluster-scoped PriorityClasses
// once no namespace references them; like the RBAC objects they would
// otherwise survive a namespace-only uninstall.
func removePriorityClasses(ctx context.Context, client *kube.Client, progress Progress) error {
	progress.Start("Remove priority classes")
	removed := 0
	for _, name := range []string{layout.PriorityClassCritical, layout.PriorityClassHigh, layout.PriorityClassNormal} {
		err := client.Clientset.SchedulingV1().PriorityClasses().Delete(ctx, name, metav1.DeleteOptions{})
		switch {
		case err == nil:
			removed++
		case !apierrors.IsNotFound(err):
			return fmt.Errorf("remove priority class %s: %w", name, err)
		}
	}
	if removed == 0 {
		progress.Skip("nothing to remove")
		return nil
	}
	progress.Done(fmt.Sprintf("%d class(es)", removed))
	return nil
}

// stopBundleReconciler removes skalid's cluster authorization before
// scaling its deployment down. Revoking authorization first closes the
// race with an already-running worker: even while its pod is terminating,
// it can no longer recreate a namespace. The fixed-name RBAC objects are
// bundle-owned cluster-scoped resources and would otherwise survive a
// namespace-only uninstall.
func stopBundleReconciler(ctx context.Context, client *kube.Client, progress Progress) error {
	progress.Start(reconcilerStopTitle)
	changed := false

	err := client.Clientset.RbacV1().ClusterRoleBindings().Delete(
		ctx, skalidRBACName, metav1.DeleteOptions{})
	switch {
	case err == nil:
		changed = true
	case !apierrors.IsNotFound(err):
		return fmt.Errorf("revoke skalid cluster role binding: %w", err)
	}

	err = client.Clientset.RbacV1().ClusterRoles().Delete(
		ctx, skalidRBACName, metav1.DeleteOptions{})
	switch {
	case err == nil:
		changed = true
	case !apierrors.IsNotFound(err):
		return fmt.Errorf("remove skalid cluster role: %w", err)
	}

	scaled := false
	err = retry.RetryOnConflict(retry.DefaultRetry, func() error {
		deployment, err := client.Clientset.AppsV1().Deployments(bundle.Namespace).
			Get(ctx, skalidDeployment, metav1.GetOptions{})
		if apierrors.IsNotFound(err) {
			return nil
		}
		if err != nil {
			return err
		}
		if deployment.Spec.Replicas != nil && *deployment.Spec.Replicas == 0 {
			return nil
		}
		deployment = deployment.DeepCopy()
		replicas := int32(0)
		deployment.Spec.Replicas = &replicas
		if _, err := client.Clientset.AppsV1().Deployments(bundle.Namespace).
			Update(ctx, deployment, metav1.UpdateOptions{}); err != nil {
			return err
		}
		scaled = true
		return nil
	})
	if err != nil {
		return fmt.Errorf("scale skalid deployment to zero: %w", err)
	}
	changed = changed || scaled

	if !changed {
		progress.Skip("already stopped")
		return nil
	}
	progress.Done("cluster access revoked, deployment scaled to zero")
	return nil
}

type namespaceDeletion struct {
	name string
	uid  types.UID
}

// deleteNamespaces deletes the named namespaces with UID preconditions and
// waits for termination; absent namespaces are skipped. A UID change is
// reported immediately as recreation instead of being mistaken for a slow
// termination. Namespaces genuinely wedged on finalizers are surfaced while
// waiting and finalized after the grace period.
func deleteNamespaces(ctx context.Context, client *kube.Client, names []string, progress Progress) (int, error) {
	deletions := make([]namespaceDeletion, 0, len(names))
	for _, name := range names {
		namespace, err := client.Clientset.CoreV1().Namespaces().
			Get(ctx, name, metav1.GetOptions{})
		if apierrors.IsNotFound(err) {
			continue
		}
		if err != nil {
			return len(deletions), fmt.Errorf("read namespace %s for deletion: %w", name, err)
		}
		uid := namespace.UID
		err = client.Clientset.CoreV1().Namespaces().Delete(ctx, name, metav1.DeleteOptions{
			Preconditions: &metav1.Preconditions{UID: &uid},
		})
		if apierrors.IsNotFound(err) {
			continue
		}
		if err != nil {
			return len(deletions), fmt.Errorf("delete namespace %s: %w", name, err)
		}
		deletions = append(deletions, namespaceDeletion{name: name, uid: uid})
	}
	deleted := len(deletions)
	if deleted == 0 {
		return 0, nil
	}

	stuck, err := waitNamespacesGone(ctx, client, deletions, progress)
	if err != nil {
		return deleted, err
	}
	if err := forceNamespacesGone(ctx, client, stuck, progress); err != nil {
		return deleted, err
	}
	return deleted, nil
}

// waitNamespacesGone waits through the normal namespace-controller path,
// returning only namespaces still present after the grace period.
func waitNamespacesGone(ctx context.Context, client *kube.Client, deletions []namespaceDeletion, progress Progress) ([]namespaceDeletion, error) {
	deadline := time.Now().Add(namespaceTerminationGrace)
	for {
		remaining := make([]namespaceDeletion, 0, len(deletions))
		blockers := make([]string, 0, len(deletions))
		for _, deletion := range deletions {
			namespace, err := client.Clientset.CoreV1().Namespaces().
				Get(ctx, deletion.name, metav1.GetOptions{})
			if apierrors.IsNotFound(err) {
				continue
			}
			if err != nil {
				return nil, fmt.Errorf("wait for namespace %s: %w", deletion.name, err)
			}
			if namespace.UID != deletion.uid {
				return nil, fmt.Errorf("namespace %s was recreated during uninstall "+
					"(deleted uid %s, current uid %s); stop the controller that owns it and retry",
					deletion.name, deletion.uid, namespace.UID)
			}
			remaining = append(remaining, deletion)
			if blocker := namespaceBlocker(namespace); blocker != "" {
				blockers = append(blockers, deletion.name+": "+blocker)
			} else {
				blockers = append(blockers, deletion.name)
			}
		}
		if len(remaining) == 0 {
			return nil, nil
		}
		note(progress, "waiting for "+strings.Join(blockers, "; "))
		if time.Now().After(deadline) {
			return remaining, nil
		}
		select {
		case <-ctx.Done():
			return nil, ctx.Err()
		case <-time.After(namespacePollInterval):
		}
	}
}

// namespaceBlocker summarizes active namespace termination conditions.
func namespaceBlocker(namespace *corev1.Namespace) string {
	var reasons []string
	for _, condition := range namespace.Status.Conditions {
		if condition.Status != corev1.ConditionTrue {
			continue
		}
		message := strings.TrimSpace(condition.Message)
		if message == "" {
			message = string(condition.Reason)
		}
		if message != "" {
			reasons = append(reasons, message)
		}
	}
	return strings.Join(reasons, "; ")
}

// forceNamespacesGone clears every stuck namespace's kubernetes finalizer
// through the finalize subresource, then verifies the exact UIDs disappear
// under one shared deadline. It never finalizes a same-named replacement.
func forceNamespacesGone(ctx context.Context, client *kube.Client, deletions []namespaceDeletion, progress Progress) error {
	for _, deletion := range deletions {
		note(progress, "forcing termination of "+deletion.name)
		namespace, err := client.Clientset.CoreV1().Namespaces().
			Get(ctx, deletion.name, metav1.GetOptions{})
		if apierrors.IsNotFound(err) {
			continue
		}
		if err != nil {
			return fmt.Errorf("read stuck namespace %s: %w", deletion.name, err)
		}
		if namespace.UID != deletion.uid {
			return fmt.Errorf("namespace %s was recreated before forced termination "+
				"(deleted uid %s, current uid %s)", deletion.name, deletion.uid, namespace.UID)
		}
		if len(namespace.Spec.Finalizers) == 0 {
			continue
		}
		namespace = namespace.DeepCopy()
		namespace.Spec.Finalizers = nil
		if _, err := client.Clientset.CoreV1().Namespaces().
			Finalize(ctx, namespace, metav1.UpdateOptions{}); err != nil {
			return fmt.Errorf("force namespace %s: %w", deletion.name, err)
		}
	}

	deadline := time.Now().Add(namespaceForceDeadline)
	for {
		var remaining []string
		for _, deletion := range deletions {
			namespace, err := client.Clientset.CoreV1().Namespaces().
				Get(ctx, deletion.name, metav1.GetOptions{})
			if apierrors.IsNotFound(err) {
				continue
			}
			if err != nil {
				return fmt.Errorf("confirm namespace %s removed: %w", deletion.name, err)
			}
			if namespace.UID != deletion.uid {
				return fmt.Errorf("namespace %s was recreated after forced termination "+
					"(deleted uid %s, current uid %s)", deletion.name, deletion.uid, namespace.UID)
			}
			remaining = append(remaining, deletion.name)
		}
		if len(remaining) == 0 {
			return nil
		}
		if time.Now().After(deadline) {
			return fmt.Errorf("namespace(s) %s did not terminate after clearing finalizers; "+
				"inspect with kubectl get namespace <name> -o yaml", strings.Join(remaining, ", "))
		}
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-time.After(namespacePollInterval):
		}
	}
}

// NodeRemovalPlan states what removing this node means, computed
// read-only before any confirmation.
type NodeRemovalPlan struct {
	NodeName string
	Role     string
	// ClusterReachable reports whether the node inventory was read. A
	// joining server cleaned up locally while this is false may leave a
	// stale node or etcd member behind on the surviving servers.
	ClusterReachable bool
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
	plan.ClusterReachable = true
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
	startAttempted := record.RegistrationMayHaveStarted()
	if startAttempted && plan.Role == layout.RoleServer && plan.Total > 1 {
		if err := removeSelfFromCluster(ctx, runner, plan.NodeName, progress); err != nil {
			return err
		}
	}

	progress.Start("Uninstall k3s")
	script := k3sUninstallScript
	if record.Node.Role == layout.RoleAgent {
		script = k3sAgentUninstallScript
	}
	info, statErr := runner.Stat(ctx, script)
	if statErr != nil {
		return statErr
	}
	if info.Exists {
		if err := uninstallK3s(ctx, runner, record.Node.Role); err != nil {
			return err
		}
		progress.Done("")
	} else if !startAttempted {
		if err := runner.Remove(ctx, K3sConfigDir); err != nil {
			return fmt.Errorf("remove staged k3s config: %w", err)
		}
		progress.Skip("k3s was never started and no uninstall script exists")
	} else {
		// A partial or manually damaged install may have lost only the
		// upstream uninstall helper. Re-run the pinned installer with
		// startup disabled to reconstruct the binary, unit, and script,
		// then immediately execute the normal upstream cleanup.
		progress.Skip("upstream uninstall script is missing; reconstructing it")
		if err := installK3sFiles(ctx, runner, k3sNode{Role: record.Node.Role}, progress); err != nil {
			return fmt.Errorf("restore missing upstream uninstall script: %w", err)
		}
		if err := uninstallK3s(ctx, runner, record.Node.Role); err != nil {
			return err
		}
	}

	if record.Reconciled() {
		progress.Start("Remove Skali host services")
		if err := RemoveHostd(ctx, runner, !record.EnrolledOnly()); err != nil {
			return err
		}
		progress.Done("")
	}

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

// QuiesceReconciledCluster prevents the coordinator from recreating state
// while a final seed uninstall removes the platform and coordinator
// namespace. It is deliberately separate from node removal because a
// multi-node reconciled cluster must first remove its other nodes through
// the candidate/apply workflow.
func QuiesceReconciledCluster(ctx context.Context, runner host.Runner,
	client *kube.Client) error {
	store := &clusterstate.Store{Client: client.Clientset}
	if _, err := store.Update(ctx, func(state *clusterstate.State) error {
		state.Decommissioning = true
		return nil
	}); err != nil {
		return fmt.Errorf("mark cluster decommissioning: %w", err)
	}
	if err := StopHostd(ctx, runner); err != nil {
		return fmt.Errorf("stop coordinator reconciliation: %w", err)
	}
	return nil
}

func RemoveCoordinatorNamespace(ctx context.Context, client *kube.Client,
	progress Progress) error {
	progress.Start("Remove " + clusterstate.Namespace)
	deleted, err := deleteNamespaces(ctx, client, []string{clusterstate.Namespace}, progress)
	if err != nil {
		return err
	}
	if deleted == 0 {
		progress.Skip("already absent")
	} else {
		progress.Done("")
	}
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

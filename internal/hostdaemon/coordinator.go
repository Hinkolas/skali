package hostdaemon

import (
	"context"
	"crypto/tls"
	"errors"
	"fmt"
	"log/slog"
	"net"
	"net/http"
	"net/url"
	"os"
	"slices"
	"sort"
	"strings"
	"time"

	appsv1 "k8s.io/api/apps/v1"
	coordinationv1 "k8s.io/api/coordination/v1"
	corev1 "k8s.io/api/core/v1"
	policyv1 "k8s.io/api/policy/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/labels"
	"k8s.io/apimachinery/pkg/types"
	"k8s.io/client-go/kubernetes"
	"k8s.io/client-go/tools/clientcmd"
	"k8s.io/client-go/util/retry"

	"github.com/google/uuid"

	"github.com/Hinkolas/skali/internal/bundle"
	"github.com/Hinkolas/skali/internal/clusterstate"
	"github.com/Hinkolas/skali/internal/installer"
	"github.com/Hinkolas/skali/internal/installer/host"
	skalikube "github.com/Hinkolas/skali/internal/kubernetes"
	"github.com/Hinkolas/skali/internal/layout"
)

const coordinatorLease = "skali-cluster-coordinator"

var errPlatformInitializationRequired = errors.New("platform initialization is required")

type CoordinatorDaemon struct {
	Kubeconfig string
	// Listen overrides the resolved listener set with explicit
	// "address:port" entries; empty resolves them from this node.
	Listen   []string
	Logger   *slog.Logger
	Identity string
	Runner   host.Runner
}

func (d *CoordinatorDaemon) Run(ctx context.Context) error {
	if d.Kubeconfig == "" {
		d.Kubeconfig = installer.K3sKubeconfigPath
	}
	if d.Runner == nil {
		d.Runner = host.Local{}
	}
	config, err := clientcmd.BuildConfigFromFlags("", d.Kubeconfig)
	if err != nil {
		return fmt.Errorf("load coordinator kubeconfig: %w", err)
	}
	clientset, err := kubernetes.NewForConfig(config)
	if err != nil {
		return err
	}
	if len(d.Listen) == 0 {
		agentConfig, err := installer.LoadAgentConfig(ctx, d.Runner)
		if err != nil {
			return fmt.Errorf("load coordinator node identity: %w", err)
		}
		node, err := clientset.CoreV1().Nodes().Get(ctx, agentConfig.NodeName,
			metav1.GetOptions{})
		if err != nil {
			return fmt.Errorf("load coordinator node %s: %w", agentConfig.NodeName, err)
		}
		d.Listen, err = d.resolveListenAddresses(ctx, *node)
		if err != nil {
			return err
		}
	}
	store := &clusterstate.Store{Client: clientset}
	if d.Identity == "" {
		hostname, _ := os.Hostname()
		d.Identity = hostname + "-" + uuid.NewString()
	}

	coordinator := &clusterstate.Coordinator{
		Store: store, Logger: d.Logger,
		ActionFor: func(ctx context.Context, nodeID string) (clusterstate.AgentAction, error) {
			return d.actionFor(ctx, store, nodeID)
		},
		ActionDone: func(ctx context.Context, nodeID string, report clusterstate.AgentReport) error {
			return d.actionDone(ctx, store, nodeID, report)
		},
	}
	tlsConfig, err := coordinator.TLSConfig(ctx)
	if err != nil {
		return err
	}
	server := &http.Server{
		Addr: d.Listen[0], Handler: coordinator.Handler(),
		TLSConfig: tlsConfig, ReadHeaderTimeout: 10 * time.Second,
	}
	// One listener per address rather than one wildcard listener: k3s owns
	// loopback 6444 for local kube-apiserver access, and a multi-homed node
	// must answer enrollment on its private and its public address at once.
	var listeners []net.Listener
	var listenErrs []string
	for _, address := range d.Listen {
		listener, err := net.Listen("tcp", address)
		if err != nil {
			listenErrs = append(listenErrs, fmt.Sprintf("%s: %v", address, err))
			d.log("coordinator address is unavailable", "address", address, "error", err)
			continue
		}
		listeners = append(listeners, listener)
		d.log("coordinator listening", "address", address)
	}
	if len(listeners) == 0 {
		return fmt.Errorf("listen for coordinator enrollment: %s", strings.Join(listenErrs, "; "))
	}
	serverErr := make(chan error, len(listeners))
	for _, listener := range listeners {
		tlsListener := tls.NewListener(listener, tlsConfig)
		go func() {
			err := server.Serve(tlsListener)
			if errors.Is(err, http.ErrServerClosed) {
				err = nil
			}
			serverErr <- err
		}()
	}
	go d.controlLoop(ctx, store, clientset)

	select {
	case <-ctx.Done():
		shutdownCtx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
		defer cancel()
		return server.Shutdown(shutdownCtx)
	case err := <-serverErr:
		return err
	}
}

// resolveListenAddresses deliberately binds explicit addresses instead of
// the wildcard: k3s owns loopback port 6444 for local kube-apiserver
// access, so :6444 would collide with it. The set is the node's declared
// cluster address, its public addresses when the operator asked the
// coordinator to serve them, and always the address the running k3s
// advertises, so agents enrolled against the old single address keep
// working across an upgrade.
func (d *CoordinatorDaemon) resolveListenAddresses(ctx context.Context,
	node corev1.Node) ([]string, error) {
	advertised := ""
	for _, address := range node.Status.Addresses {
		if address.Type == corev1.NodeInternalIP && net.ParseIP(address.Address) != nil {
			advertised = address.Address
			break
		}
	}
	record, err := installer.LoadRecord(ctx, d.Runner)
	if err != nil {
		if advertised == "" {
			return nil, fmt.Errorf("coordinator node %s has no valid InternalIP", node.Name)
		}
		record = nil
	}
	plan, err := installer.PlanCoordinatorBind(ctx, d.Runner, record, advertised)
	if err != nil {
		return nil, err
	}
	for _, skipped := range plan.Skipped {
		d.log("declared coordinator address is not assigned to this host; not binding it",
			"address", skipped)
	}
	addresses := make([]string, 0, len(plan.Addresses))
	for _, address := range plan.Addresses {
		addresses = append(addresses,
			net.JoinHostPort(address, clusterstate.DefaultCoordinatorPort))
	}
	return addresses, nil
}

func (d *CoordinatorDaemon) controlLoop(ctx context.Context, store *clusterstate.Store,
	client kubernetes.Interface) {
	ticker := time.NewTicker(3 * time.Second)
	defer ticker.Stop()
	var reconcileCancel context.CancelFunc
	var reconcileDone <-chan error
	for {
		select {
		case <-ctx.Done():
			if reconcileCancel != nil {
				reconcileCancel()
			}
			return
		case err := <-reconcileDone:
			reconcileDone = nil
			reconcileCancel = nil
			if err == nil || errors.Is(err, context.Canceled) {
				continue
			}
			d.log("cluster reconciliation tick failed", "error", err)
			_, _ = store.Update(ctx, func(state *clusterstate.State) error {
				return clusterstate.FailOperation(state, err.Error(), time.Now())
			})
		case <-ticker.C:
			leader, err := d.acquireOrRenew(ctx, client)
			if err != nil {
				d.log("coordinator leader lease failed", "error", err)
				if reconcileCancel != nil {
					reconcileCancel()
				}
				continue
			}
			if !leader {
				if reconcileCancel != nil {
					reconcileCancel()
				}
				continue
			}
			if reconcileDone == nil {
				reconcileCtx, cancel := context.WithCancel(ctx)
				done := make(chan error, 1)
				reconcileCancel = cancel
				reconcileDone = done
				go func() {
					done <- d.reconcile(reconcileCtx, store)
				}()
			}
		}
	}
}

func (d *CoordinatorDaemon) acquireOrRenew(ctx context.Context,
	client kubernetes.Interface) (bool, error) {
	now := metav1.NewMicroTime(time.Now().UTC())
	duration := int32(15)
	leases := client.CoordinationV1().Leases(clusterstate.Namespace)
	lease, err := leases.Get(ctx, coordinatorLease, metav1.GetOptions{})
	if apierrors.IsNotFound(err) {
		_, err = leases.Create(ctx, &coordinationv1.Lease{
			ObjectMeta: metav1.ObjectMeta{Name: coordinatorLease},
			Spec: coordinationv1.LeaseSpec{
				HolderIdentity: &d.Identity, LeaseDurationSeconds: &duration,
				AcquireTime: &now, RenewTime: &now,
			},
		}, metav1.CreateOptions{})
		return err == nil, ignoreConflict(err)
	}
	if err != nil {
		return false, err
	}
	expired := lease.Spec.RenewTime == nil || lease.Spec.LeaseDurationSeconds == nil ||
		lease.Spec.RenewTime.Add(time.Duration(*lease.Spec.LeaseDurationSeconds)*time.Second).
			Before(now.Time)
	if lease.Spec.HolderIdentity != nil && *lease.Spec.HolderIdentity != d.Identity && !expired {
		return false, nil
	}
	lease.Spec.HolderIdentity = &d.Identity
	lease.Spec.LeaseDurationSeconds = &duration
	lease.Spec.RenewTime = &now
	if lease.Spec.AcquireTime == nil || expired {
		lease.Spec.AcquireTime = &now
	}
	_, err = leases.Update(ctx, lease, metav1.UpdateOptions{})
	return err == nil, ignoreConflict(err)
}

func ignoreConflict(err error) error {
	if apierrors.IsConflict(err) {
		return nil
	}
	return err
}

func (d *CoordinatorDaemon) actionFor(ctx context.Context, store *clusterstate.Store,
	nodeID string) (clusterstate.AgentAction, error) {
	state, err := store.Load(ctx)
	if err != nil {
		return clusterstate.AgentAction{}, err
	}
	node, exists := state.Nodes[nodeID]
	if !exists {
		return clusterstate.AgentAction{}, errors.New("node is not enrolled")
	}
	if node.Phase == clusterstate.NodePhaseUninstalling ||
		node.Phase == clusterstate.NodePhaseAwaitingCleanup {
		if state.CurrentOperation == "" {
			return clusterstate.AgentAction{
				ID: "cleanup-" + nodeID, Type: "cleanup",
			}, nil
		}
		if state.CurrentOperation != "" {
			operation := state.Operations[state.CurrentOperation]
			if step, ok := operation.NodeSteps[nodeID]; !ok ||
				step.Action != clusterstate.NodeActionRemove ||
				step.Phase == clusterstate.StepComplete {
				return clusterstate.AgentAction{
					ID: "cleanup-" + nodeID, Type: "cleanup",
				}, nil
			}
		}
	}
	if node.Phase == clusterstate.NodePhaseRemoved {
		return clusterstate.AgentAction{}, nil
	}
	if state.CurrentOperation == "" {
		return clusterstate.AgentAction{}, nil
	}
	runnable, err := clusterstate.RunnableNodeActions(state)
	if err != nil {
		return clusterstate.AgentAction{}, err
	}
	step, ok := runnable[nodeID]
	if !ok {
		return clusterstate.AgentAction{}, nil
	}
	attemptID := step.AttemptID
	if attemptID == "" || step.Phase == clusterstate.StepFailed {
		attemptID = uuid.NewString()
		_, err = store.Update(ctx, func(current *clusterstate.State) error {
			return clusterstate.StartNodeAction(current, nodeID, attemptID, time.Now())
		})
		if err != nil {
			return clusterstate.AgentAction{}, err
		}
	}
	state, err = store.Load(ctx)
	if err != nil {
		return clusterstate.AgentAction{}, err
	}
	operation := state.Operations[state.CurrentOperation]
	target := state.Revisions[operation.TargetRevision]
	from := state.Revisions[operation.FromRevision]
	action := clusterstate.AgentAction{ID: attemptID, Type: step.Action}
	switch step.Action {
	case clusterstate.NodeActionInstall:
		desired := target.Nodes[nodeID]
		action.Cluster = state.Cluster
		action.Role = desired.Role
		action.NodeName = desired.Name
		action.NodeIP = state.Nodes[nodeID].NodeIP
		action.Capabilities = append([]string(nil), desired.Capabilities...)
		action.Server, err = k3sJoinEndpoint(state)
		if err != nil {
			return clusterstate.AgentAction{}, err
		}
		action.K3sToken, action.PullSecret, err = installer.JoinCredentials(ctx, d.Runner, desired.Role)
		if err != nil {
			return clusterstate.AgentAction{}, err
		}
	case clusterstate.NodeActionCapabilities:
		desired := target.Nodes[nodeID]
		action.Capabilities = append([]string(nil), desired.Capabilities...)
	case clusterstate.NodeActionRemove:
		action.Role = from.Nodes[nodeID].Role
		if !step.ClusterPrepared {
			if err := d.prepareNodeRemoval(ctx, from.Nodes[nodeID].Name); err != nil {
				_, _ = store.Update(ctx, func(current *clusterstate.State) error {
					return clusterstate.CompleteNodeAction(current, nodeID, attemptID,
						false, err.Error(), time.Now())
				})
				return clusterstate.AgentAction{}, err
			}
			if _, err := store.Update(ctx, func(current *clusterstate.State) error {
				return clusterstate.MarkNodeRemovalPrepared(current, nodeID, time.Now())
			}); err != nil {
				return clusterstate.AgentAction{}, err
			}
		}
	default:
		return clusterstate.AgentAction{}, fmt.Errorf("unknown node action %q", step.Action)
	}
	return action, nil
}

// prepareNodeRemoval performs the cluster-visible half of removal while a
// healthy coordinator still has administrative access. It respects PDBs,
// waits for drain, and deletes the Node object before the remote agent
// removes k3s. Repeating it is safe when an action response was lost.
func (d *CoordinatorDaemon) prepareNodeRemoval(ctx context.Context, nodeName string) error {
	client, err := installer.KubeClient(ctx, d.Runner)
	if err != nil {
		return err
	}
	_, err = client.Clientset.CoreV1().Nodes().Get(ctx, nodeName, metav1.GetOptions{})
	if apierrors.IsNotFound(err) {
		return nil
	}
	if err != nil {
		return fmt.Errorf("read node %s before drain: %w", nodeName, err)
	}
	result, err := d.Runner.Run(ctx, host.Command{
		Name: "k3s", Args: []string{
			"kubectl", "drain", nodeName,
			"--ignore-daemonsets", "--delete-emptydir-data", "--timeout=10m",
		},
	})
	if err != nil {
		return fmt.Errorf("drain node %s: %w", nodeName, err)
	}
	if result.ExitCode != 0 {
		return fmt.Errorf("drain node %s: exit %d: %s", nodeName,
			result.ExitCode, strings.TrimSpace(result.Stderr))
	}
	err = client.Clientset.CoreV1().Nodes().Delete(ctx, nodeName, metav1.DeleteOptions{})
	if apierrors.IsNotFound(err) {
		return nil
	}
	if err != nil {
		return fmt.Errorf("delete node %s after drain: %w", nodeName, err)
	}
	return nil
}

func (d *CoordinatorDaemon) actionDone(ctx context.Context, store *clusterstate.Store,
	nodeID string, report clusterstate.AgentReport) error {
	_, err := store.Update(ctx, func(state *clusterstate.State) error {
		if report.ActionID == "cleanup-"+nodeID {
			if !report.ActionOK {
				return fmt.Errorf("node cleanup failed: %s", report.Error)
			}
			return clusterstate.CompleteNodeCleanup(state, nodeID, time.Now())
		}
		return clusterstate.CompleteNodeAction(state, nodeID, report.ActionID,
			report.ActionOK, report.Error, time.Now())
	})
	return err
}

func (d *CoordinatorDaemon) reconcile(ctx context.Context, store *clusterstate.Store) error {
	state, err := store.Load(ctx)
	if err != nil {
		return err
	}
	if state.CurrentOperation == "" {
		return nil
	}
	if state.Decommissioning || state.ReconciliationPaused {
		return nil
	}
	operation := state.Operations[state.CurrentOperation]
	target := state.Revisions[operation.TargetRevision]
	runnable, err := clusterstate.RunnableNodeActions(state)
	if err != nil {
		return err
	}
	if len(runnable) > 0 {
		// The leader performs cluster-visible drain/member deletion even
		// when the host is offline. The remote agent remains responsible
		// for removing only its installer-owned local state.
		for nodeID, step := range runnable {
			if step.Action != clusterstate.NodeActionRemove || step.ClusterPrepared {
				continue
			}
			from := state.Revisions[operation.FromRevision]
			node := from.Nodes[nodeID]
			if err := d.prepareNodeRemoval(ctx, node.Name); err != nil {
				attemptID := step.AttemptID
				if attemptID == "" {
					attemptID = uuid.NewString()
				}
				_, _ = store.Update(ctx, func(current *clusterstate.State) error {
					if err := clusterstate.StartNodeAction(current, nodeID,
						attemptID, time.Now()); err != nil {
						return err
					}
					return clusterstate.CompleteNodeAction(current, nodeID,
						attemptID, false, err.Error(), time.Now())
				})
				return err
			}
			if _, err := store.Update(ctx, func(current *clusterstate.State) error {
				return clusterstate.MarkNodeRemovalPrepared(current, nodeID, time.Now())
			}); err != nil {
				return err
			}
		}
		return nil
	}
	if !nodeActionsComplete(operation, clusterstate.NodeActionInstall,
		clusterstate.NodeActionCapabilities) {
		return nil
	}
	if !operation.TopologyActivated {
		coordinators, err := d.activateTopology(ctx, target)
		if err != nil {
			return err
		}
		if err := d.moveInvalidManagedPods(ctx); err != nil {
			return err
		}
		if _, err := store.Update(ctx, func(state *clusterstate.State) error {
			for nodeID, endpoint := range coordinators {
				node := state.Nodes[nodeID]
				node.Coordinator = endpoint
				node.UpdatedAt = time.Now().UTC().Truncate(time.Second)
				state.Nodes[nodeID] = node
			}
			return clusterstate.MarkTopologyActivated(state, time.Now())
		}); err != nil {
			return err
		}
		return nil
	}

	if operation.Phase != clusterstate.OperationRemoving &&
		operation.Phase != clusterstate.OperationRebalancing &&
		operation.Phase != clusterstate.OperationVerifying {
		if target.Platform.Enabled {
			if err := d.reconcilePlatform(ctx); err != nil {
				if errors.Is(err, errPlatformInitializationRequired) {
					_, updateErr := store.Update(ctx, func(state *clusterstate.State) error {
						return clusterstate.AwaitPlatformInitialization(state, time.Now())
					})
					return updateErr
				}
				_, _ = store.Update(ctx, func(state *clusterstate.State) error {
					return clusterstate.AdvancePlatform(state, false, err.Error(), time.Now())
				})
				return err
			}
		}
		if _, err := store.Update(ctx, func(state *clusterstate.State) error {
			return clusterstate.AdvancePlatform(state, true, "", time.Now())
		}); err != nil {
			return err
		}
		return nil
	}

	state, err = store.Load(ctx)
	if err != nil {
		return err
	}
	operation = state.Operations[state.CurrentOperation]
	if clusterstate.NeedsEtcdSnapshot(state) {
		if err := d.snapshotEtcd(ctx, operation.ID); err != nil {
			_, _ = store.Update(ctx, func(state *clusterstate.State) error {
				return clusterstate.MarkEtcdSnapshot(state, false, err.Error(), time.Now())
			})
			return err
		}
		if _, err := store.Update(ctx, func(state *clusterstate.State) error {
			return clusterstate.MarkEtcdSnapshot(state, true, "", time.Now())
		}); err != nil {
			return err
		}
		return nil
	}
	if !nodeActionsComplete(operation, clusterstate.NodeActionRemove) {
		return nil
	}
	if operation.RebalanceWorkloads && operation.Phase == clusterstate.OperationRebalancing {
		if err := d.rebalanceWorkloads(ctx); err != nil {
			return err
		}
		_, err = store.Update(ctx, func(state *clusterstate.State) error {
			op := state.Operations[state.CurrentOperation]
			op.Phase = clusterstate.OperationVerifying
			op.UpdatedAt = time.Now().UTC().Truncate(time.Second)
			state.Operations[op.ID] = op
			return nil
		})
		return err
	}
	if operation.Phase == clusterstate.OperationVerifying ||
		operation.Phase == clusterstate.OperationRemoving {
		for id, step := range operation.NodeSteps {
			if step.Action == clusterstate.NodeActionRemove &&
				state.Nodes[id].Phase != clusterstate.NodePhaseRemoved {
				return nil
			}
		}
		from := state.Revisions[operation.FromRevision]
		target := state.Revisions[operation.TargetRevision]
		// Membership changed, so settle the platform onto the remaining
		// topology before verifying it: a removed database node strands its
		// instance (the local-path volume left with the node), which blocks
		// the CNPG descale, and the lowered tier needs a converge nothing
		// else drives. On an unchanged tier both steps are no-ops.
		if target.Platform.Enabled {
			if err := d.pruneOrphanedDatabaseInstances(ctx); err != nil {
				return err
			}
			if err := d.reconcilePlatform(ctx); err != nil &&
				!errors.Is(err, errPlatformInitializationRequired) {
				return err
			}
		}
		if err := d.verifyTarget(ctx, from, target); err != nil {
			return err
		}
		_, err = store.Update(ctx, func(state *clusterstate.State) error {
			return clusterstate.CompleteOperation(state, time.Now())
		})
	}
	return err
}

// moveInvalidManagedPods evicts only Skali-managed pods whose hard
// nodeSelector no longer matches their current node after a capability
// edit. Eviction respects PDBs; valid healthy workloads are left untouched.
func (d *CoordinatorDaemon) moveInvalidManagedPods(ctx context.Context) error {
	client, err := installer.KubeClient(ctx, d.Runner)
	if err != nil {
		return err
	}
	namespaces, err := client.Clientset.CoreV1().Namespaces().List(ctx,
		metav1.ListOptions{LabelSelector: skalikube.ManagedSelector})
	if err != nil {
		return fmt.Errorf("list managed namespaces for required workload moves: %w", err)
	}
	nodeLabels := make(map[string]map[string]string)
	for _, namespace := range namespaces.Items {
		pods, err := client.Clientset.CoreV1().Pods(namespace.Name).List(ctx,
			metav1.ListOptions{LabelSelector: skalikube.ManagedSelector})
		if err != nil {
			return fmt.Errorf("list managed pods in %s: %w", namespace.Name, err)
		}
		for index := range pods.Items {
			pod := &pods.Items[index]
			if pod.Spec.NodeName == "" || pod.DeletionTimestamp != nil ||
				len(pod.Spec.NodeSelector) == 0 {
				continue
			}
			currentLabels, cached := nodeLabels[pod.Spec.NodeName]
			if !cached {
				node, getErr := client.Clientset.CoreV1().Nodes().Get(ctx,
					pod.Spec.NodeName, metav1.GetOptions{})
				if apierrors.IsNotFound(getErr) {
					continue
				}
				if getErr != nil {
					return fmt.Errorf("read node %s for workload placement: %w",
						pod.Spec.NodeName, getErr)
				}
				currentLabels = node.Labels
				nodeLabels[pod.Spec.NodeName] = currentLabels
			}
			valid := true
			for key, value := range pod.Spec.NodeSelector {
				if currentLabels[key] != value {
					valid = false
					break
				}
			}
			if valid {
				continue
			}
			eviction := &policyv1.Eviction{
				ObjectMeta: metav1.ObjectMeta{
					Name: pod.Name, Namespace: pod.Namespace,
				},
			}
			if err := client.Clientset.PolicyV1().Evictions(pod.Namespace).
				Evict(ctx, eviction); err != nil {
				return fmt.Errorf("move invalid workload pod %s/%s: %w",
					pod.Namespace, pod.Name, err)
			}
			if err := waitPodDeleted(ctx, client.Clientset, pod.Namespace,
				pod.Name, pod.UID); err != nil {
				return err
			}
		}
	}
	return nil
}

func waitPodDeleted(ctx context.Context, client kubernetes.Interface,
	namespace, name string, uid types.UID) error {
	ticker := time.NewTicker(2 * time.Second)
	defer ticker.Stop()
	deadline := time.NewTimer(10 * time.Minute)
	defer deadline.Stop()
	for {
		pod, err := client.CoreV1().Pods(namespace).Get(ctx, name, metav1.GetOptions{})
		if apierrors.IsNotFound(err) || (err == nil && pod.UID != uid) {
			return nil
		}
		if err != nil {
			return err
		}
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-deadline.C:
			return fmt.Errorf("invalid workload pod %s/%s did not terminate",
				namespace, name)
		case <-ticker.C:
		}
	}
}

func (d *CoordinatorDaemon) verifyTarget(ctx context.Context, from,
	target clusterstate.Revision) error {
	client, err := installer.KubeClient(ctx, d.Runner)
	if err != nil {
		return err
	}
	for _, desired := range clusterstate.SortedRevisionNodes(target.Nodes) {
		node, err := client.Clientset.CoreV1().Nodes().Get(ctx, desired.Name,
			metav1.GetOptions{})
		if err != nil {
			return fmt.Errorf("verify converged node %s: %w", desired.Name, err)
		}
		ready := false
		for _, condition := range node.Status.Conditions {
			if condition.Type == corev1.NodeReady &&
				condition.Status == corev1.ConditionTrue {
				ready = true
				break
			}
		}
		if !ready {
			return fmt.Errorf("verify converged node %s: node is not Ready", desired.Name)
		}
		for _, capability := range desired.Capabilities {
			if node.Labels[layout.CapabilityLabel(capability)] !=
				layout.CapabilityLabelValue {
				return fmt.Errorf("verify converged node %s: capability %s is missing",
					desired.Name, capability)
			}
		}
		for _, taint := range node.Spec.Taints {
			if taint.Key == layout.PendingTaintKey {
				return fmt.Errorf("verify converged node %s: pending taint remains",
					desired.Name)
			}
		}
	}
	for id, previous := range from.Nodes {
		if _, retained := target.Nodes[id]; retained {
			continue
		}
		_, err := client.Clientset.CoreV1().Nodes().Get(ctx, previous.Name,
			metav1.GetOptions{})
		if err == nil {
			return fmt.Errorf("verify removed node %s: Kubernetes Node still exists",
				previous.Name)
		}
		if !apierrors.IsNotFound(err) {
			return fmt.Errorf("verify removed node %s: %w", previous.Name, err)
		}
	}
	if !target.Platform.Enabled {
		return nil
	}
	if bundle.StampedHash(ctx, client) == "" {
		return errors.New("verify platform: bundle convergence stamp is missing")
	}
	for _, name := range []string{"skali-registry", "skalid", "skali-web"} {
		deployment, err := client.Clientset.AppsV1().Deployments(bundle.Namespace).
			Get(ctx, name, metav1.GetOptions{})
		if err != nil {
			return fmt.Errorf("verify platform deployment %s: %w", name, err)
		}
		desired := int32(1)
		if deployment.Spec.Replicas != nil {
			desired = *deployment.Spec.Replicas
		}
		if deployment.Status.AvailableReplicas < desired ||
			deployment.Status.UnavailableReplicas > 0 {
			return fmt.Errorf("verify platform deployment %s: %d/%d replicas available",
				name, deployment.Status.AvailableReplicas, desired)
		}
	}
	databaseNodes := 0
	for _, node := range target.Nodes {
		if slices.Contains(node.Capabilities, layout.CapabilityDatabase) {
			databaseNodes++
		}
	}
	instances := bundle.TierInstances(layout.DeriveTier(databaseNodes))
	if err := (&bundle.Applier{Client: client}).WaitClusterReady(ctx,
		bundle.Namespace, "skali-db", instances); err != nil {
		return fmt.Errorf("verify database replication: %w", err)
	}
	return nil
}

func (d *CoordinatorDaemon) activateTopology(ctx context.Context,
	target clusterstate.Revision) (map[string]string, error) {
	client, err := installer.KubeClient(ctx, d.Runner)
	if err != nil {
		return nil, err
	}
	coordinators := make(map[string]string)
	// Prove every desired member Ready before removing any pending taint or
	// publishing final capability labels.
	for _, desired := range clusterstate.SortedRevisionNodes(target.Nodes) {
		node, err := client.Clientset.CoreV1().Nodes().Get(ctx, desired.Name,
			metav1.GetOptions{})
		if err != nil {
			return nil, fmt.Errorf("verify target node %s: %w", desired.Name, err)
		}
		ready := false
		for _, condition := range node.Status.Conditions {
			if condition.Type == corev1.NodeReady && condition.Status == corev1.ConditionTrue {
				ready = true
				break
			}
		}
		if !ready {
			return nil, fmt.Errorf("target node %s is not Ready", desired.Name)
		}
		if desired.Role == layout.RoleServer {
			for _, address := range node.Status.Addresses {
				if address.Type == corev1.NodeInternalIP && net.ParseIP(address.Address) != nil {
					coordinators[desired.ID] = "https://" + net.JoinHostPort(
						address.Address, clusterstate.DefaultCoordinatorPort)
					break
				}
			}
		}
	}
	for _, desired := range clusterstate.SortedRevisionNodes(target.Nodes) {
		err := retry.RetryOnConflict(retry.DefaultRetry, func() error {
			node, err := client.Clientset.CoreV1().Nodes().Get(ctx, desired.Name,
				metav1.GetOptions{})
			if err != nil {
				return err
			}
			node = node.DeepCopy()
			if node.Labels == nil {
				node.Labels = make(map[string]string)
			}
			for _, capability := range layout.Capabilities {
				delete(node.Labels, layout.CapabilityLabel(capability))
			}
			for key, value := range layout.CapabilityLabels(desired.Capabilities) {
				node.Labels[key] = value
			}
			taints := node.Spec.Taints[:0]
			for _, taint := range node.Spec.Taints {
				if taint.Key != layout.PendingTaintKey {
					taints = append(taints, taint)
				}
			}
			node.Spec.Taints = taints
			_, err = client.Clientset.CoreV1().Nodes().Update(ctx, node,
				metav1.UpdateOptions{})
			return err
		})
		if err != nil {
			return nil, fmt.Errorf("activate target node %s: %w", desired.Name, err)
		}
	}
	return coordinators, nil
}

func (d *CoordinatorDaemon) snapshotEtcd(ctx context.Context, operationID string) error {
	name := "skali-before-removal-" + operationID
	if len(name) > 63 {
		name = name[:63]
	}
	result, err := d.Runner.Run(ctx, host.Command{
		Name: "k3s", Args: []string{"etcd-snapshot", "save", "--name", name},
	})
	if err != nil {
		return fmt.Errorf("take etcd snapshot before server removal: %w", err)
	}
	if result.ExitCode != 0 {
		return fmt.Errorf("take etcd snapshot before server removal: exit %d: %s",
			result.ExitCode, strings.TrimSpace(result.Stderr))
	}
	return nil
}

func (d *CoordinatorDaemon) reconcilePlatform(ctx context.Context) error {
	record, err := installer.LoadRecord(ctx, d.Runner)
	if err != nil {
		return err
	}
	client, err := installer.KubeClient(ctx, d.Runner)
	if err != nil {
		return err
	}
	if published, publishedErr := installer.InClusterRecord(ctx, client); publishedErr == nil &&
		published != nil && published.Reconciled() {
		record = published
	}
	// Initial platform creation is still driven by cluster init so admin
	// credentials never cross the long-lived agent API.
	if record.Versions.Bundle == "" {
		return errPlatformInitializationRequired
	}
	profile, _, err := installer.LiveProfile(ctx, client, d.Runner, record)
	if err != nil {
		// An empty stamp means a converge is in flight: init clears it with
		// its first namespace apply and restamps only after the last stage,
		// so the live objects LiveProfile reads (the skali-web deployment
		// in particular) may not exist yet. Defer to the driver instead of
		// failing the operation on a half-assembled platform.
		if bundle.StampedHash(ctx, client) == "" {
			return errPlatformInitializationRequired
		}
		return err
	}
	if bundle.StampedHash(ctx, client) == bundle.Hash(profile) {
		return nil
	}
	if err := bundle.Converge(ctx, client, profile, nil); err != nil {
		return err
	}
	return bundle.StampHash(ctx, client, profile)
}

// pruneOrphanedDatabaseInstances removes bootstrap-database instances
// stranded by a node removal: their local-path volume is pinned to the gone
// node, so the recreated pod pends forever, blocking both the CNPG descale
// and the operation verify. Only a pending instance whose bound volume
// names a node that no longer exists is pruned; a scale-up instance still
// waiting for first consumer has no bound volume and is left alone.
func (d *CoordinatorDaemon) pruneOrphanedDatabaseInstances(ctx context.Context) error {
	client, err := installer.KubeClient(ctx, d.Runner)
	if err != nil {
		return err
	}
	pods, err := client.Clientset.CoreV1().Pods(bundle.Namespace).List(ctx,
		metav1.ListOptions{LabelSelector: "cnpg.io/cluster=skali-db"})
	if err != nil {
		return fmt.Errorf("list database instances: %w", err)
	}
	for _, pod := range pods.Items {
		if pod.Spec.NodeName != "" || pod.Status.Phase != corev1.PodPending {
			continue
		}
		claim, err := client.Clientset.CoreV1().PersistentVolumeClaims(bundle.Namespace).
			Get(ctx, pod.Name, metav1.GetOptions{})
		if err != nil || claim.Spec.VolumeName == "" {
			continue
		}
		volume, err := client.Clientset.CoreV1().PersistentVolumes().
			Get(ctx, claim.Spec.VolumeName, metav1.GetOptions{})
		if err != nil {
			continue
		}
		if !volumeNodesGone(ctx, client.Clientset, volume) {
			continue
		}
		d.log("pruning database instance stranded by node removal", "instance", pod.Name)
		if err := client.Clientset.CoreV1().PersistentVolumeClaims(bundle.Namespace).
			DeleteCollection(ctx, metav1.DeleteOptions{}, metav1.ListOptions{
				LabelSelector: "cnpg.io/instanceName=" + pod.Name,
			}); err != nil {
			return fmt.Errorf("prune database instance %s claims: %w", pod.Name, err)
		}
		if err := client.Clientset.CoreV1().Pods(bundle.Namespace).
			Delete(ctx, pod.Name, metav1.DeleteOptions{}); err != nil && !apierrors.IsNotFound(err) {
			return fmt.Errorf("prune database instance %s: %w", pod.Name, err)
		}
	}
	return nil
}

// volumeNodesGone reports whether every node the volume's affinity pins it
// to has left the cluster; a volume without node affinity is never gone.
func volumeNodesGone(ctx context.Context, clientset kubernetes.Interface, volume *corev1.PersistentVolume) bool {
	if volume.Spec.NodeAffinity == nil || volume.Spec.NodeAffinity.Required == nil {
		return false
	}
	pinned := false
	for _, term := range volume.Spec.NodeAffinity.Required.NodeSelectorTerms {
		for _, expression := range term.MatchExpressions {
			if expression.Key != corev1.LabelHostname && expression.Key != "metadata.name" {
				continue
			}
			for _, name := range expression.Values {
				pinned = true
				if _, err := clientset.CoreV1().Nodes().Get(ctx, name, metav1.GetOptions{}); err == nil {
					return false
				} else if !apierrors.IsNotFound(err) {
					return false
				}
			}
		}
	}
	return pinned
}

func (d *CoordinatorDaemon) rebalanceWorkloads(ctx context.Context) error {
	client, err := installer.KubeClient(ctx, d.Runner)
	if err != nil {
		return err
	}
	namespaces, err := client.Clientset.CoreV1().Namespaces().List(ctx,
		metav1.ListOptions{LabelSelector: skalikube.ManagedSelector})
	if err != nil {
		return fmt.Errorf("list managed namespaces for workload rebalance: %w", err)
	}
	type workload struct{ namespace, name string }
	var workloads []workload
	for _, namespace := range namespaces.Items {
		deployments, err := client.Clientset.AppsV1().Deployments(namespace.Name).
			List(ctx, metav1.ListOptions{LabelSelector: skalikube.ManagedSelector})
		if err != nil {
			return fmt.Errorf("list deployments in %s: %w", namespace.Name, err)
		}
		for _, deployment := range deployments.Items {
			workloads = append(workloads, workload{namespace.Name, deployment.Name})
		}
	}
	sort.Slice(workloads, func(i, j int) bool {
		if workloads[i].namespace == workloads[j].namespace {
			return workloads[i].name < workloads[j].name
		}
		return workloads[i].namespace < workloads[j].namespace
	})

	for _, workload := range workloads {
		deployment, err := client.Clientset.AppsV1().Deployments(workload.namespace).
			Get(ctx, workload.name, metav1.GetOptions{})
		if err != nil {
			return err
		}
		if reason := rebalanceBlocker(ctx, client.Clientset, deployment); reason != "" {
			d.log("workload rebalance skipped", "namespace", workload.namespace,
				"deployment", workload.name, "reason", reason)
			continue
		}
		var generation int64
		err = retry.RetryOnConflict(retry.DefaultRetry, func() error {
			current, err := client.Clientset.AppsV1().Deployments(workload.namespace).
				Get(ctx, workload.name, metav1.GetOptions{})
			if err != nil {
				return err
			}
			current = current.DeepCopy()
			if current.Spec.Template.Annotations == nil {
				current.Spec.Template.Annotations = make(map[string]string)
			}
			current.Spec.Template.Annotations["skali.dev/rebalanced-at"] =
				time.Now().UTC().Format(time.RFC3339Nano)
			updated, err := client.Clientset.AppsV1().Deployments(workload.namespace).
				Update(ctx, current, metav1.UpdateOptions{})
			if err == nil {
				generation = updated.Generation
			}
			return err
		})
		if err != nil {
			return fmt.Errorf("start rebalance of %s/%s: %w",
				workload.namespace, workload.name, err)
		}
		if err := waitDeploymentRollout(ctx, client.Clientset, workload.namespace,
			workload.name, generation); err != nil {
			return err
		}
	}
	return nil
}

func rebalanceBlocker(ctx context.Context, client kubernetes.Interface,
	deployment *appsv1.Deployment) string {
	desired := int32(1)
	if deployment.Spec.Replicas != nil {
		desired = *deployment.Spec.Replicas
	}
	if desired == 0 {
		return "scaled to zero"
	}
	if deployment.Spec.Strategy.Type != "" &&
		deployment.Spec.Strategy.Type != appsv1.RollingUpdateDeploymentStrategyType {
		return "deployment does not use RollingUpdate"
	}
	if deployment.Status.AvailableReplicas < desired ||
		deployment.Status.UnavailableReplicas > 0 {
		return "deployment is not fully available"
	}
	for _, volume := range deployment.Spec.Template.Spec.Volumes {
		if volume.PersistentVolumeClaim != nil {
			return "uses a persistent volume that cannot be moved without disruption"
		}
	}
	pdbs, err := client.PolicyV1().PodDisruptionBudgets(deployment.Namespace).
		List(ctx, metav1.ListOptions{})
	if err != nil {
		return "pod disruption budgets could not be read"
	}
	podLabels := labels.Set(deployment.Spec.Template.Labels)
	for _, pdb := range pdbs.Items {
		if pdb.Spec.Selector == nil {
			continue
		}
		selector, err := metav1.LabelSelectorAsSelector(pdb.Spec.Selector)
		if err == nil && selector.Matches(podLabels) && pdb.Status.DisruptionsAllowed < 1 {
			return "a matching PodDisruptionBudget allows no disruption"
		}
	}
	return ""
}

func waitDeploymentRollout(ctx context.Context, client kubernetes.Interface,
	namespace, name string, generation int64) error {
	ticker := time.NewTicker(2 * time.Second)
	defer ticker.Stop()
	deadline := time.NewTimer(10 * time.Minute)
	defer deadline.Stop()
	for {
		deployment, err := client.AppsV1().Deployments(namespace).
			Get(ctx, name, metav1.GetOptions{})
		if err != nil {
			return err
		}
		desired := int32(1)
		if deployment.Spec.Replicas != nil {
			desired = *deployment.Spec.Replicas
		}
		if deployment.Status.ObservedGeneration >= generation &&
			deployment.Status.UpdatedReplicas >= desired &&
			deployment.Status.AvailableReplicas >= desired &&
			deployment.Status.UnavailableReplicas == 0 {
			return nil
		}
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-deadline.C:
			return fmt.Errorf("deployment %s/%s did not become ready after rebalance",
				namespace, name)
		case <-ticker.C:
		}
	}
}

func k3sJoinEndpoint(state *clusterstate.State) (string, error) {
	var endpoints []string
	for _, node := range state.Nodes {
		if node.Role == "server" && node.Phase == clusterstate.NodePhaseActive &&
			node.Coordinator != "" {
			endpoints = append(endpoints, node.Coordinator)
		}
	}
	slices.Sort(endpoints)
	if len(endpoints) == 0 {
		return "", errors.New("no active server coordinator endpoint is available")
	}
	parsed, err := url.Parse(endpoints[0])
	if err != nil {
		return "", err
	}
	parsed.Host = net.JoinHostPort(parsed.Hostname(), "6443")
	parsed.Path = ""
	return parsed.String(), nil
}

func nodeActionsComplete(operation clusterstate.Operation, actions ...string) bool {
	for _, step := range operation.NodeSteps {
		if slices.Contains(actions, step.Action) && step.Phase != clusterstate.StepComplete {
			return false
		}
	}
	return true
}

func (d *CoordinatorDaemon) log(message string, args ...any) {
	if d.Logger != nil {
		d.Logger.Warn(message, args...)
	}
}

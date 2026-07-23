package installer

import (
	"context"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
	corev1 "k8s.io/api/core/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	k8sfake "k8s.io/client-go/kubernetes/fake"
	k8stesting "k8s.io/client-go/testing"

	"github.com/Hinkolas/skali/internal/bundle"
	"github.com/Hinkolas/skali/internal/kube"
	"github.com/Hinkolas/skali/internal/layout"
)

// recordingProgress captures stage titles and transient notes so tests can
// assert what a stage surfaced. It implements the optional progressNoter.
type recordingProgress struct {
	starts []string
	notes  []string
}

func (p *recordingProgress) Start(title string) { p.starts = append(p.starts, title) }
func (p *recordingProgress) Done(string)        {}
func (p *recordingProgress) Skip(string)        {}
func (p *recordingProgress) Note(line string)   { p.notes = append(p.notes, line) }

// shrinkNamespaceTiming collapses the termination pacing so the force path
// runs in milliseconds, restoring the defaults when the test ends.
func shrinkNamespaceTiming(t *testing.T) {
	t.Helper()
	grace, force, poll := namespaceTerminationGrace, namespaceForceDeadline, namespacePollInterval
	namespaceTerminationGrace = 5 * time.Millisecond
	namespaceForceDeadline = 500 * time.Millisecond
	namespacePollInterval = time.Millisecond
	t.Cleanup(func() {
		namespaceTerminationGrace, namespaceForceDeadline, namespacePollInterval = grace, force, poll
	})
}

func TestNamespaceBlocker(t *testing.T) {
	t.Parallel()

	// Only conditions reporting a problem (status True) contribute.
	ns := &corev1.Namespace{Status: corev1.NamespaceStatus{Conditions: []corev1.NamespaceCondition{
		{Type: corev1.NamespaceDeletionContentFailure, Status: corev1.ConditionFalse, Message: "settled"},
		{Type: corev1.NamespaceFinalizersRemaining, Status: corev1.ConditionTrue,
			Message: "Some content in the namespace has finalizers remaining: finalizer.acme.cert-manager.io in 3 resource instances"},
	}}}
	require.Equal(t, "Some content in the namespace has finalizers remaining: finalizer.acme.cert-manager.io in 3 resource instances",
		namespaceBlocker(ns))

	// A namespace the controller has not annotated yet reports nothing.
	require.Equal(t, "", namespaceBlocker(&corev1.Namespace{}))

	// A messageless condition falls back to its reason.
	byReason := &corev1.Namespace{Status: corev1.NamespaceStatus{Conditions: []corev1.NamespaceCondition{
		{Type: corev1.NamespaceContentRemaining, Status: corev1.ConditionTrue, Reason: "SomeResourcesRemain"},
	}}}
	require.Equal(t, "SomeResourcesRemain", namespaceBlocker(byReason))
}

func TestDeleteNamespacesCleanExit(t *testing.T) {
	t.Parallel()
	ctx := context.Background()

	clientset := k8sfake.NewSimpleClientset(
		&corev1.Namespace{ObjectMeta: metav1.ObjectMeta{Name: "skali-hello-world-production"}},
	)
	client := &kube.Client{Clientset: clientset}
	// One present namespace is counted; an absent one is skipped, and a
	// clean termination never reaches the force path.
	deleted, err := deleteNamespaces(ctx, client, []string{"skali-hello-world-production", "absent"}, nil)
	require.NoError(t, err)
	require.Equal(t, 1, deleted)
}

func TestDeleteNamespacesForcesStuck(t *testing.T) {
	shrinkNamespaceTiming(t)
	ctx := context.Background()

	stuck := &corev1.Namespace{
		ObjectMeta: metav1.ObjectMeta{Name: "skali-maxbau-production"},
		Spec:       corev1.NamespaceSpec{Finalizers: []corev1.FinalizerName{"kubernetes"}},
		Status: corev1.NamespaceStatus{Conditions: []corev1.NamespaceCondition{{
			Type:   corev1.NamespaceFinalizersRemaining,
			Status: corev1.ConditionTrue,
			Message: "Some content in the namespace has finalizers remaining: " +
				"finalizer.acme.cert-manager.io in 3 resource instances",
		}}},
	}
	clientset := k8sfake.NewSimpleClientset(stuck)

	forced := false
	// Delete leaves the namespace terminating: its finalizer holds it.
	clientset.PrependReactor("delete", "namespaces", func(k8stesting.Action) (bool, runtime.Object, error) {
		return true, nil, nil
	})
	// Get returns the terminating namespace until it is forced, then gone.
	clientset.PrependReactor("get", "namespaces", func(k8stesting.Action) (bool, runtime.Object, error) {
		if forced {
			return true, nil, apierrors.NewNotFound(corev1.Resource("namespaces"), stuck.Name)
		}
		return true, stuck.DeepCopy(), nil
	})
	// Finalize must arrive with the finalizers cleared; it retires the ns.
	clientset.PrependReactor("create", "namespaces", func(action k8stesting.Action) (bool, runtime.Object, error) {
		if action.GetSubresource() != "finalize" {
			return false, nil, nil
		}
		ns := action.(k8stesting.CreateAction).GetObject().(*corev1.Namespace)
		require.Empty(t, ns.Spec.Finalizers)
		forced = true
		return true, ns, nil
	})

	client := &kube.Client{Clientset: clientset}
	progress := &recordingProgress{}
	deleted, err := deleteNamespaces(ctx, client, []string{stuck.Name}, progress)
	require.NoError(t, err)
	require.Equal(t, 1, deleted)
	require.True(t, forced, "the stuck namespace should have been forced")

	notes := strings.Join(progress.notes, "\n")
	require.Contains(t, notes, "finalizer.acme.cert-manager.io", "the blocker should be surfaced while waiting")
	require.Contains(t, notes, "forcing termination of "+stuck.Name)
}

func clusterNode(name string, server bool) *corev1.Node {
	labels := map[string]string{}
	if server {
		labels[layout.ControlPlaneLabel] = "true"
	}
	return &corev1.Node{ObjectMeta: metav1.ObjectMeta{Name: name, Labels: labels}}
}

func systemPod(name, nodeName string, pvc bool) *corev1.Pod {
	pod := &corev1.Pod{
		ObjectMeta: metav1.ObjectMeta{Name: name, Namespace: bundle.Namespace},
		Spec:       corev1.PodSpec{NodeName: nodeName},
	}
	if pvc {
		pod.Spec.Volumes = []corev1.Volume{{
			Name: "data",
			VolumeSource: corev1.VolumeSource{
				PersistentVolumeClaim: &corev1.PersistentVolumeClaimVolumeSource{ClaimName: name},
			},
		}}
	}
	return pod
}

func TestPlanNodeRemovalGuards(t *testing.T) {
	t.Parallel()
	ctx := context.Background()

	// A healthy pair: removing one server is allowed and counted.
	client := &kube.Client{Clientset: k8sfake.NewSimpleClientset(
		clusterNode("cp-1", true), clusterNode("cp-2", true), clusterNode("db-1", false),
	)}
	plan, err := planNodeRemovalWith(ctx, client,
		&NodeRemovalPlan{NodeName: "cp-2", Role: layout.RoleServer, Total: 1})
	require.NoError(t, err)
	require.Equal(t, 3, plan.Total)
	require.Equal(t, 2, plan.Servers)
	require.Equal(t, 1, plan.Agents)

	// The last server never leaves while agents remain.
	client = &kube.Client{Clientset: k8sfake.NewSimpleClientset(
		clusterNode("cp-1", true), clusterNode("db-1", false),
	)}
	_, err = planNodeRemovalWith(ctx, client,
		&NodeRemovalPlan{NodeName: "cp-1", Role: layout.RoleServer, Total: 1})
	require.ErrorContains(t, err, "this is the only server")

	// skali-system data on the leaving node blocks until relocated; pods
	// without claims and pods on other nodes do not.
	client = &kube.Client{Clientset: k8sfake.NewSimpleClientset(
		clusterNode("cp-1", true), clusterNode("cp-2", true),
		systemPod("skali-db-1", "cp-2", true),
		systemPod("skalid-abc", "cp-2", false),
		systemPod("skali-db-2", "cp-1", true),
	)}
	_, err = planNodeRemovalWith(ctx, client,
		&NodeRemovalPlan{NodeName: "cp-2", Role: layout.RoleServer, Total: 1})
	require.ErrorContains(t, err, "skali-system data lives on this node (skali-db-1)")

	// A single node passes with no guard: destroying the cluster is the
	// separately confirmed path.
	client = &kube.Client{Clientset: k8sfake.NewSimpleClientset(clusterNode("cp-1", true))}
	plan, err = planNodeRemovalWith(ctx, client,
		&NodeRemovalPlan{NodeName: "cp-1", Role: layout.RoleServer, Total: 1})
	require.NoError(t, err)
	require.Equal(t, 1, plan.Total)
}

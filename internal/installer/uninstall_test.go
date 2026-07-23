package installer

import (
	"context"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
	appsv1 "k8s.io/api/apps/v1"
	corev1 "k8s.io/api/core/v1"
	rbacv1 "k8s.io/api/rbac/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/types"
	k8sfake "k8s.io/client-go/kubernetes/fake"
	k8stesting "k8s.io/client-go/testing"

	"github.com/Hinkolas/skali/internal/bundle"
	"github.com/Hinkolas/skali/internal/installer/host"
	"github.com/Hinkolas/skali/internal/kube"
	"github.com/Hinkolas/skali/internal/kubernetes"
	"github.com/Hinkolas/skali/internal/layout"
)

type recordingProgress struct {
	starts []string
	notes  []string
}

func (p *recordingProgress) Start(title string) { p.starts = append(p.starts, title) }
func (p *recordingProgress) Done(string)        {}
func (p *recordingProgress) Skip(string)        {}
func (p *recordingProgress) Note(line string)   { p.notes = append(p.notes, line) }

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

func TestStopBundleReconcilerRevokesAccessBeforeScaling(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	replicas := int32(1)
	clientset := k8sfake.NewSimpleClientset(
		&rbacv1.ClusterRole{ObjectMeta: metav1.ObjectMeta{Name: skalidRBACName}},
		&rbacv1.ClusterRoleBinding{ObjectMeta: metav1.ObjectMeta{Name: skalidRBACName}},
		&appsv1.Deployment{
			ObjectMeta: metav1.ObjectMeta{Name: skalidDeployment, Namespace: bundle.Namespace},
			Spec:       appsv1.DeploymentSpec{Replicas: &replicas},
		},
	)
	bindingRevoked := false
	clientset.PrependReactor("delete", "clusterrolebindings", func(k8stesting.Action) (bool, runtime.Object, error) {
		bindingRevoked = true
		return false, nil, nil
	})
	clientset.PrependReactor("update", "deployments", func(action k8stesting.Action) (bool, runtime.Object, error) {
		require.True(t, bindingRevoked, "skalid authorization must be revoked before it is scaled down")
		deployment := action.(k8stesting.UpdateAction).GetObject().(*appsv1.Deployment)
		require.NotNil(t, deployment.Spec.Replicas)
		require.Zero(t, *deployment.Spec.Replicas)
		return false, nil, nil
	})

	progress := &recordingProgress{}
	client := &kube.Client{Clientset: clientset}
	require.NoError(t, stopBundleReconciler(ctx, client, progress))
	require.Equal(t, []string{reconcilerStopTitle}, progress.starts)

	_, err := clientset.RbacV1().ClusterRoleBindings().Get(ctx, skalidRBACName, metav1.GetOptions{})
	require.True(t, apierrors.IsNotFound(err))
	_, err = clientset.RbacV1().ClusterRoles().Get(ctx, skalidRBACName, metav1.GetOptions{})
	require.True(t, apierrors.IsNotFound(err))
	deployment, err := clientset.AppsV1().Deployments(bundle.Namespace).
		Get(ctx, skalidDeployment, metav1.GetOptions{})
	require.NoError(t, err)
	require.NotNil(t, deployment.Spec.Replicas)
	require.Zero(t, *deployment.Spec.Replicas)
}

func TestUninstallBundleStopsReconcilerBeforeDeletingProjects(t *testing.T) {
	ctx := context.Background()
	replicas := int32(1)
	projectNamespace := &corev1.Namespace{ObjectMeta: metav1.ObjectMeta{
		Name: "skali-demo-production",
		UID:  types.UID("project"),
		Labels: map[string]string{
			kubernetes.LabelManaged: "true",
		},
	}}
	clientset := k8sfake.NewSimpleClientset(
		projectNamespace,
		&corev1.Namespace{ObjectMeta: metav1.ObjectMeta{
			Name: bundle.Namespace, UID: types.UID("system"),
		}},
		&rbacv1.ClusterRole{ObjectMeta: metav1.ObjectMeta{Name: skalidRBACName}},
		&rbacv1.ClusterRoleBinding{ObjectMeta: metav1.ObjectMeta{Name: skalidRBACName}},
		&appsv1.Deployment{
			ObjectMeta: metav1.ObjectMeta{Name: skalidDeployment, Namespace: bundle.Namespace},
			Spec:       appsv1.DeploymentSpec{Replicas: &replicas},
		},
	)
	client := &kube.Client{Clientset: clientset}
	runner := &host.Fake{}
	record := &Record{Versions: Versions{Bundle: "installed"}}

	require.NoError(t, UninstallBundle(ctx, runner, client, record, &recordingProgress{}))
	require.Empty(t, record.Versions.Bundle)

	actions := clientset.Actions()
	revoke := actionIndex(actions, func(action k8stesting.Action) bool {
		return action.GetVerb() == "delete" && action.GetResource().Resource == "clusterrolebindings"
	})
	scale := actionIndex(actions, func(action k8stesting.Action) bool {
		return action.GetVerb() == "update" && action.GetResource().Resource == "deployments"
	})
	deleteProject := actionIndex(actions, func(action k8stesting.Action) bool {
		if action.GetVerb() != "delete" || action.GetResource().Resource != "namespaces" {
			return false
		}
		return action.(k8stesting.DeleteAction).GetName() == projectNamespace.Name
	})
	require.Less(t, revoke, deleteProject)
	require.Less(t, scale, deleteProject)
}

func actionIndex(actions []k8stesting.Action, match func(k8stesting.Action) bool) int {
	for index, action := range actions {
		if match(action) {
			return index
		}
	}
	return len(actions)
}

func TestDeleteNamespacesCleanExit(t *testing.T) {
	ctx := context.Background()
	namespace := &corev1.Namespace{ObjectMeta: metav1.ObjectMeta{
		Name: "skali-demo-production", UID: types.UID("original"),
	}}
	client := &kube.Client{Clientset: k8sfake.NewSimpleClientset(namespace)}

	deleted, err := deleteNamespaces(ctx, client,
		[]string{namespace.Name, "already-absent"}, nil)
	require.NoError(t, err)
	require.Equal(t, 1, deleted)
}

func TestDeleteNamespacesReportsRecreationByUID(t *testing.T) {
	ctx := context.Background()
	name := "skali-demo-production"
	original := &corev1.Namespace{ObjectMeta: metav1.ObjectMeta{
		Name: name, UID: types.UID("original"),
	}}
	replacement := &corev1.Namespace{ObjectMeta: metav1.ObjectMeta{
		Name: name, UID: types.UID("replacement"),
	}}
	clientset := k8sfake.NewSimpleClientset(original)
	deleted := false
	clientset.PrependReactor("delete", "namespaces", func(action k8stesting.Action) (bool, runtime.Object, error) {
		options := action.(k8stesting.DeleteAction).GetDeleteOptions()
		require.NotNil(t, options.Preconditions)
		require.Equal(t, original.UID, *options.Preconditions.UID)
		deleted = true
		return true, nil, nil
	})
	clientset.PrependReactor("get", "namespaces", func(k8stesting.Action) (bool, runtime.Object, error) {
		if deleted {
			return true, replacement.DeepCopy(), nil
		}
		return true, original.DeepCopy(), nil
	})

	client := &kube.Client{Clientset: clientset}
	count, err := deleteNamespaces(ctx, client, []string{name}, nil)
	require.Equal(t, 1, count)
	require.ErrorContains(t, err, "was recreated during uninstall")
	require.ErrorContains(t, err, "original")
	require.ErrorContains(t, err, "replacement")
}

func TestDeleteNamespacesForcesStuckFinalizer(t *testing.T) {
	shrinkNamespaceTiming(t)
	ctx := context.Background()
	stuck := &corev1.Namespace{
		ObjectMeta: metav1.ObjectMeta{
			Name: "skali-demo-production", UID: types.UID("stuck"),
		},
		Spec: corev1.NamespaceSpec{
			Finalizers: []corev1.FinalizerName{"kubernetes"},
		},
		Status: corev1.NamespaceStatus{Conditions: []corev1.NamespaceCondition{{
			Type:    corev1.NamespaceFinalizersRemaining,
			Status:  corev1.ConditionTrue,
			Message: "finalizer.acme.cert-manager.io remains",
		}}},
	}
	clientset := k8sfake.NewSimpleClientset(stuck)
	forced := false
	clientset.PrependReactor("delete", "namespaces", func(k8stesting.Action) (bool, runtime.Object, error) {
		return true, nil, nil // leave the namespace terminating
	})
	clientset.PrependReactor("get", "namespaces", func(k8stesting.Action) (bool, runtime.Object, error) {
		if forced {
			return true, nil, apierrors.NewNotFound(corev1.Resource("namespaces"), stuck.Name)
		}
		return true, stuck.DeepCopy(), nil
	})
	clientset.PrependReactor("create", "namespaces", func(action k8stesting.Action) (bool, runtime.Object, error) {
		if action.GetSubresource() != "finalize" {
			return false, nil, nil
		}
		namespace := action.(k8stesting.CreateAction).GetObject().(*corev1.Namespace)
		require.Empty(t, namespace.Spec.Finalizers)
		require.Equal(t, stuck.UID, namespace.UID)
		forced = true
		return true, namespace, nil
	})

	progress := &recordingProgress{}
	client := &kube.Client{Clientset: clientset}
	deleted, err := deleteNamespaces(ctx, client, []string{stuck.Name}, progress)
	require.NoError(t, err)
	require.Equal(t, 1, deleted)
	require.True(t, forced)
	notes := strings.Join(progress.notes, "\n")
	require.Contains(t, notes, "finalizer.acme.cert-manager.io")
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

package reconcile

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	"k8s.io/apimachinery/pkg/api/meta"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/runtime/schema"
	"k8s.io/client-go/dynamic/fake"
	kt "k8s.io/client-go/testing"

	"github.com/Hinkolas/skali/internal/kube"
	rendering "github.com/Hinkolas/skali/internal/kubernetes"
)

type namespaceClient struct {
	*fakeCluster
	client *kube.Client
}

func (c namespaceClient) Apply(ctx context.Context, obj runtime.Object, force bool) (kube.ApplyResult, error) {
	if obj.GetObjectKind().GroupVersionKind().Kind == "Namespace" {
		return c.client.Apply(ctx, obj, force)
	}
	return c.fakeCluster.Apply(ctx, obj, force)
}

func TestReconcileRecoveredApplyConflictDoesNotFailRun(t *testing.T) {
	f := newKernelFixture(t, Config{RolloutDeadline: time.Hour})
	ctx := context.Background()
	f.executeDeployment(t)
	f.fake.SetFresh()
	_, err := f.kernel.reconcileEnvironment(ctx, f.environmentID)
	require.NoError(t, err)
	f.markHealthy(t)
	_, err = f.kernel.reconcileEnvironment(ctx, f.environmentID)
	require.NoError(t, err)
	before, err := f.journal.ListRuns(ctx, f.environmentID)
	require.NoError(t, err)

	typed := rendering.RenderNamespace("demo", "production", f.environmentID.String())
	typed.UID = "original"
	typed.ResourceVersion = "1"
	typed.Annotations = map[string]string{"skali.dev/resource-identity": "/Namespace/" + typed.Name}
	content, err := runtime.DefaultUnstructuredConverter.ToUnstructured(typed)
	require.NoError(t, err)
	live := &unstructured.Unstructured{Object: content}
	dynamic := fake.NewSimpleDynamicClient(runtime.NewScheme(), live)
	mapper := meta.NewDefaultRESTMapper([]schema.GroupVersion{{Version: "v1"}})
	mapper.Add(schema.GroupVersionKind{Version: "v1", Kind: "Namespace"}, meta.RESTScopeRoot)
	c := &kube.Client{Dynamic: dynamic, Mapper: mapper}
	calls := 0
	dynamic.PrependReactor("patch", "namespaces", func(kt.Action) (bool, runtime.Object, error) {
		calls++
		latest := live.DeepCopy()
		latest.SetResourceVersion("2")
		if calls == 1 {
			require.NoError(t, dynamic.Tracker().Update(schema.GroupVersionResource{Version: "v1", Resource: "namespaces"}, latest, ""))
			return true, nil, apierrors.NewConflict(schema.GroupResource{Resource: "namespaces"}, live.GetName(), errors.New("object modified"))
		}
		return true, latest, nil
	})
	f.kernel.deps.Cluster = namespaceClient{fakeCluster: f.cluster, client: c}
	_, err = f.kernel.reconcileEnvironment(ctx, f.environmentID)
	require.NoError(t, err)
	require.Equal(t, 2, calls)
	after, err := f.journal.ListRuns(ctx, f.environmentID)
	require.NoError(t, err)
	require.Len(t, after, len(before), "a recovered no-op must not create a run")
	for _, run := range after {
		require.NotEqual(t, "failed", run.Status)
	}

	// Exhaustion is one failed operation/run, not five separate failures.
	dynamic.PrependReactor("patch", "namespaces", func(kt.Action) (bool, runtime.Object, error) {
		return true, nil, apierrors.NewConflict(schema.GroupResource{Resource: "namespaces"}, live.GetName(), errors.New("object modified"))
	})
	_, err = f.kernel.reconcileEnvironment(ctx, f.environmentID)
	require.True(t, apierrors.IsConflict(err))
	after, err = f.journal.ListRuns(ctx, f.environmentID)
	require.NoError(t, err)
	require.Len(t, after, len(before)+1)
	require.Equal(t, "failed", after[0].Status)
}

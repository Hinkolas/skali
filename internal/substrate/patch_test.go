package substrate

import (
	"context"
	"testing"

	"github.com/google/uuid"
	"github.com/stretchr/testify/require"
	corev1 "k8s.io/api/core/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/runtime/schema"

	"github.com/Hinkolas/skali/internal/dbstore"
	"github.com/Hinkolas/skali/internal/observe"
	"github.com/Hinkolas/skali/internal/store"
	"github.com/Hinkolas/skali/internal/substrate/cnpg"
	"github.com/Hinkolas/skali/internal/testdb"
)

// patchCluster answers the pool reads a patch update makes per pool: each
// pool's CNPG phase and the image its one member runs. A pool without an
// entry reads as not created.
type patchCluster struct {
	*fakeCluster
	pools map[string]patchPoolState
}

type patchPoolState struct {
	phase string
	image string
	ready bool
}

func (f *patchCluster) GetObject(_ context.Context, gvr schema.GroupVersionResource, _, name string) (*unstructured.Unstructured, error) {
	state, ok := f.pools[name]
	if gvr != cnpg.ClusterGVR || !ok {
		return nil, apierrors.NewNotFound(schema.GroupResource{Group: gvr.Group, Resource: gvr.Resource}, name)
	}
	return &unstructured.Unstructured{Object: map[string]any{
		"status": map[string]any{"phase": state.phase},
	}}, nil
}

func (f *patchCluster) ListPods(_ context.Context, _, selector string) ([]corev1.Pod, error) {
	for name, state := range f.pools {
		if selector != cnpg.InstanceSelector(name) {
			continue
		}
		ready := corev1.ConditionFalse
		if state.ready {
			ready = corev1.ConditionTrue
		}
		return []corev1.Pod{{
			ObjectMeta: metav1.ObjectMeta{Name: name + "-1"},
			Spec:       corev1.PodSpec{Containers: []corev1.Container{{Name: cnpg.PostgresContainer, Image: state.image}}},
			Status:     corev1.PodStatus{Conditions: []corev1.PodCondition{{Type: corev1.PodReady, Status: ready}}},
		}}, nil
	}
	return nil, nil
}

func (f *patchCluster) settle(pool, image string) {
	f.pools[pool] = patchPoolState{phase: clusterHealthyPhase, image: image, ready: true}
}

// Pools on a reviewed patch source move to the current image one at a
// time: the second waits until every member of the first runs the new
// image and the pool is healthy again, so an update that does not settle
// stops at one pool. A pool on an image with no reviewed transition keeps
// it.
func TestPatchPoolMovesOnePoolAtATime(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	db := dbstore.New(store.NewStore(testdb.New(t)))
	current, ok := cnpg.Lookup("postgres", 17)
	require.True(t, ok)
	require.NotEmpty(t, current.PatchesFrom, "the postgres 17 catalog entry lists a patch source")
	old := current.PatchesFrom[0]
	// An older build on another OS base: never a reviewed patch source.
	unreviewed := "ghcr.io/cloudnative-pg/postgresql:17.5-system-bookworm"

	create := func(name string, major int, image string) store.DatabaseCluster {
		t.Helper()
		row, err := db.CreateCluster(ctx, dbstore.ClusterInput{
			Name: name, Engine: "postgres", Major: major, Class: dbstore.ClassDedicated,
			ClaimID: uuid.New(), Instances: 1, StorageBytes: 1 << 30, Image: image,
		})
		require.NoError(t, err)
		return *row
	}
	first := create("pg17-first", 17, old)
	second := create("pg17-second", 17, old)
	held := create("pg17-held", 17, unreviewed)

	cluster := &patchCluster{fakeCluster: &fakeCluster{}, pools: map[string]patchPoolState{}}
	for _, pool := range []store.DatabaseCluster{first, second, held} {
		cluster.settle(pool.Name, pool.Image)
	}
	controller := New(Deps{DB: db, Cluster: cluster, Observed: observe.NewStore(nil)}, Config{})
	image := func(pool store.DatabaseCluster) string {
		t.Helper()
		row, err := db.GetCluster(ctx, pool.ID)
		require.NoError(t, err)
		return row.Image
	}

	patched, wait, err := controller.patchPool(ctx, first)
	require.NoError(t, err)
	require.Zero(t, wait)
	require.Equal(t, current.Ref, patched.Image, "the pass applies the moved row")
	require.Equal(t, current.Ref, image(first))

	// The first pool's member still runs the old image: the second waits.
	patched, wait, err = controller.patchPool(ctx, second)
	require.NoError(t, err)
	require.Equal(t, patchWait, wait)
	require.Equal(t, old, patched.Image)
	require.Equal(t, old, image(second))

	// Rolled but not healthy again: still waiting.
	cluster.pools[first.Name] = patchPoolState{phase: "Upgrading cluster", image: current.Ref, ready: true}
	_, wait, err = controller.patchPool(ctx, second)
	require.NoError(t, err)
	require.Equal(t, patchWait, wait)
	require.Equal(t, old, image(second))

	cluster.settle(first.Name, current.Ref)
	patched, wait, err = controller.patchPool(ctx, second)
	require.NoError(t, err)
	require.Zero(t, wait)
	require.Equal(t, current.Ref, patched.Image)
	require.Equal(t, current.Ref, image(second))

	// A stale row (another pass moved the pool first) is a no-op.
	cluster.settle(second.Name, current.Ref)
	// A pool being released does not hold the rollout back.
	releasing := create("pg17-releasing", 17, current.Ref)
	_, err = db.TransitionCluster(ctx, releasing.ID, dbstore.StateReleasing)
	require.NoError(t, err)
	_, wait, err = controller.patchPool(ctx, first)
	require.NoError(t, err)
	require.Equal(t, requeueWait, wait)

	patched, wait, err = controller.patchPool(ctx, held)
	require.NoError(t, err)
	require.Zero(t, wait)
	require.Equal(t, unreviewed, patched.Image)
	require.Equal(t, unreviewed, image(held), "a pool without a reviewed transition keeps its image")
}

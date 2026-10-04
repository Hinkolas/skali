package substrate

import (
	"context"
	"errors"
	"fmt"
	"testing"

	"github.com/stretchr/testify/require"
	"k8s.io/apimachinery/pkg/api/meta"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/runtime/schema"

	"github.com/Hinkolas/skali/internal/edge"
	"github.com/Hinkolas/skali/internal/kube"
	"github.com/Hinkolas/skali/internal/store"
	"github.com/Hinkolas/skali/internal/substrate/seaweed"
)

// edgeCluster records what a pass applies and deletes; unknown kinds on
// delete answer like a cluster without the CRD.
type edgeCluster struct {
	fakeCluster
	applied []string
	deleted []string
	unknown map[schema.GroupVersionKind]bool
}

func (e *edgeCluster) ApplyAs(_ context.Context, obj runtime.Object, _ string, _ bool) (kube.ApplyResult, error) {
	accessor, err := meta.Accessor(obj)
	if err != nil {
		return kube.ApplyResult{}, err
	}
	e.applied = append(e.applied, obj.GetObjectKind().GroupVersionKind().Kind+"/"+accessor.GetName())
	return kube.ApplyResult{Changed: true}, nil
}

func (e *edgeCluster) Delete(_ context.Context, ref kube.ObjectRef) (bool, error) {
	if e.unknown[ref.GVK] {
		return false, fmt.Errorf("kube: map %s: %w", ref.GVK.Kind,
			&meta.NoKindMatchError{GroupKind: ref.GVK.GroupKind(), SearchedVersions: []string{ref.GVK.Version}})
	}
	e.deleted = append(e.deleted, ref.GVK.Kind+"/"+ref.Name)
	return true, nil
}

// TestSweepLegacyS3EdgeRemoves: the first pass of a managed installation
// deletes exactly the objects the removed installation-wide endpoint
// owned, nothing of the store itself, and the published endpoint of a
// bucket without a route is the in-cluster gateway.
func TestSweepLegacyS3EdgeRemoves(t *testing.T) {
	t.Parallel()
	cluster := &edgeCluster{}
	c := &Controller{cfg: Config{Managed: true}, deps: Deps{Cluster: cluster}}
	require.NoError(t, sweepLegacyEdge(c))
	require.Empty(t, cluster.applied)
	require.ElementsMatch(t, []string{
		"IngressRoute/seaweed-s3",
		"IngressRoute/seaweed-s3-http",
		"Middleware/" + edge.RedirectMiddlewareName,
		"Certificate/seaweed-s3-tls",
		"Ingress/seaweed-s3",
	}, cluster.deleted)
	require.Equal(t, InternalBucketEndpoint(), mustEndpoint(t, c, store.BucketClaim{}))
}

// sweepLegacyEdge runs one sweep and expects it not to wait: the fake
// clusters serve no legacy route, so nothing gates the deletes.
func sweepLegacyEdge(c *Controller) error {
	wait, err := c.sweepLegacyS3Edge(context.Background())
	if wait != 0 {
		return fmt.Errorf("unexpected wait %s", wait)
	}
	return err
}

// servingEdgeCluster still carries the retired edge's TLS route.
type servingEdgeCluster struct{ edgeCluster }

func (s *servingEdgeCluster) GetObject(_ context.Context, gvr schema.GroupVersionResource, namespace, name string) (*unstructured.Unstructured, error) {
	if gvr == edge.IngressRouteGVR && namespace == Namespace && name == "seaweed-s3" {
		return &unstructured.Unstructured{Object: map[string]any{}}, nil
	}
	return s.edgeCluster.GetObject(context.Background(), gvr, namespace, name)
}

// TestSweepLegacyS3EdgeWaitsForConsumers: while the retired edge still
// serves, it outlives every bucket whose output mirror is unconfirmed and
// then the settle window after the last republication, so consumers that
// read the old endpoint roll onto the new one before it stops answering.
func TestSweepLegacyS3EdgeWaitsForConsumers(t *testing.T) {
	t.Parallel()
	fx := newRotationFixture(t)
	ctx := context.Background()
	cluster := &servingEdgeCluster{}
	c := &Controller{cfg: Config{Managed: true}, deps: Deps{Cluster: cluster, DB: fx.db}}

	wait, err := c.sweepLegacyS3Edge(ctx)
	require.NoError(t, err)
	require.Equal(t, legacyEdgeRecheck, wait, "an unconfirmed mirror keeps the edge")
	require.Empty(t, cluster.deleted)

	_, err = fx.db.PublishAllocationOutputs(ctx, fx.allocation.ID, InternalBucketEndpoint(), true)
	require.NoError(t, err)
	wait, err = c.sweepLegacyS3Edge(ctx)
	require.NoError(t, err)
	require.InDelta(t, legacyEdgeSettle.Seconds(), wait.Seconds(), 5, "consumers get the settle window")
	require.Empty(t, cluster.deleted)
	require.False(t, c.legacyEdgeSwept)

	_, err = fx.pool.Exec(ctx, "UPDATE bucket_allocations SET outputs_published_at = now() - interval '16 minutes'")
	require.NoError(t, err)
	wait, err = c.sweepLegacyS3Edge(ctx)
	require.NoError(t, err)
	require.Zero(t, wait)
	require.Contains(t, cluster.deleted, "IngressRoute/seaweed-s3")
	require.True(t, c.legacyEdgeSwept)
}

// TestSweepLegacyS3EdgeOnce: the sweep runs once per process; later passes
// do not repeat the deletes. A failed sweep is retried on the next pass.
func TestSweepLegacyS3EdgeOnce(t *testing.T) {
	t.Parallel()
	cluster := &edgeCluster{}
	c := &Controller{cfg: Config{Managed: true}, deps: Deps{Cluster: cluster}}
	require.NoError(t, sweepLegacyEdge(c))
	first := len(cluster.deleted)
	require.NoError(t, sweepLegacyEdge(c))
	require.Len(t, cluster.deleted, first)

	failing := &failingCluster{edgeCluster: edgeCluster{}, err: errors.New("boom")}
	c = &Controller{cfg: Config{Managed: true}, deps: Deps{Cluster: failing}}
	require.ErrorContains(t, sweepLegacyEdge(c), "boom")
	require.False(t, c.legacyEdgeSwept, "a failed sweep is not recorded as done")
}

// TestSweepLegacyS3EdgeUnmanaged: an unmanaged installation never rendered
// the edge and does not sweep it; its endpoint is in-cluster.
func TestSweepLegacyS3EdgeUnmanaged(t *testing.T) {
	t.Parallel()
	cluster := &edgeCluster{}
	c := &Controller{cfg: Config{Managed: false}, deps: Deps{Cluster: cluster}}
	require.NoError(t, sweepLegacyEdge(c))
	require.Empty(t, cluster.applied)
	require.Empty(t, cluster.deleted)
	require.Equal(t, InternalBucketEndpoint(), mustEndpoint(t, c, store.BucketClaim{}))
}

// TestSweepLegacyS3EdgeToleratesMissingKinds: a kind the cluster does not
// serve cannot hold an object; absence is the desired state and the sweep
// carries on with the kinds that exist.
func TestSweepLegacyS3EdgeToleratesMissingKinds(t *testing.T) {
	t.Parallel()
	cluster := &edgeCluster{unknown: map[schema.GroupVersionKind]bool{edge.CertificateGVK: true}}
	c := &Controller{cfg: Config{Managed: true}, deps: Deps{Cluster: cluster}}
	require.NoError(t, sweepLegacyEdge(c))
	require.NotContains(t, cluster.deleted, "Certificate/seaweed-s3-tls")
	require.Contains(t, cluster.deleted, "IngressRoute/seaweed-s3")
	require.True(t, c.legacyEdgeSwept)
}

// TestSweepLegacyS3OpenBothShapes: the policy that opened the S3 port to
// every pod is deleted on the first store pass of a local and a managed
// installation alike (a dev cluster persists across upgrades too), once
// per process, and a failed sweep is retried.
func TestSweepLegacyS3OpenBothShapes(t *testing.T) {
	t.Parallel()
	for _, managed := range []bool{false, true} {
		cluster := &edgeCluster{}
		c := &Controller{cfg: Config{Managed: managed}, deps: Deps{Cluster: cluster}}
		require.NoError(t, c.sweepLegacyS3Open(context.Background()))
		require.Equal(t, []string{"NetworkPolicy/" + seaweed.LegacyS3OpenPolicy}, cluster.deleted, "managed=%v", managed)
		require.NoError(t, c.sweepLegacyS3Open(context.Background()))
		require.Len(t, cluster.deleted, 1, "the sweep runs once per process")
	}

	failing := &failingCluster{edgeCluster: edgeCluster{}, err: errors.New("boom")}
	c := &Controller{deps: Deps{Cluster: failing}}
	require.ErrorContains(t, c.sweepLegacyS3Open(context.Background()), "boom")
	require.False(t, c.legacyS3OpenSwept, "a failed sweep is not recorded as done")
}

type failingCluster struct {
	edgeCluster
	err error
}

func (f *failingCluster) Delete(context.Context, kube.ObjectRef) (bool, error) {
	return false, f.err
}

func mustEndpoint(t *testing.T, c *Controller, row store.BucketClaim) string {
	t.Helper()
	endpoint, err := c.bucketEndpoint(row)
	require.NoError(t, err)
	return endpoint
}

// TestBucketEndpointPrecedence: a claim's own route wins over the
// in-cluster URL; a route
// with TLS disabled publishes a plain-HTTP origin; a malformed stored
// route is an error, never a silent fallback.
func TestBucketEndpointPrecedence(t *testing.T) {
	t.Parallel()
	managed := &Controller{cfg: Config{Managed: true}}
	unmanaged := &Controller{cfg: Config{}}
	routed := store.BucketClaim{Route: []byte(`{"domain":"files.example.com","tls":"automatic"}`)}
	plain := store.BucketClaim{Route: []byte(`{"domain":"files.example.com","tls":"disabled"}`)}

	require.Equal(t, "https://files.example.com", mustEndpoint(t, managed, routed))
	require.Equal(t, "https://files.example.com", mustEndpoint(t, unmanaged, routed))
	require.Equal(t, "http://files.example.com", mustEndpoint(t, unmanaged, plain))
	require.Equal(t, InternalBucketEndpoint(), mustEndpoint(t, managed, store.BucketClaim{}))
	require.Equal(t, InternalBucketEndpoint(), mustEndpoint(t, unmanaged, store.BucketClaim{}))
	require.Equal(t, InternalBucketEndpoint(), mustEndpoint(t, unmanaged, store.BucketClaim{Route: []byte(`{"domain":""}`)}))

	_, err := unmanaged.bucketEndpoint(store.BucketClaim{Route: []byte(`not json`)})
	require.Error(t, err)
}

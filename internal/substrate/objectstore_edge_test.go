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
	typed := obj.(*unstructured.Unstructured)
	e.applied = append(e.applied, typed.GetKind()+"/"+typed.GetName())
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
	require.NoError(t, c.sweepLegacyS3Edge(context.Background()))
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

// TestSweepLegacyS3EdgeOnce: the sweep runs once per process; later passes
// do not repeat the deletes. A failed sweep is retried on the next pass.
func TestSweepLegacyS3EdgeOnce(t *testing.T) {
	t.Parallel()
	cluster := &edgeCluster{}
	c := &Controller{cfg: Config{Managed: true}, deps: Deps{Cluster: cluster}}
	require.NoError(t, c.sweepLegacyS3Edge(context.Background()))
	first := len(cluster.deleted)
	require.NoError(t, c.sweepLegacyS3Edge(context.Background()))
	require.Len(t, cluster.deleted, first)

	failing := &failingCluster{edgeCluster: edgeCluster{}, err: errors.New("boom")}
	c = &Controller{cfg: Config{Managed: true}, deps: Deps{Cluster: failing}}
	require.ErrorContains(t, c.sweepLegacyS3Edge(context.Background()), "boom")
	require.False(t, c.legacyEdgeSwept, "a failed sweep is not recorded as done")
}

// TestSweepLegacyS3EdgeUnmanaged: an unmanaged installation never rendered
// the edge and does not sweep it; its endpoint is in-cluster.
func TestSweepLegacyS3EdgeUnmanaged(t *testing.T) {
	t.Parallel()
	cluster := &edgeCluster{}
	c := &Controller{cfg: Config{Managed: false}, deps: Deps{Cluster: cluster}}
	require.NoError(t, c.sweepLegacyS3Edge(context.Background()))
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
	require.NoError(t, c.sweepLegacyS3Edge(context.Background()))
	require.NotContains(t, cluster.deleted, "Certificate/seaweed-s3-tls")
	require.Contains(t, cluster.deleted, "IngressRoute/seaweed-s3")
	require.True(t, c.legacyEdgeSwept)
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

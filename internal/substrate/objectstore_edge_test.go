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

func TestReconcilePublicEdgeApplies(t *testing.T) {
	t.Parallel()
	cluster := &edgeCluster{}
	c := &Controller{cfg: Config{Managed: true, S3Domain: "s3.example.com"}, deps: Deps{Cluster: cluster}}
	require.NoError(t, c.reconcilePublicEdge(context.Background()))
	require.ElementsMatch(t, []string{
		"Middleware/" + edge.RedirectMiddlewareName,
		"Certificate/seaweed-s3-tls",
		"IngressRoute/seaweed-s3",
		"IngressRoute/seaweed-s3-http",
	}, cluster.applied)
	require.Empty(t, cluster.deleted)
	require.Equal(t, "https://s3.example.com", c.bucketEndpoint())
}

// TestReconcilePublicEdgeRemoves: with the domain cleared every pass
// deletes exactly the edge objects, nothing of the store itself, and the
// published endpoint falls back to the in-cluster gateway.
func TestReconcilePublicEdgeRemoves(t *testing.T) {
	t.Parallel()
	cluster := &edgeCluster{}
	c := &Controller{cfg: Config{Managed: true}, deps: Deps{Cluster: cluster}}
	require.NoError(t, c.reconcilePublicEdge(context.Background()))
	require.Empty(t, cluster.applied)
	require.ElementsMatch(t, []string{
		"IngressRoute/seaweed-s3",
		"IngressRoute/seaweed-s3-http",
		"Middleware/" + edge.RedirectMiddlewareName,
		"Certificate/seaweed-s3-tls",
		"Ingress/seaweed-s3",
	}, cluster.deleted)
	require.Equal(t, InternalBucketEndpoint(), c.bucketEndpoint())
}

// TestReconcilePublicEdgeUnmanaged: an unmanaged installation neither
// renders nor sweeps the edge, even with a domain configured, and its
// endpoint stays in-cluster.
func TestReconcilePublicEdgeUnmanaged(t *testing.T) {
	t.Parallel()
	cluster := &edgeCluster{}
	c := &Controller{cfg: Config{Managed: false, S3Domain: "s3.example.com"}, deps: Deps{Cluster: cluster}}
	require.NoError(t, c.reconcilePublicEdge(context.Background()))
	require.Empty(t, cluster.applied)
	require.Empty(t, cluster.deleted)
	require.Equal(t, InternalBucketEndpoint(), c.bucketEndpoint())
}

// TestReconcilePublicEdgeToleratesMissingKinds: a kind the cluster does
// not serve cannot hold an object; absence is the desired state and the
// pass carries on with the kinds that exist.
func TestReconcilePublicEdgeToleratesMissingKinds(t *testing.T) {
	t.Parallel()
	cluster := &edgeCluster{unknown: map[schema.GroupVersionKind]bool{edge.CertificateGVK: true}}
	c := &Controller{cfg: Config{Managed: true}, deps: Deps{Cluster: cluster}}
	require.NoError(t, c.reconcilePublicEdge(context.Background()))
	require.NotContains(t, cluster.deleted, "Certificate/seaweed-s3-tls")
	require.Contains(t, cluster.deleted, "IngressRoute/seaweed-s3")

	// Any other delete failure still surfaces.
	failing := &failingCluster{edgeCluster: edgeCluster{}, err: errors.New("boom")}
	c = &Controller{cfg: Config{Managed: true}, deps: Deps{Cluster: failing}}
	require.ErrorContains(t, c.reconcilePublicEdge(context.Background()), "boom")
}

type failingCluster struct {
	edgeCluster
	err error
}

func (f *failingCluster) Delete(context.Context, kube.ObjectRef) (bool, error) {
	return false, f.err
}

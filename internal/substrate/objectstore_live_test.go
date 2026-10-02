package substrate

import (
	"context"
	"net/http"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/stretchr/testify/require"
	networkingv1 "k8s.io/api/networking/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/runtime/schema"

	"github.com/Hinkolas/skali/internal/bundle"
	"github.com/Hinkolas/skali/internal/claim"
	"github.com/Hinkolas/skali/internal/dbstore"
	"github.com/Hinkolas/skali/internal/edge"
	"github.com/Hinkolas/skali/internal/kube"
	"github.com/Hinkolas/skali/internal/kubetest"
	"github.com/Hinkolas/skali/internal/observe"
	"github.com/Hinkolas/skali/internal/project"
	"github.com/Hinkolas/skali/internal/store"
	"github.com/Hinkolas/skali/internal/substrate/seaweed"
	"github.com/Hinkolas/skali/internal/testdb"
)

// TestLiveObjectStoreBoot drives the physical SeaweedFS system from nothing
// to ready against a real k3d cluster: the visible metadata-claim wait, the
// filer store Secret derived from the claim's outputs, the all-in-one dev
// shape, and an answering S3 port through the network fence. Requires
// TEST_KUBECONFIG and TEST_DATABASE_URL.
func TestLiveObjectStoreBoot(t *testing.T) {
	config := kubetest.Config(t)
	pool := testdb.New(t)
	ctx := context.Background()

	client, err := kube.NewFromConfig(config)
	require.NoError(t, err)
	installOperator(t, client)

	st := store.NewStore(pool)
	dbSvc := dbstore.New(st)
	projects := project.New(st)
	controller := New(Deps{
		DB:       dbSvc,
		Cluster:  KubeCluster{Client: client},
		Observed: observe.NewStore(nil),
		Seaweed:  seaweed.NewClient(client, Namespace),
	}, Config{Managed: false})

	cleanupPlatform(t, client)

	// One pending bucket claim exists alongside the store (it is never
	// driven; the store alone is under test).
	suffix := uuid.Must(uuid.NewV7()).String()[24:]
	proj, err := projects.Create(ctx, "demo"+suffix, "", uuid.Nil)
	require.NoError(t, err)
	env, err := projects.CreateEnvironment(ctx, proj.ID, "production", project.EnvironmentOptions{})
	require.NoError(t, err)
	owner := dbstore.ServiceOwner(proj.ID, env.ID, proj.Name, "production", "files")
	_, err = dbSvc.EnsureBucketClaim(ctx, owner, dbstore.BucketSpec{
		Visibility: "private", Versioning: "disabled",
	})
	require.NoError(t, err)

	// Dev creates the store lazily; the first bucket claim calls this.
	row, err := controller.ensureObjectStoreRow(ctx)
	require.NoError(t, err)
	require.Equal(t, dbstore.StateActive, row.State)
	require.EqualValues(t, 1, row.Masters)

	// The first reconcile pass cannot finish: the metadata database claim is
	// the visible first gate of the REWORK 10.5 chain.
	requeue, err := controller.reconcileObjectStore(ctx)
	require.NoError(t, err)
	require.Equal(t, requeueWait, requeue, "the store visibly waits before its metadata database exists")
	metadata, err := dbSvc.LiveSystemClaim(ctx, MetadataClaimKey)
	require.NoError(t, err)
	require.NotEqual(t, string(claim.PhaseProvisioned), metadata.Phase)

	// Drive the store and its metadata claim like the workers would.
	deadline := time.Now().Add(8 * time.Minute)
	for {
		require.False(t, time.Now().After(deadline),
			"object store not ready before deadline; metadata wait: %s", controller.WaitingReason(metadata.ID))
		mRequeue, mErr := controller.reconcileClaim(ctx, metadata.ID)
		if mErr != nil {
			t.Logf("metadata claim (retrying): %v", mErr)
		} else if fresh, freshErr := dbSvc.GetClaim(ctx, metadata.ID); freshErr == nil {
			stepClaim(t, "metadata claim", mRequeue, mErr,
				claim.Phase(fresh.Phase), controller.WaitingReason(metadata.ID))
		}
		requeue, err := controller.reconcileObjectStore(ctx)
		if err != nil {
			t.Logf("object store (retrying): %v", err)
		} else if requeue == 0 {
			break
		}
		time.Sleep(2 * time.Second)
	}

	// The metadata database provisioned through the ordinary system-claim
	// path; the filer store Secret carries its outputs, password included,
	// Secret-to-Secret only.
	outputs, err := controller.EnsureSystemClaim(ctx, MetadataClaimKey, metadataClaimSpec())
	require.NoError(t, err)
	require.True(t, outputs.Provisioned)
	filerStore, err := client.Clientset.CoreV1().Secrets(Namespace).Get(ctx, seaweed.FilerStoreSecret, metav1.GetOptions{})
	require.NoError(t, err)
	require.Equal(t, outputs.Host, string(filerStore.Data["WEED_POSTGRES2_HOSTNAME"]))
	require.Equal(t, outputs.Database, string(filerStore.Data["WEED_POSTGRES2_DATABASE"]))
	require.NotEmpty(t, filerStore.Data["WEED_POSTGRES2_PASSWORD"])

	// The dev all-in-one answers on the S3 port through the fence: auth is
	// on from process start (the bootstrap deny identity), so an anonymous
	// request is rejected, not served.
	data, status, err := client.ServiceProxyDo(ctx, http.MethodGet, Namespace,
		seaweed.S3Service, seaweed.S3Port, "/", nil, nil)
	require.NoError(t, err)
	require.Equal(t, http.StatusForbidden, status, "anonymous S3 must be denied, got %d: %s", status, data)

	// The admin channel works end to end: the bootstrap identity is the
	// only one, kept so the identity list can never go empty.
	identities, err := controller.deps.Seaweed.Identities(ctx)
	require.NoError(t, err)
	require.NotNil(t, identities.Find(seaweed.BootstrapIdentityName))

	// Idempotence: a settled store reconciles to a no-op.
	requeue, err = controller.reconcileObjectStore(ctx)
	require.NoError(t, err)
	require.Zero(t, requeue)
}

// TestLiveObjectStorePublicEdge drives the public S3 edge through its
// lifecycle against a real cluster: enabling the domain renders the routes
// and the certificate, a hostname change replaces them in place, disabling
// removes them (a plain Ingress left by a pre-IngressRoute store included),
// and the whole sequence repeats. The store itself is not booted: the edge
// pass names edge objects only, which is the point. Requires
// TEST_KUBECONFIG; installs cert-manager into the test cluster for the
// Certificate kind.
func TestLiveObjectStorePublicEdge(t *testing.T) {
	config := kubetest.Config(t)
	ctx := context.Background()
	client, err := kube.NewFromConfig(config)
	require.NoError(t, err)
	installCertManager(t, client)
	cleanupPlatform(t, client)

	controller := &Controller{
		cfg:  Config{Managed: true, S3Domain: "s3.first.example.test"},
		deps: Deps{Cluster: KubeCluster{Client: client}},
	}
	require.NoError(t, controller.ensureNamespace(ctx))

	// A store published before the IngressRoute rework left a plain
	// Ingress under the route's name.
	pathType := networkingv1.PathTypePrefix
	_, err = client.Clientset.NetworkingV1().Ingresses(Namespace).Create(ctx, &networkingv1.Ingress{
		ObjectMeta: metav1.ObjectMeta{Name: "seaweed-s3", Namespace: Namespace},
		Spec: networkingv1.IngressSpec{Rules: []networkingv1.IngressRule{{
			Host: "s3.legacy.example.test",
			IngressRuleValue: networkingv1.IngressRuleValue{HTTP: &networkingv1.HTTPIngressRuleValue{
				Paths: []networkingv1.HTTPIngressPath{{Path: "/", PathType: &pathType, Backend: networkingv1.IngressBackend{
					Service: &networkingv1.IngressServiceBackend{Name: seaweed.S3Service, Port: networkingv1.ServiceBackendPort{Number: seaweed.S3Port}},
				}}},
			}},
		}}},
	}, metav1.CreateOptions{})
	require.NoError(t, err)

	ingressRoutes := schema.GroupVersionResource{Group: "traefik.io", Version: "v1alpha1", Resource: "ingressroutes"}
	middlewares := schema.GroupVersionResource{Group: "traefik.io", Version: "v1alpha1", Resource: "middlewares"}
	certificates := schema.GroupVersionResource{Group: "cert-manager.io", Version: "v1", Resource: "certificates"}
	get := func(gvr schema.GroupVersionResource, name string) (*unstructured.Unstructured, bool) {
		object, err := client.Dynamic.Resource(gvr).Namespace(Namespace).Get(ctx, name, metav1.GetOptions{})
		if apierrors.IsNotFound(err) {
			return nil, false
		}
		require.NoError(t, err)
		return object, true
	}
	edgeHost := func(domain string) {
		t.Helper()
		for _, name := range []string{"seaweed-s3", "seaweed-s3-http"} {
			route, ok := get(ingressRoutes, name)
			require.True(t, ok, "%s must exist", name)
			routes, _, err := unstructured.NestedSlice(route.Object, "spec", "routes")
			require.NoError(t, err)
			require.Len(t, routes, 1)
			match := routes[0].(map[string]any)["match"].(string)
			require.Contains(t, match, "Host(\""+domain+"\")", "%s serves the current domain only", name)
		}
		_, ok := get(middlewares, edge.RedirectMiddlewareName)
		require.True(t, ok, "redirect middleware must exist")
		certificate, ok := get(certificates, "seaweed-s3-tls")
		require.True(t, ok, "certificate must exist")
		names, _, err := unstructured.NestedStringSlice(certificate.Object, "spec", "dnsNames")
		require.NoError(t, err)
		require.Equal(t, []string{domain}, names)
	}
	edgeAbsent := func() {
		t.Helper()
		for _, name := range []string{"seaweed-s3", "seaweed-s3-http"} {
			_, ok := get(ingressRoutes, name)
			require.False(t, ok, "%s must be gone", name)
		}
		_, ok := get(middlewares, edge.RedirectMiddlewareName)
		require.False(t, ok, "redirect middleware must be gone")
		_, ok = get(certificates, "seaweed-s3-tls")
		require.False(t, ok, "certificate must be gone")
		_, err := client.Clientset.NetworkingV1().Ingresses(Namespace).Get(ctx, "seaweed-s3", metav1.GetOptions{})
		require.True(t, apierrors.IsNotFound(err), "the legacy Ingress must be gone, got %v", err)
	}

	// Enable. cert-manager's webhook admits Certificates only once its CA
	// is injected, so the first pass may need a few retries.
	require.Eventually(t, func() bool {
		if err := controller.reconcilePublicEdge(ctx); err != nil {
			t.Logf("public edge (retrying): %v", err)
			return false
		}
		return true
	}, 3*time.Minute, 5*time.Second)
	edgeHost("s3.first.example.test")
	require.Equal(t, "https://s3.first.example.test", mustEndpoint(t, controller, store.BucketClaim{}))

	// Hostname change: the same objects carry the new host, nothing of the
	// old one remains.
	controller.cfg.S3Domain = "s3.second.example.test"
	require.NoError(t, controller.reconcilePublicEdge(ctx))
	edgeHost("s3.second.example.test")
	require.Equal(t, "https://s3.second.example.test", mustEndpoint(t, controller, store.BucketClaim{}))

	// Disable: every edge object goes, the legacy Ingress with them, and
	// the endpoint falls back to the in-cluster gateway. A repeated pass
	// finds nothing to do.
	controller.cfg.S3Domain = ""
	require.NoError(t, controller.reconcilePublicEdge(ctx))
	edgeAbsent()
	require.Equal(t, InternalBucketEndpoint(), mustEndpoint(t, controller, store.BucketClaim{}))
	require.NoError(t, controller.reconcilePublicEdge(ctx))
	edgeAbsent()

	// Re-enable: the sequence repeats from a clean slate.
	controller.cfg.S3Domain = "s3.first.example.test"
	require.NoError(t, controller.reconcilePublicEdge(ctx))
	edgeHost("s3.first.example.test")
	controller.cfg.S3Domain = ""
	require.NoError(t, controller.reconcilePublicEdge(ctx))
	edgeAbsent()
}

// installCertManager applies the pinned cert-manager bundle so the
// Certificate kind exists in the test cluster and waits for the webhook
// that admits it.
func installCertManager(t *testing.T, client *kube.Client) {
	t.Helper()
	ctx := context.Background()
	applier := &bundle.Applier{Client: client}
	require.NoError(t, applier.ApplyManifest(ctx, bundle.CertManagerManifest()))
	for _, name := range []string{"cert-manager", "cert-manager-cainjector", "cert-manager-webhook"} {
		require.NoError(t, applier.WaitDeploymentReady(ctx, bundle.CertManagerNamespace, name))
	}
}

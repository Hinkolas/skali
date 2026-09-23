package bundle

import (
	"github.com/Hinkolas/skali/internal/edge"
	"strings"
	"testing"

	"github.com/stretchr/testify/require"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"

	"github.com/Hinkolas/skali/internal/substrate/cnpg"
)

func TestRenderBundleObjects(t *testing.T) {
	t.Parallel()
	objects, err := Render(localProfile())
	require.NoError(t, err)

	require.Len(t, objects.Namespace, 1)
	require.Equal(t, "Namespace", objects.Namespace[0].GetKind())
	// The development CA precedes the issuer that signs with it, in the
	// namespace cert-manager reads cluster-scoped issuer secrets from.
	require.Len(t, objects.Issuer, 2)
	caSecret := objects.Issuer[0]
	require.Equal(t, "Secret", caSecret.GetKind())
	require.Equal(t, CAIssuerSecretName, caSecret.GetName())
	require.Equal(t, CertManagerNamespace, caSecret.GetNamespace())
	secretType, _, _ := unstructured.NestedString(caSecret.Object, "type")
	require.Equal(t, "kubernetes.io/tls", secretType)
	issuer := objects.Issuer[1]
	require.Equal(t, "ClusterIssuer", issuer.GetKind())
	require.Equal(t, edge.IssuerName, issuer.GetName())
	caRef, _, _ := unstructured.NestedString(issuer.Object, "spec", "ca", "secretName")
	require.Equal(t, CAIssuerSecretName, caRef)
	require.Len(t, objects.Edge, 1)
	require.Equal(t, "TLSOption", objects.Edge[0].GetKind())
	require.Len(t, objects.Database, 1)
	require.Equal(t, "Cluster", objects.Database[0].GetKind())
	require.Len(t, objects.Registry, 4)
	require.Len(t, objects.Skalid, 12)
	// The console compresses locally too, through the stage's own
	// Middleware ahead of its router, and the redirect Middleware rides
	// this stage locally (production renders it in the registry stage).
	compress := objects.Skalid[6]
	require.Equal(t, "Middleware", compress.GetKind())
	require.Equal(t, "compress", compress.GetName())
	excluded, _, _ := unstructured.NestedStringSlice(compress.Object, "spec", "compress", "excludedContentTypes")
	require.Equal(t, edge.CompressExcludedContentTypes, excluded)
	redirect := objects.Skalid[7]
	require.Equal(t, "Middleware", redirect.GetKind())
	require.Equal(t, edge.RedirectMiddlewareName, redirect.GetName())
	// The platform domain serves TLS locally exactly like production: an
	// explicit Certificate, the websecure console router, and a redirecting
	// plain-HTTP router.
	certificate := objects.Skalid[8]
	require.Equal(t, "Certificate", certificate.GetKind())
	require.Equal(t, "skalid-tls", certificate.GetName())
	dnsNames, _, _ := unstructured.NestedStringSlice(certificate.Object, "spec", "dnsNames")
	require.Equal(t, []string{LocalPlatformHost}, dnsNames)
	console := objects.Skalid[9]
	require.Equal(t, "IngressRoute", console.GetKind())
	require.Equal(t, "skalid", console.GetName())
	consoleEntryPoints, _, _ := unstructured.NestedStringSlice(console.Object, "spec", "entryPoints")
	require.Equal(t, []string{edge.EntryPointWebSecure}, consoleEntryPoints)
	consoleSecret, _, _ := unstructured.NestedString(console.Object, "spec", "tls", "secretName")
	require.Equal(t, "skalid-tls", consoleSecret)
	consoleJSON, err := console.MarshalJSON()
	require.NoError(t, err)
	require.Contains(t, string(consoleJSON), `"middlewares":[{"name":"compress"}]`)
	consoleHTTP := objects.Skalid[10]
	require.Equal(t, "IngressRoute", consoleHTTP.GetKind())
	require.Equal(t, "skalid-http", consoleHTTP.GetName())
	consoleHTTPJSON, err := consoleHTTP.MarshalJSON()
	require.NoError(t, err)
	require.Contains(t, string(consoleHTTPJSON), `"middlewares":[{"name":"redirect-https"}]`)
	// The edge identity route: host-independent and ranked above every
	// tenant rule, plain HTTP only, no certificate involved.
	probe := objects.Skalid[11]
	require.Equal(t, "IngressRoute", probe.GetKind())
	require.Equal(t, "skalid-edge-probe", probe.GetName())
	entryPoints, _, _ := unstructured.NestedStringSlice(probe.Object, "spec", "entryPoints")
	require.Equal(t, []string{"web"}, entryPoints)
	probeRoutes, _, _ := unstructured.NestedSlice(probe.Object, "spec", "routes")
	require.Len(t, probeRoutes, 1)
	probeRoute := probeRoutes[0].(map[string]any)
	require.Equal(t, "PathPrefix(`/.well-known/skali-edge`)", probeRoute["match"])
	require.EqualValues(t, 10000, probeRoute["priority"])
	_, hasTLS, _ := unstructured.NestedMap(probe.Object, "spec", "tls")
	require.False(t, hasTLS)
	require.Len(t, objects.EdgeMetrics, 1)
	require.Equal(t, "HelmChartConfig", objects.EdgeMetrics[0].GetKind())
	require.Equal(t, "kube-system", objects.EdgeMetrics[0].GetNamespace(),
		"the traefik chart overlay must land beside the k3s-owned HelmChart")
	require.Len(t, objects.BootstrapUser, 2)

	// Everything namespaced sits in skali-system; the admin password only
	// ever appears base64-encoded in the bootstrap secret.
	for _, object := range objects.Skalid {
		if object.GetKind() != "ClusterRole" && object.GetKind() != "ClusterRoleBinding" {
			require.Equal(t, Namespace, object.GetNamespace(), object.GetKind())
		}
	}
	deployment := objects.Skalid[4]
	require.Equal(t, "Deployment", deployment.GetKind())
	raw, err := deployment.MarshalJSON()
	require.NoError(t, err)
	require.Contains(t, string(raw), "skalid:dev")
	require.Contains(t, string(raw), "skali-registry.skali-system.svc:5000")

	// The image ID rides the pod template as an annotation, so a rebuilt
	// image rolls the deployment even under the unchanged skalid:dev tag.
	annotations, _, err := unstructured.NestedStringMap(deployment.Object,
		"spec", "template", "metadata", "annotations")
	require.NoError(t, err)
	require.Equal(t, "sha256:0123456789abcdef", annotations["skali.dev/image-id"])

	// An empty image ID (published, immutable tags) omits the annotation.
	plain, err := Render(Profile{
		SkalidImage:   "ghcr.io/hinkolas/skalid:v1.0.0",
		AuthSecret:    strings.Repeat("a", 32),
		AdminEmail:    "dev@skali.localhost",
		AdminPassword: "generated-password",
		RegistryHost:  "localhost:5510",
		Local:         localCA(),
	})
	require.NoError(t, err)
	_, found, err := unstructured.NestedStringMap(plain.Skalid[4].Object,
		"spec", "template", "metadata", "annotations")
	require.NoError(t, err)
	require.False(t, found, "no annotations block expected without an image ID")

	// The vendored operator manifest parses.
	cnpg, err := ParseManifest(CNPGManifest())
	require.NoError(t, err)
	require.NotEmpty(t, cnpg)
}

// TestImagePins pins the bundle's image discipline: skali-db renders with
// an explicit imageName (frozen against operator-default drift, and never
// pinned below a previously running major), and the operator manifest is
// rewritten to IfNotPresent so pre-pulled images are honored on every pod
// start.
func TestImagePins(t *testing.T) {
	t.Parallel()
	objects, err := Render(Profile{
		SkalidImage:   "skalid:dev",
		AuthSecret:    strings.Repeat("a", 32),
		AdminEmail:    "dev@skali.localhost",
		AdminPassword: "generated-password",
		RegistryHost:  "localhost:5510",
		Local:         localCA(),
	})
	require.NoError(t, err)
	raw, err := objects.Database[0].MarshalJSON()
	require.NoError(t, err)
	require.Contains(t, string(raw), `"imageName":"`+BootstrapPostgresImage+`"`)

	image, ok := cnpg.Lookup("postgres", 18)
	require.True(t, ok)
	require.Equal(t, image.Ref, BootstrapPostgresImage,
		"skali-db and the substrate catalog must agree on the postgres 18 image")

	manifest := string(CNPGManifest())
	require.NotContains(t, manifest, "imagePullPolicy: Always")
	require.Contains(t, manifest, "imagePullPolicy: IfNotPresent")
	require.Contains(t, manifest, CNPGOperatorImage,
		"the exported operator image must match the vendored manifest")
}

func TestHashTracksProfile(t *testing.T) {
	t.Parallel()
	base := localProfile()
	require.Equal(t, Hash(base), Hash(base), "the hash must be deterministic")

	// A regenerated development CA must yield a converge: the issuer
	// secret in the cluster would otherwise sign with the old key while
	// the developer trusts the new certificate.
	rotatedCA := base
	rotatedCA.Local = &Local{CACertPEM: "-----BEGIN CERTIFICATE-----\nrotated\n-----END CERTIFICATE-----\n", CAKeyPEM: base.Local.CAKeyPEM}
	require.NotEqual(t, Hash(base), Hash(rotatedCA))

	// A rebuilt image changes only the image ID behind the same tag; the
	// hash must move with it so the fast path yields to a converge.
	rebuilt := base
	rebuilt.SkalidImageID = "sha256:fedcba9876543210"
	require.NotEqual(t, Hash(base), Hash(rebuilt))

	changedRegistry := base
	changedRegistry.RegistryHost = "localhost:5512"
	require.NotEqual(t, Hash(base), Hash(changedRegistry))
}

// The local shape renders a ClusterIssuer that signs with the development
// CA, so rendering without one must fail instead of producing an issuer
// that points at a missing Secret.
func TestRenderLocalRequiresCA(t *testing.T) {
	t.Parallel()
	profile := localProfile()
	profile.Local = nil
	_, err := Render(profile)
	require.ErrorContains(t, err, "development CA is required")

	profile.Local = &Local{CACertPEM: "cert"}
	_, err = Render(profile)
	require.ErrorContains(t, err, "certificate and key are required")
}

// The exported cert-manager images must match what the vendored manifest
// deploys: local dev pre-pulls exactly these into the cluster.
func TestCertManagerImagesMatchManifest(t *testing.T) {
	t.Parallel()
	manifest := string(CertManagerManifest())
	images := CertManagerImages()
	require.Len(t, images, 3)
	for _, image := range images {
		require.Contains(t, manifest, `image: "`+image+`"`, image)
	}
	require.NotContains(t, manifest, "imagePullPolicy: Always")
}

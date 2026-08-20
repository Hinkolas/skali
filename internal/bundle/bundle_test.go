package bundle

import (
	"strings"
	"testing"

	"github.com/stretchr/testify/require"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"

	"github.com/Hinkolas/skali/internal/substrate/cnpg"
)

func TestRenderBundleObjects(t *testing.T) {
	t.Parallel()
	objects, err := Render(Profile{
		SkalidImage:   "skalid:dev",
		SkalidImageID: "sha256:0123456789abcdef",
		AuthSecret:    strings.Repeat("a", 32),
		AdminEmail:    "dev@skali.localhost",
		AdminPassword: "generated-password",
		RegistryHost:  "localhost:5510",
	})
	require.NoError(t, err)

	require.Len(t, objects.Namespace, 1)
	require.Equal(t, "Namespace", objects.Namespace[0].GetKind())
	require.Len(t, objects.Database, 1)
	require.Equal(t, "Cluster", objects.Database[0].GetKind())
	require.Len(t, objects.Registry, 4)
	require.Len(t, objects.Skalid, 7)
	require.Empty(t, objects.Web, "the web console is production-only")
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
	base := Profile{
		SkalidImage:   "skalid:dev",
		SkalidImageID: "sha256:0123456789abcdef",
		AuthSecret:    strings.Repeat("a", 32),
		AdminEmail:    "dev@skali.localhost",
		AdminPassword: "generated-password",
		RegistryHost:  "localhost:5510",
	}
	require.Equal(t, Hash(base), Hash(base), "the hash must be deterministic")

	// A rebuilt image changes only the image ID behind the same tag; the
	// hash must move with it so the fast path yields to a converge.
	rebuilt := base
	rebuilt.SkalidImageID = "sha256:fedcba9876543210"
	require.NotEqual(t, Hash(base), Hash(rebuilt))

	changedRegistry := base
	changedRegistry.RegistryHost = "localhost:5512"
	require.NotEqual(t, Hash(base), Hash(changedRegistry))
}

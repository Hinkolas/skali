package bundle

import (
	"strings"
	"testing"

	"github.com/stretchr/testify/require"
)

func TestRenderBundleObjects(t *testing.T) {
	t.Parallel()
	objects, err := Render(Profile{
		SkalidImage:   "skalid:dev",
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
	require.Len(t, objects.Issuer, 1)
	require.Len(t, objects.Skalid, 7)
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

	// The vendored operator manifests parse.
	cnpg, err := ParseManifest(CNPGManifest())
	require.NoError(t, err)
	require.NotEmpty(t, cnpg)
	certManager, err := ParseManifest(CertManagerManifest())
	require.NoError(t, err)
	require.NotEmpty(t, certManager)
}

package bundle

import (
	"strings"
	"testing"

	"github.com/stretchr/testify/require"
)

func externalProfile() Profile {
	profile := productionProfile()
	profile.RegistryHost = profile.Production.RegistryDomain
	profile.Production.External = &ExternalCluster{IngressClassName: "nginx"}
	return profile
}

func TestExternalRenders(t *testing.T) {
	t.Parallel()
	profile := externalProfile()
	sources := stageSources(profile)

	database, registry, skalid, issuer := sources[2], sources[3], sources[4], sources[1]

	// Placement belongs to the cluster operator: no capability selectors.
	require.NotContains(t, database, "nodeSelector")
	require.NotContains(t, registry, "nodeSelector")

	// The named ingress class replaces traefik everywhere routes render.
	require.Contains(t, issuer, "ingressClassName: nginx")
	require.Contains(t, registry, "ingressClassName: nginx")
	require.Contains(t, skalid, "ingressClassName: nginx")
	require.NotContains(t, skalid, "traefik")

	// Pulls travel the public domain: skalid injects the pull secret and
	// stamps its ingress class into rendered routes.
	require.Contains(t, skalid, "SKALI_REGISTRY_PULL_SECRET")
	require.Contains(t, skalid, "SKALI_INGRESS_CLASS")
	require.Contains(t, skalid, "value: nginx")
	require.Contains(t, skalid, "value: registry.example.com",
		"SKALI_REGISTRY_HOST is the public domain in this mode")

	// The managed render carries none of it.
	managed := stageSources(productionProfile())
	require.Contains(t, managed[2], "nodeSelector")
	require.NotContains(t, managed[4], "SKALI_REGISTRY_PULL_SECRET")
	require.Contains(t, managed[4], "ingressClassName: traefik")
}

func TestExternalStorageClass(t *testing.T) {
	t.Parallel()
	profile := externalProfile()
	profile.Production.External.StorageClassName = "fast-ssd"
	sources := stageSources(profile)
	require.Contains(t, sources[2], "storageClass: fast-ssd")
	require.Contains(t, sources[3], "storageClassName: fast-ssd")

	// Empty relies on the cluster default: nothing rendered.
	bare := externalProfile()
	require.NotContains(t, stageSources(bare)[2], "storageClass:")
	require.NotContains(t, stageSources(bare)[3], "storageClassName:")
}

func TestExternalHash(t *testing.T) {
	t.Parallel()
	managed := Hash(productionProfile())
	external := Hash(externalProfile())
	require.NotEqual(t, managed, external, "the external render is a different bundle")

	// Skipped operator manifests leave the hash: the choices are part of
	// what a converge would apply.
	reusedCNPG := externalProfile()
	reusedCNPG.Production.External.SkipCNPG = true
	require.NotEqual(t, external, Hash(reusedCNPG))
	reusedBoth := externalProfile()
	reusedBoth.Production.External.SkipCNPG = true
	reusedBoth.Production.External.SkipCertManager = true
	require.NotEqual(t, Hash(reusedCNPG), Hash(reusedBoth))
}

func TestExternalRequiresIngressClass(t *testing.T) {
	t.Parallel()
	profile := externalProfile()
	profile.Production.External.IngressClassName = ""
	_, err := Render(profile)
	require.ErrorContains(t, err, "existing-cluster mode requires an ingress class")
}

func TestExternalRenderParses(t *testing.T) {
	t.Parallel()
	objects, err := Render(externalProfile())
	require.NoError(t, err)
	require.NotEmpty(t, objects.Skalid)
	for _, source := range stageSources(externalProfile()) {
		require.False(t, strings.Contains(source, "%!"), "format verb leak in:\n%s", source)
	}
}

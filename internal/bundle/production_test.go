package bundle

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/stretchr/testify/require"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"

	"github.com/Hinkolas/skali/internal/layout"
)

func localProfile() Profile {
	return Profile{
		SkalidImage:   "skalid:dev",
		SkalidImageID: "sha256:0123456789abcdef",
		AuthSecret:    strings.Repeat("a", 32),
		AdminEmail:    "dev@skali.localhost",
		AdminPassword: "generated-password",
		RegistryHost:  "localhost:5510",
	}
}

func productionProfile() Profile {
	return Profile{
		SkalidImage:  "ghcr.io/hinkolas/skalid:v2.0.0",
		AuthSecret:   strings.Repeat("a", 32),
		RegistryHost: RegistryInternalHost,
		Production: &Production{
			IngressHost:        "skali.example.com",
			ACMEEmail:          "ops@example.com",
			Capabilities:       layout.Capabilities,
			DatabaseTier:       layout.TierSynchronous,
			DatabaseStorage:    "10Gi",
			RegistryStorage:    "20Gi",
			InstallationRecord: "version: \"1\"\ninstallationId: 0f0f\ncluster: production\n",
		},
	}
}

// TestLocalRenderFrozen pins the local render byte-for-byte against
// goldens captured before the production profile existed. Any drift here
// forces a converge on every dev machine and defeats the warm fast path.
func TestLocalRenderFrozen(t *testing.T) {
	t.Parallel()
	profile := localProfile()
	sources := stageSources(profile)
	require.Len(t, sources, 7)

	frozen := map[string]int{
		"local-namespace.yaml": 0,
		"local-database.yaml":  2,
		"local-registry.yaml":  3,
		"local-skalid.yaml":    4,
		"local-bootstrap.yaml": 6,
	}
	for name, index := range frozen {
		golden, err := os.ReadFile(filepath.Join("testdata", name))
		require.NoError(t, err, name)
		require.Equal(t, string(golden), sources[index], name)
	}
	// Production-only stages contribute zero bytes locally.
	require.Empty(t, sources[1], "issuer stage must be empty locally")
	require.Empty(t, sources[5], "record stage must be empty locally")

	// The full hash including the vendored operator manifests is frozen
	// too: cert-manager must not leak into the local fingerprint.
	hashGolden, err := os.ReadFile(filepath.Join("testdata", "local-hash.txt"))
	require.NoError(t, err)
	require.Equal(t, strings.TrimSpace(string(hashGolden)), Hash(profile))
}

func TestProductionDatabaseTiers(t *testing.T) {
	t.Parallel()
	for tier, golden := range map[layout.Tier]string{
		layout.TierSingle:       "database-single.yaml",
		layout.TierAsynchronous: "database-asynchronous.yaml",
		layout.TierSynchronous:  "database-synchronous.yaml",
	} {
		profile := productionProfile()
		profile.Production.DatabaseTier = tier
		source := databaseYAML(profile)
		expected, err := os.ReadFile(filepath.Join("testdata", golden))
		require.NoError(t, err, golden)
		require.Equal(t, string(expected), source, golden)
	}
	require.Equal(t, 1, TierInstances(layout.TierSingle))
	require.Equal(t, 2, TierInstances(layout.TierAsynchronous))
	require.Equal(t, 3, TierInstances(layout.TierSynchronous))
}

func TestRenderProductionObjects(t *testing.T) {
	t.Parallel()
	profile := productionProfile()
	objects, err := Render(profile)
	require.NoError(t, err)

	// Issuer: one ClusterIssuer named skali on the production ACME
	// directory with the traefik http01 solver.
	require.Len(t, objects.Issuer, 1)
	issuer := objects.Issuer[0]
	require.Equal(t, "ClusterIssuer", issuer.GetKind())
	require.Equal(t, IssuerName, issuer.GetName())
	server, _, _ := unstructured.NestedString(issuer.Object, "spec", "acme", "server")
	require.Equal(t, ACMEProductionServer, server)
	email, _, _ := unstructured.NestedString(issuer.Object, "spec", "acme", "email")
	require.Equal(t, "ops@example.com", email)
	solvers, _, _ := unstructured.NestedSlice(issuer.Object, "spec", "acme", "solvers")
	require.Len(t, solvers, 1)

	// The staging override surfaces verbatim.
	staged := productionProfile()
	staged.Production.ACMEServer = "https://acme-staging-v02.api.letsencrypt.org/directory"
	require.Contains(t, issuerYAML(staged), "acme-staging-v02")

	// Skalid: capabilities env, TLS ingress on the api domain.
	deployment := objects.Skalid[4]
	require.Equal(t, "Deployment", deployment.GetKind())
	raw, err := deployment.MarshalJSON()
	require.NoError(t, err)
	require.Contains(t, string(raw), `"SKALI_CAPABILITIES"`)
	require.Contains(t, string(raw), "application;database;object-storage;registry;edge")

	ingress := objects.Skalid[6]
	require.Equal(t, "Ingress", ingress.GetKind())
	require.Equal(t, IssuerName, ingress.GetAnnotations()["cert-manager.io/cluster-issuer"])
	tls, _, _ := unstructured.NestedSlice(ingress.Object, "spec", "tls")
	require.Len(t, tls, 1)
	entry := tls[0].(map[string]any)
	require.Equal(t, "skalid-tls", entry["secretName"])
	require.Equal(t, []any{"skali.example.com"}, entry["hosts"])
	rules, _, _ := unstructured.NestedSlice(ingress.Object, "spec", "rules")
	require.Equal(t, "skali.example.com", rules[0].(map[string]any)["host"])

	// Registry: pinned to a registry-capable node with the sized volume.
	var pvc, registryDeployment *unstructured.Unstructured
	for index := range objects.Registry {
		switch objects.Registry[index].GetKind() {
		case "PersistentVolumeClaim":
			pvc = &objects.Registry[index]
		case "Deployment":
			registryDeployment = &objects.Registry[index]
		}
	}
	require.NotNil(t, pvc)
	require.NotNil(t, registryDeployment)
	size, _, _ := unstructured.NestedString(pvc.Object, "spec", "resources", "requests", "storage")
	require.Equal(t, "20Gi", size)
	selector, _, _ := unstructured.NestedStringMap(registryDeployment.Object,
		"spec", "template", "spec", "nodeSelector")
	require.Equal(t, map[string]string{"skali.dev/capability-registry": "true"}, selector)

	// Record: the ConfigMap round-trips the canonical text exactly.
	require.Len(t, objects.Record, 1)
	record := objects.Record[0]
	require.Equal(t, "ConfigMap", record.GetKind())
	require.Equal(t, RecordName, record.GetName())
	text, _, _ := unstructured.NestedString(record.Object, "data", RecordKey)
	require.Equal(t, profile.Production.InstallationRecord, text)

	// The vendored cert-manager manifest parses.
	certManager, err := ParseManifest(CertManagerManifest())
	require.NoError(t, err)
	require.NotEmpty(t, certManager)
}

func TestProductionHashProperties(t *testing.T) {
	t.Parallel()
	base := productionProfile()
	require.Equal(t, Hash(base), Hash(base))
	require.NotEqual(t, Hash(base), Hash(localProfile()))

	// Every production input moves the hash.
	for name, mutate := range map[string]func(*Production){
		"tier":    func(p *Production) { p.DatabaseTier = layout.TierSingle },
		"ingress": func(p *Production) { p.IngressHost = "other.example.com" },
		"acme":    func(p *Production) { p.ACMEServer = "https://acme-staging-v02.api.letsencrypt.org/directory" },
		"record":  func(p *Production) { p.InstallationRecord = "version: \"1\"\ninstallationId: ffff\n" },
	} {
		changed := productionProfile()
		mutate(changed.Production)
		require.NotEqual(t, Hash(base), Hash(changed), name)
	}

	// The admin credentials must NOT move the production hash: they are
	// never persisted host-side, so a repeat run could not reproduce them.
	credentialed := productionProfile()
	credentialed.AdminEmail = "ops@example.com"
	credentialed.AdminPassword = "secret"
	require.Equal(t, Hash(base), Hash(credentialed))

	// Locally they stay part of the fingerprint (the state file records
	// them and the local converge applies the stage).
	localChanged := localProfile()
	localChanged.AdminPassword = "different"
	require.NotEqual(t, Hash(localProfile()), Hash(localChanged))
}

func TestProductionProfileValidation(t *testing.T) {
	t.Parallel()
	for field, mutate := range map[string]func(*Production){
		"ingress host":          func(p *Production) { p.IngressHost = "" },
		"acme email":            func(p *Production) { p.ACMEEmail = "" },
		"capabilities":          func(p *Production) { p.Capabilities = nil },
		"database tier":         func(p *Production) { p.DatabaseTier = "" },
		"database storage size": func(p *Production) { p.DatabaseStorage = "" },
		"registry storage size": func(p *Production) { p.RegistryStorage = "" },
		"installation record":   func(p *Production) { p.InstallationRecord = "" },
	} {
		profile := productionProfile()
		mutate(profile.Production)
		_, err := Render(profile)
		require.ErrorContains(t, err, field, field)
	}
}

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
			RegistryDomain:     "registry.example.com",
			TokenKeyPEM:        "-----BEGIN EC PRIVATE KEY-----\nfake\n-----END EC PRIVATE KEY-----\n",
			TokenCertPEM:       "-----BEGIN CERTIFICATE-----\nfake\n-----END CERTIFICATE-----\n",
			NodePullSecret:     "node-pull-secret",
			ACMEEmail:          "ops@example.com",
			Capabilities:       layout.Capabilities,
			DatabaseTier:       layout.TierSynchronous,
			DatabaseStorage:    "10Gi",
			RegistryStorage:    "20Gi",
			WebImage:           "ghcr.io/hinkolas/skali-web:v2.0.0",
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
	require.Len(t, sources, 11)

	frozen := map[string]int{
		"local-namespace.yaml":    0,
		"local-priority.yaml":     1,
		"local-database.yaml":     4,
		"local-registry.yaml":     5,
		"local-skalid.yaml":       6,
		"local-edge-metrics.yaml": 9,
		"local-bootstrap.yaml":    10,
	}
	for name, index := range frozen {
		path := filepath.Join("testdata", name)
		// Refresh deliberately with UPDATE_GOLDEN=1 go test ./internal/bundle;
		// remember the refreshed bundle rolls a converge on every dev machine.
		if os.Getenv("UPDATE_GOLDEN") != "" {
			require.NoError(t, os.WriteFile(path, []byte(sources[index]), 0o644))
		}
		golden, err := os.ReadFile(path)
		require.NoError(t, err, name)
		require.Equal(t, string(golden), sources[index], name)
	}
	// Production-only stages contribute zero bytes locally.
	require.Empty(t, sources[2], "issuer stage must be empty locally")
	require.Empty(t, sources[3], "edge stage must be empty locally")
	require.Empty(t, sources[7], "record stage must be empty locally")
	require.Empty(t, sources[8], "web stage must be empty locally")

	// The full hash including the vendored operator manifests is frozen
	// too: cert-manager must not leak into the local fingerprint.
	hashPath := filepath.Join("testdata", "local-hash.txt")
	if os.Getenv("UPDATE_GOLDEN") != "" {
		require.NoError(t, os.WriteFile(hashPath, []byte(Hash(profile)+"\n"), 0o644))
	}
	hashGolden, err := os.ReadFile(hashPath)
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
		path := filepath.Join("testdata", golden)
		if os.Getenv("UPDATE_GOLDEN") != "" {
			require.NoError(t, os.WriteFile(path, []byte(source), 0o644))
		}
		expected, err := os.ReadFile(path)
		require.NoError(t, err, golden)
		require.Equal(t, string(expected), source, golden)
	}
}

// The PriorityClasses are API once pods name them: names, order, and the
// immutable values are pinned; none is the global default.
func TestPriorityClasses(t *testing.T) {
	t.Parallel()
	objects, err := Render(localProfile())
	require.NoError(t, err)
	require.Len(t, objects.Priority, 3)
	values := map[string]int64{}
	for _, object := range objects.Priority {
		require.Equal(t, "PriorityClass", object.GetKind())
		require.Equal(t, "scheduling.k8s.io/v1", object.GetAPIVersion())
		require.Empty(t, object.GetNamespace(), "cluster-scoped")
		// sigs.k8s.io/yaml decodes numbers as float64; the apply marshals
		// them back as plain integers.
		value, found := object.Object["value"].(float64)
		require.True(t, found, object.GetName())
		values[object.GetName()] = int64(value)
		globalDefault, _, _ := unstructured.NestedBool(object.Object, "globalDefault")
		require.False(t, globalDefault, object.GetName())
	}
	require.Equal(t, map[string]int64{
		layout.PriorityClassCritical: layout.PriorityClassCriticalValue,
		layout.PriorityClassHigh:     layout.PriorityClassHighValue,
		layout.PriorityClassNormal:   layout.PriorityClassNormalValue,
	}, values)
	// Every bundle pod template names the critical class.
	for _, stage := range [][]unstructured.Unstructured{objects.Database, objects.Registry, objects.Skalid} {
		for _, object := range stage {
			var class string
			switch object.GetKind() {
			case "Cluster":
				class, _, _ = unstructured.NestedString(object.Object, "spec", "priorityClassName")
			case "Deployment":
				class, _, _ = unstructured.NestedString(object.Object, "spec", "template", "spec", "priorityClassName")
			default:
				continue
			}
			require.Equal(t, layout.PriorityClassCritical, class, object.GetKind()+"/"+object.GetName())
		}
	}
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

	// Edge: the default TLSOption enforces strict SNI so unknown hosts get
	// a refused handshake, not the self-signed default certificate.
	require.Len(t, objects.Edge, 1)
	edge := objects.Edge[0]
	require.Equal(t, "TLSOption", edge.GetKind())
	require.Equal(t, "default", edge.GetName())
	require.Equal(t, Namespace, edge.GetNamespace())
	strict, _, _ := unstructured.NestedBool(edge.Object, "spec", "sniStrict")
	require.True(t, strict)

	// Skalid: capabilities env, TLS ingress on the api domain.
	deployment := objects.Skalid[4]
	require.Equal(t, "Deployment", deployment.GetKind())
	raw, err := deployment.MarshalJSON()
	require.NoError(t, err)
	require.Contains(t, string(raw), `"SKALI_CAPABILITIES"`)
	require.Contains(t, string(raw), "application;database;object-storage;registry;edge")

	// The platform edge: an explicit Certificate for the api domain and a
	// websecure IngressRoute splitting the platform domain by path, /api to
	// the daemon and everything else to the web console (Traefik prioritizes
	// the longer match), plus the plain-HTTP redirect router.
	certificate := objects.Skalid[6]
	require.Equal(t, "Certificate", certificate.GetKind())
	require.Equal(t, "skalid-tls", certificate.GetName())
	certSecret, _, _ := unstructured.NestedString(certificate.Object, "spec", "secretName")
	require.Equal(t, "skalid-tls", certSecret)
	certIssuer, _, _ := unstructured.NestedString(certificate.Object, "spec", "issuerRef", "name")
	require.Equal(t, IssuerName, certIssuer)
	certNames, _, _ := unstructured.NestedStringSlice(certificate.Object, "spec", "dnsNames")
	require.Equal(t, []string{"skali.example.com"}, certNames)

	route := objects.Skalid[7]
	require.Equal(t, "IngressRoute", route.GetKind())
	require.Equal(t, "skalid", route.GetName())
	points, _, _ := unstructured.NestedStringSlice(route.Object, "spec", "entryPoints")
	require.Equal(t, []string{"websecure"}, points)
	routeSecret, _, _ := unstructured.NestedString(route.Object, "spec", "tls", "secretName")
	require.Equal(t, "skalid-tls", routeSecret)
	skalidRoutes, _, _ := unstructured.NestedSlice(route.Object, "spec", "routes")
	require.Len(t, skalidRoutes, 2)
	apiRule := skalidRoutes[0].(map[string]any)
	require.Equal(t, "Host(`skali.example.com`) && PathPrefix(`/api`)", apiRule["match"],
		"/api must be listed first; Traefik prioritizes the longer match")
	require.Equal(t, "skalid", apiRule["services"].([]any)[0].(map[string]any)["name"])
	webRule := skalidRoutes[1].(map[string]any)
	require.Equal(t, "Host(`skali.example.com`) && PathPrefix(`/`)", webRule["match"])
	require.Equal(t, "skali-web", webRule["services"].([]any)[0].(map[string]any)["name"])

	redirect := objects.Skalid[8]
	require.Equal(t, "IngressRoute", redirect.GetKind())
	require.Equal(t, "skalid-http", redirect.GetName())
	redirectPoints, _, _ := unstructured.NestedStringSlice(redirect.Object, "spec", "entryPoints")
	require.Equal(t, []string{"web"}, redirectPoints)
	redirectJSON, err := redirect.MarshalJSON()
	require.NoError(t, err)
	require.Contains(t, string(redirectJSON), `"middlewares":[{"name":"redirect-https"}]`)

	// Registry: token secret first, then the four base objects, the public
	// edge objects (shared redirect Middleware, Certificate, both routers)
	// last.
	require.Len(t, objects.Registry, 9)
	var pvc, registryDeployment, tokenSecret, registryConfig *unstructured.Unstructured
	var registryCertificate, registryRoute, registryRedirect, middleware *unstructured.Unstructured
	for index := range objects.Registry {
		switch objects.Registry[index].GetKind() {
		case "PersistentVolumeClaim":
			pvc = &objects.Registry[index]
		case "Deployment":
			registryDeployment = &objects.Registry[index]
		case "Secret":
			tokenSecret = &objects.Registry[index]
		case "ConfigMap":
			registryConfig = &objects.Registry[index]
		case "Certificate":
			registryCertificate = &objects.Registry[index]
		case "Middleware":
			middleware = &objects.Registry[index]
		case "IngressRoute":
			if objects.Registry[index].GetName() == "skali-registry-http" {
				registryRedirect = &objects.Registry[index]
			} else {
				registryRoute = &objects.Registry[index]
			}
		}
	}
	require.NotNil(t, pvc)
	require.NotNil(t, registryDeployment)
	size, _, _ := unstructured.NestedString(pvc.Object, "spec", "resources", "requests", "storage")
	require.Equal(t, "20Gi", size)
	selector, _, _ := unstructured.NestedStringMap(registryDeployment.Object,
		"spec", "template", "spec", "nodeSelector")
	require.Equal(t, map[string]string{"skali.dev/capability-registry": "true"}, selector)

	// Token auth: the registry config names the realm on the registry
	// domain and trusts the certificate the token secret carries; the
	// deployment mounts exactly that certificate.
	require.NotNil(t, registryConfig)
	configYML, _, _ := unstructured.NestedString(registryConfig.Object, "data", "config.yml")
	require.Contains(t, configYML, "realm: https://registry.example.com/token")
	require.Contains(t, configYML, "service: skali-registry")
	require.Contains(t, configYML, "issuer: skalid")
	require.Contains(t, configYML, "rootcertbundle: /etc/skali/registry-token/cert.pem")
	require.NotNil(t, tokenSecret)
	require.Equal(t, "skali-registry-token", tokenSecret.GetName())
	require.Equal(t, "Secret", objects.Registry[0].GetKind(),
		"the token secret must precede the deployment that mounts it")
	deploymentJSON, err := registryDeployment.MarshalJSON()
	require.NoError(t, err)
	require.Contains(t, string(deploymentJSON), "/etc/skali/registry-token")
	require.Contains(t, string(deploymentJSON), "skali-registry-token")

	// The registry edge binds the registry domain with its own explicit
	// certificate and routes the token realm to skalid; the shared redirect
	// Middleware rides this stage because it converges before skalid's.
	require.NotNil(t, registryCertificate)
	require.Equal(t, "skali-registry-tls", registryCertificate.GetName())
	registryCertNames, _, _ := unstructured.NestedStringSlice(registryCertificate.Object, "spec", "dnsNames")
	require.Equal(t, []string{"registry.example.com"}, registryCertNames)
	require.NotNil(t, registryRoute)
	registrySecret, _, _ := unstructured.NestedString(registryRoute.Object, "spec", "tls", "secretName")
	require.Equal(t, "skali-registry-tls", registrySecret)
	registryRoutes, _, _ := unstructured.NestedSlice(registryRoute.Object, "spec", "routes")
	require.Len(t, registryRoutes, 2)
	tokenRule := registryRoutes[0].(map[string]any)
	require.Equal(t, "Host(`registry.example.com`) && PathPrefix(`/token`)", tokenRule["match"])
	require.Equal(t, "skalid", tokenRule["services"].([]any)[0].(map[string]any)["name"])
	require.NotNil(t, registryRedirect)
	require.NotNil(t, middleware)
	require.Equal(t, "redirect-https", middleware.GetName())

	// Skalid learns the signing key and node secret from the token secret,
	// and hands build clients push refs on the public registry domain.
	require.Contains(t, string(raw), "SKALI_REGISTRY_TOKEN_KEY")
	require.Contains(t, string(raw), "SKALI_REGISTRY_NODE_SECRET")
	require.Contains(t, string(raw), `"SKALI_REGISTRY_PUSH_HOST"`)
	require.Contains(t, string(raw), `"value":"registry.example.com"`)

	// Record: the ConfigMap round-trips the canonical text exactly.
	require.Len(t, objects.Record, 1)
	record := objects.Record[0]
	require.Equal(t, "ConfigMap", record.GetKind())
	require.Equal(t, RecordName, record.GetName())
	text, _, _ := unstructured.NestedString(record.Object, "data", RecordKey)
	require.Equal(t, profile.Production.InstallationRecord, text)

	// Web console: deployment wired to the in-cluster daemon and the public
	// origin, service in front of the SvelteKit port.
	require.Len(t, objects.Web, 2)
	webDeployment := objects.Web[0]
	require.Equal(t, "Deployment", webDeployment.GetKind())
	require.Equal(t, "skali-web", webDeployment.GetName())
	webJSON, err := webDeployment.MarshalJSON()
	require.NoError(t, err)
	require.Contains(t, string(webJSON), `"value":"http://skalid"`)
	require.Contains(t, string(webJSON), `"value":"https://skali.example.com"`)
	require.Contains(t, string(webJSON), `"ADDRESS_HEADER"`)
	require.Contains(t, string(webJSON), `"path":"/healthz"`)
	webService := objects.Web[1]
	require.Equal(t, "Service", webService.GetKind())
	ports, _, _ := unstructured.NestedSlice(webService.Object, "spec", "ports")
	require.Len(t, ports, 1)
	webPort := ports[0].(map[string]any)
	require.EqualValues(t, 80, webPort["port"])
	require.EqualValues(t, 3000, webPort["targetPort"])

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
		"tier":            func(p *Production) { p.DatabaseTier = layout.TierSingle },
		"ingress":         func(p *Production) { p.IngressHost = "other.example.com" },
		"acme":            func(p *Production) { p.ACMEServer = "https://acme-staging-v02.api.letsencrypt.org/directory" },
		"record":          func(p *Production) { p.InstallationRecord = "version: \"1\"\ninstallationId: ffff\n" },
		"registry domain": func(p *Production) { p.RegistryDomain = "other-registry.example.com" },
		"token key":       func(p *Production) { p.TokenKeyPEM = "rotated" },
		"node secret":     func(p *Production) { p.NodePullSecret = "rotated" },
		"web image":       func(p *Production) { p.WebImage = "ghcr.io/hinkolas/skali-web:other" },
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
		"registry domain":       func(p *Production) { p.RegistryDomain = "" },
		"registry token":        func(p *Production) { p.TokenKeyPEM = "" },
		"node pull secret":      func(p *Production) { p.NodePullSecret = "" },
		"acme email":            func(p *Production) { p.ACMEEmail = "" },
		"capabilities":          func(p *Production) { p.Capabilities = nil },
		"database tier":         func(p *Production) { p.DatabaseTier = "" },
		"database storage size": func(p *Production) { p.DatabaseStorage = "" },
		"registry storage size": func(p *Production) { p.RegistryStorage = "" },
		"web image":             func(p *Production) { p.WebImage = "" },
		"installation record":   func(p *Production) { p.InstallationRecord = "" },
	} {
		profile := productionProfile()
		mutate(profile.Production)
		_, err := Render(profile)
		require.ErrorContains(t, err, field, field)
	}
}

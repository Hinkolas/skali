// Package bundle defines the installer-owned skali-system bundle: the
// blessed operators (CNPG; Traefik ships with k3s itself), the bootstrap
// Postgres cluster, the managed registry, and skalid, plus
// the ordered apply engine over server-side apply under the installer
// field manager. The local `skali dev` installation and the R4 production
// installer share these definitions; only the profile differs. skalid
// itself never touches these resources (section 14.5: it cannot reconcile
// or delete what it needs to run).
package bundle

import (
	"bytes"
	"crypto/sha256"
	_ "embed"
	"encoding/base64"
	"encoding/hex"
	"errors"
	"fmt"
	"strings"

	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"sigs.k8s.io/yaml"

	"github.com/Hinkolas/skali/internal/edge"
	"github.com/Hinkolas/skali/internal/layout"
	"github.com/Hinkolas/skali/internal/registrytoken"
)

// Pinned component versions of this bundle release. cert-manager ships in
// the bundle but applies only under a production profile: the local edge
// stays HTTP-only, so TLS issuance (`tls: automatic`) only works on a
// production installation.
const (
	Namespace   = "skali-system"
	CNPGVersion = "1.29.2"
	// CNPGOperatorImage is the operator image the vendored manifest
	// deploys, exported so local dev can pre-pull it into the cluster.
	CNPGOperatorImage = "ghcr.io/cloudnative-pg/cloudnative-pg:" + CNPGVersion
	// BootstrapPostgresImage pins skali-db's postgres image to what the
	// 1.29 operator would default to anyway, frozen against operator
	// default drift and pre-pullable. Never pin below a previously running
	// major: CNPG refuses downgrades.
	BootstrapPostgresImage = "ghcr.io/cloudnative-pg/postgresql:18.4-system-trixie"
	// CertManagerVersion pins the vendored cert-manager release. The asset
	// is embedded even though the local profile never applies it; roughly
	// one megabyte of CLI weight buys one shared bundle package.
	CertManagerVersion = "1.21.1"
	RegistryImage      = "registry:2.8.3"
	// RegistryNodePort is the stable node port the host maps its loopback
	// registry port onto.
	RegistryNodePort = 30500
	// PoolNodePortMin..Max bound the loopback NodePort range the local
	// platform assigns to postgres pools; S3NodePort sits just above it
	// for the dev object store gateway. skali dev maps the identical
	// numbers on 127.0.0.1 at cluster create, so allocation caps at nine
	// live pools with host access. Managed clusters never allocate any of
	// these. Defined here (not in substrate) because the CLI's localdev
	// package must share them without importing the substrate.
	PoolNodePortMin = 30501
	PoolNodePortMax = 30509
	S3NodePort      = 30510
	// RegistryInternalHost names the managed registry in production
	// artifact references. It never resolves in DNS (.internal is reserved
	// for private use): every node's containerd maps it onto the local
	// registry NodePort through registries.yaml, exactly like
	// localhost:5510 locally.
	RegistryInternalHost = "registry.skali.internal"
	// IssuerName is the ClusterIssuer every issued route certificate binds
	// to; the edge package owns the name, re-exported here for the bundle's
	// callers.
	IssuerName = edge.IssuerName
	// ACMEProductionServer is the default ACME directory.
	ACMEProductionServer = "https://acme-v02.api.letsencrypt.org/directory"
	// RecordName and RecordKey locate the in-cluster installation record
	// that skalid imports on first boot (observation only; mutation
	// authority over bootstrap resources stays with the installer).
	RecordName = "skali-installation"
	RecordKey  = "installation.yaml"
	// HashAnnotation carries Hash() of the last fully converged bundle on
	// the skali-system namespace. It is stamped only after a converge
	// proved out end to end, and the plain namespace apply at the start of
	// the next converge clears it, so a half-applied bundle never looks
	// current.
	HashAnnotation = "skali.dev/bundle-hash"
)

// OperatorNamespaces are the namespaces the vendored operator manifests
// create; scoped uninstall removes them last. cert-manager exists only on
// production installations; deleting an absent namespace is a no-op.
var OperatorNamespaces = []string{"cnpg-system", "cert-manager"}

//go:embed assets/cnpg-1.29.2.yaml
var cnpgManifest []byte

// CNPGManifest is the pinned operator install manifest. The upstream
// operator Deployment ships imagePullPolicy Always; the bundle rewrites it
// to IfNotPresent so a pre-pulled, version-pinned operator image never
// re-contacts the registry on pod start (the same image also runs as the
// bootstrap-controller initContainer in every CNPG postgres pod).
func CNPGManifest() []byte {
	return bytes.Replace(cnpgManifest,
		[]byte("imagePullPolicy: Always"),
		[]byte("imagePullPolicy: IfNotPresent"), 1)
}

//go:embed assets/cert-manager-1.21.1.yaml
var certManagerManifest []byte

// CertManagerManifest is the pinned cert-manager install manifest; applied
// only under a production profile.
func CertManagerManifest() []byte { return certManagerManifest }

// Profile parameterizes one installation of the bundle.
type Profile struct {
	// SkalidImage is the control-plane image reference; local dev imports
	// a working-tree build, production installs published bootstrap
	// images.
	SkalidImage string
	// SkalidImageID is the content identity behind SkalidImage, stamped
	// as a pod-template annotation so a rebuilt image rolls the skalid
	// deployment even under an unchanged mutable tag. Empty omits the
	// annotation (immutable production tags roll by reference alone).
	SkalidImageID string
	// AuthSecret keys skalid's at-rest encryption.
	AuthSecret string
	// AdminEmail and AdminPassword bootstrap the first operator user.
	AdminEmail    string
	AdminPassword string
	// RegistryHost names the registry in artifact references as seen by
	// build clients and nodes (localhost:5510 in the local profile,
	// RegistryInternalHost in production).
	RegistryHost string
	// Production selects the production shape of the bundle: cert-manager
	// with the ACME skali issuer, a tier-sized database, capability-pinned
	// placement, a TLS edge, and the in-cluster installation record. Nil
	// renders the local development shape.
	Production *Production
}

// Production parameterizes the production-only parts of the bundle. Every
// field except ACMEServer is required; Render validates before rendering.
type Production struct {
	// IngressHost is the public api/ui domain (endpoints.api in
	// init.yaml); it becomes the skalid ingress host and certificate
	// subject.
	IngressHost string
	// RegistryDomain is the public managed-registry domain
	// (endpoints.registry in init.yaml): the registry ingress host, its
	// certificate subject, and the host of the token realm the registry
	// advertises in its 401 challenge.
	RegistryDomain string
	// S3Domain is the optional public S3 endpoint domain (endpoints.s3 in
	// init.yaml). When set, the substrate publishes bucket endpoints on it
	// and renders the S3 ingress in skali-platform; empty keeps bucket
	// access in-cluster.
	S3Domain string
	// TokenKeyPEM and TokenCertPEM are the registry token signing keypair
	// the installer generated (or reused) at init: skalid signs with the
	// key, the registry trusts the certificate offline.
	TokenKeyPEM  string
	TokenCertPEM string
	// NodePullSecret is the shared credential containerd presents from
	// registries.yaml; skalid grants it pull-only tokens.
	NodePullSecret string
	// ACMEEmail registers the ACME account behind the skali cluster
	// issuer.
	ACMEEmail string
	// ACMEServer overrides the ACME directory URL; empty selects the
	// Let's Encrypt production endpoint. Test installations whose port 80
	// is not publicly reachable point it at the staging endpoint so
	// pending issuance never burns production rate limits.
	ACMEServer string
	// Capabilities is the union of node capabilities in the layout, in
	// display order; rendered semicolon-delimited into SKALI_CAPABILITIES.
	Capabilities []string
	// DatabaseTier sizes the bootstrap CNPG cluster: one instance for
	// single, two for asynchronous, three with quorum replication for
	// synchronous.
	DatabaseTier layout.Tier
	// DatabaseStorage and RegistryStorage size the installer-owned
	// volumes (Kubernetes quantities, for example 10Gi).
	DatabaseStorage string
	RegistryStorage string
	// RegistryNode pins the installer-owned local-path volume after the
	// first reconciled initialization. Empty retains legacy
	// capability-only placement.
	RegistryNode string
	// WebImage is the web console image reference; the console serves the
	// platform domain root while /api routes to skalid. Local dev runs the
	// console from the working tree instead, so the field is
	// production-only.
	WebImage string
	// WebImageID is the content identity behind WebImage, stamped as a
	// pod-template annotation so a re-imported image rolls the deployment
	// under an unchanged mutable tag. Empty omits the annotation.
	WebImageID string
	// InstallationRecord is the canonical YAML of the root-owned
	// installation record; it is published as the skali-installation
	// ConfigMap. The record must never carry credentials, and its
	// canonical form must omit volatile fields: the text is a bundle-hash
	// input, so anything that changes on every write would defeat the
	// unchanged-bundle fast path.
	InstallationRecord string
}

func (p *Production) validate() error {
	switch {
	case p.IngressHost == "":
		return errors.New("bundle: production profile: ingress host is required")
	case p.RegistryDomain == "":
		return errors.New("bundle: production profile: registry domain is required")
	case p.TokenKeyPEM == "" || p.TokenCertPEM == "":
		return errors.New("bundle: production profile: registry token keypair is required")
	case p.NodePullSecret == "":
		return errors.New("bundle: production profile: node pull secret is required")
	case p.ACMEEmail == "":
		return errors.New("bundle: production profile: acme email is required")
	case len(p.Capabilities) == 0:
		return errors.New("bundle: production profile: capabilities are required")
	case p.DatabaseTier == "":
		return errors.New("bundle: production profile: database tier is required")
	case p.DatabaseStorage == "":
		return errors.New("bundle: production profile: database storage size is required")
	case p.RegistryStorage == "":
		return errors.New("bundle: production profile: registry storage size is required")
	case p.WebImage == "":
		return errors.New("bundle: production profile: web image is required")
	case p.InstallationRecord == "":
		return errors.New("bundle: production profile: installation record is required")
	}
	return nil
}

// ingressClassName is the class every bundle route uses: the managed k3s
// edge is always traefik.
func (p *Production) ingressClassName() string {
	return "traefik"
}

// Objects renders the skalid-independent and skalid parts of the bundle
// as ordered stages; every stage must be applied and healthy before the
// next starts.
type Objects struct {
	// Namespace precedes everything.
	Namespace []unstructured.Unstructured
	// Priority is the three PriorityClasses (skali-critical, skali-high,
	// skali-normal); it precedes every pod that references one, which is
	// every pod the bundle and skalid render.
	Priority []unstructured.Unstructured
	// Issuer is the ACME ClusterIssuer named skali (requires
	// cert-manager); empty under the local profile.
	Issuer []unstructured.Unstructured
	// Edge is the strict-SNI TLS policy on the Traefik edge; empty under
	// the local profile.
	Edge []unstructured.Unstructured
	// Database is the CNPG cluster (requires the operator).
	Database []unstructured.Unstructured
	// Registry is the managed OCI registry.
	Registry []unstructured.Unstructured
	// Skalid is the control plane with its RBAC, service, and edge route.
	Skalid []unstructured.Unstructured
	// Record is the in-cluster installation record; empty under the local
	// profile.
	Record []unstructured.Unstructured
	// Web is the web console deployment behind the platform domain root;
	// empty under the local profile.
	Web []unstructured.Unstructured
	// EdgeMetrics opts the k3s Traefik chart into per-router Prometheus
	// series and exposes the metrics port, feeding the skalid edge-traffic
	// sampler. Both profiles.
	EdgeMetrics []unstructured.Unstructured
	// BootstrapUser creates the first operator user.
	BootstrapUser []unstructured.Unstructured
}

// stageSources renders the ordered stage manifests; Render parses them and
// Hash fingerprints them, so the two always agree on the bundle's content.
// Local-only and production-only stages render empty for the other
// profile, contributing zero bytes to the hash.
func stageSources(profile Profile) []string {
	return []string{
		namespaceYAML(),
		priorityYAML(),
		issuerYAML(profile),
		edgeYAML(profile),
		databaseYAML(profile),
		registryYAML(profile),
		skalidYAML(profile),
		recordYAML(profile),
		webYAML(profile),
		edgeMetricsYAML(),
		// Bootstrap must stay last: Hash blanks the final source under
		// production because the generated password never reproduces.
		bootstrapYAML(profile),
	}
}

// Render produces every skali-owned bundle object for the profile.
func Render(profile Profile) (*Objects, error) {
	if profile.Production != nil {
		if err := profile.Production.validate(); err != nil {
			return nil, err
		}
	}
	objects := &Objects{}
	targets := []*[]unstructured.Unstructured{
		&objects.Namespace,
		&objects.Priority,
		&objects.Issuer,
		&objects.Edge,
		&objects.Database,
		&objects.Registry,
		&objects.Skalid,
		&objects.Record,
		&objects.Web,
		&objects.EdgeMetrics,
		&objects.BootstrapUser,
	}
	for index, source := range stageSources(profile) {
		parsed, err := ParseManifest([]byte(source))
		if err != nil {
			return nil, err
		}
		*targets[index] = parsed
	}
	return objects, nil
}

// Hash fingerprints everything a profile's converge would apply, the
// vendored operator manifests included: it changes exactly when a full
// converge could change the cluster. Production excludes the
// bootstrap-user stage: the admin password is never persisted host-side,
// so a repeat run could not reproduce it, and the account is created by a
// separate one-shot step outside the converge anyway. The local profile
// keeps it (localdev records the credentials, and its converge applies the
// stage).
func Hash(profile Profile) string {
	digest := sha256.New()
	// The patched manifest, exactly what ApplyManifest applies: a rewrite
	// there must move the hash and yield a converge.
	digest.Write(CNPGManifest())
	sources := stageSources(profile)
	if profile.Production != nil {
		digest.Write(certManagerManifest)
		sources[len(sources)-1] = ""
	}
	for _, source := range sources {
		digest.Write([]byte(source))
	}
	return hex.EncodeToString(digest.Sum(nil))
}

// ParseManifest splits one multi-document YAML manifest into unstructured
// objects, dropping empty documents.
func ParseManifest(manifest []byte) ([]unstructured.Unstructured, error) {
	var objects []unstructured.Unstructured
	for document := range strings.SplitSeq(string(manifest), "\n---") {
		document = strings.TrimSpace(document)
		if document == "" || strings.HasPrefix(document, "#") && !strings.Contains(document, "\n") {
			continue
		}
		var object map[string]any
		if err := yaml.Unmarshal([]byte(document), &object); err != nil {
			return nil, fmt.Errorf("bundle: parse manifest document: %w", err)
		}
		if len(object) == 0 {
			continue
		}
		objects = append(objects, unstructured.Unstructured{Object: object})
	}
	return objects, nil
}

func namespaceYAML() string {
	return `apiVersion: v1
kind: Namespace
metadata:
  name: ` + Namespace + `
  labels:
    skali.dev/system: "true"
`
}

// priorityYAML renders the PriorityClasses. Values are immutable once the
// classes exist (a change means delete and recreate during converge), so
// they are final; none is the global default, every skali-rendered pod
// names its class explicitly.
func priorityYAML() string {
	return fmt.Sprintf(`apiVersion: scheduling.k8s.io/v1
kind: PriorityClass
metadata:
  name: %[1]s
value: %[2]d
globalDefault: false
description: Skali itself (skalid, registry, console, object storage, managed databases); preempts application pods.
---
apiVersion: scheduling.k8s.io/v1
kind: PriorityClass
metadata:
  name: %[3]s
value: %[4]d
globalDefault: false
description: Applications of high priority environments; preempt normal ones when resources are tight.
---
apiVersion: scheduling.k8s.io/v1
kind: PriorityClass
metadata:
  name: %[5]s
value: %[6]d
globalDefault: false
description: Applications of normal priority environments; yield to high priority ones.
`, layout.PriorityClassCritical, layout.PriorityClassCriticalValue,
		layout.PriorityClassHigh, layout.PriorityClassHighValue,
		layout.PriorityClassNormal, layout.PriorityClassNormalValue)
}

// issuerYAML renders the ACME ClusterIssuer every `tls: automatic` route
// binds to. Production only: the local edge is HTTP-only.
func issuerYAML(profile Profile) string {
	if profile.Production == nil {
		return ""
	}
	server := profile.Production.ACMEServer
	if server == "" {
		server = ACMEProductionServer
	}
	return fmt.Sprintf(`apiVersion: cert-manager.io/v1
kind: ClusterIssuer
metadata:
  name: %[1]s
spec:
  acme:
    email: %[2]s
    server: %[3]s
    privateKeySecretRef:
      name: skali-acme-account
    solvers:
      - http01:
          ingress:
            ingressClassName: %[4]s
`, IssuerName, profile.Production.ACMEEmail, server, profile.Production.ingressClassName())
}

// edgeYAML renders the edge-wide TLS policy: with strict SNI, Traefik
// refuses the TLS handshake for any hostname it holds no certificate for
// (wildcard DNS pointing spare subdomains at the cluster gets a closed
// connection, not the self-signed default certificate, which HSTS-preloaded
// TLDs like .dev escalate into a non-bypassable browser error). The object
// must be named "default" to bind as the entrypoint default, and Traefik
// tolerates only one such object cluster-wide; it lives in skali-system so
// uninstall removes it with the namespace. Production only: the local edge
// is HTTP-only.
func edgeYAML(profile Profile) string {
	if profile.Production == nil {
		return ""
	}
	return `apiVersion: traefik.io/v1alpha1
kind: TLSOption
metadata:
  name: default
  namespace: ` + Namespace + `
spec:
  sniStrict: true
`
}

// edgeMetricsYAML overlays the k3s Traefik chart (helm-controller merges a
// HelmChartConfig into the HelmChart's values): per-router Prometheus labels
// on the metrics endpoint the chart already runs on :9100, plus that port on
// the traefik Service so skalid can scrape it through the API server's
// service proxy. Applying a changed overlay rolls the Traefik pod once.
// skali owns this object; operator-authored traefik HelmChartConfigs are
// unsupported. Both profiles: the local k3d edge is the same Traefik.
func edgeMetricsYAML() string {
	return `apiVersion: helm.cattle.io/v1
kind: HelmChartConfig
metadata:
  name: traefik
  namespace: kube-system
spec:
  valuesContent: |-
    metrics:
      prometheus:
        addRoutersLabels: true
    ports:
      metrics:
        expose:
          default: true
`
}

func databaseYAML(profile Profile) string {
	instances := 1
	storage := "1Gi"
	affinity := ""
	synchronous := ""
	if production := profile.Production; production != nil {
		instances = layout.TierInstances(production.DatabaseTier)
		storage = production.DatabaseStorage
		// The affinity block renders only in production: local k3d nodes
		// carry no capability labels and would strand the pod Pending.
		affinity = "\n  affinity:\n    nodeSelector:\n      " +
			layout.CapabilityLabel(layout.CapabilityDatabase) + `: "true"`
		if production.DatabaseTier == layout.TierSynchronous {
			// Three instances leave two standbys; transactions wait for
			// any one of them (quorum "any 1 of 2").
			synchronous = "\n  postgresql:\n    synchronous:\n      method: any\n      number: 1"
		}
	}
	return fmt.Sprintf(`apiVersion: postgresql.cnpg.io/v1
kind: Cluster
metadata:
  name: skali-db
  namespace: %[1]s
spec:
  imageName: %[6]s
  instances: %[2]d
  priorityClassName: %[7]s
  storage:
    size: %[3]s%[4]s%[5]s
  bootstrap:
    initdb:
      database: skali
      owner: skali
`, Namespace, instances, storage, affinity, synchronous, BootstrapPostgresImage, layout.PriorityClassCritical)
}

// recordYAML publishes the canonical installation record for skalid to
// import on first boot. Production only.
func recordYAML(profile Profile) string {
	if profile.Production == nil {
		return ""
	}
	object := map[string]any{
		"apiVersion": "v1",
		"kind":       "ConfigMap",
		"metadata": map[string]any{
			"name":      RecordName,
			"namespace": Namespace,
			"labels":    map[string]any{"skali.dev/system": "true"},
		},
		"data": map[string]any{RecordKey: profile.Production.InstallationRecord},
	}
	// Marshal (via JSON) sorts keys, so the output is deterministic; a
	// map of strings cannot fail to encode.
	data, _ := yaml.Marshal(object)
	return string(data)
}

func registryYAML(profile Profile) string {
	storage := "5Gi"
	nodeSelector := ""
	authConfig := ""
	tokenPrefix := ""
	ingressSuffix := ""
	certMount := ""
	certVolume := ""
	if production := profile.Production; production != nil {
		storage = production.RegistryStorage
		// Pin the single registry instance to a registry-capable node;
		// the local-path volume provisions on first consumption, so pod
		// and volume agree on the node.
		nodeSelector = "\n      nodeSelector:\n        " +
			layout.CapabilityLabel(layout.CapabilityRegistry) + `: "true"`
		if production.RegistryNode != "" {
			nodeSelector += "\n        kubernetes.io/hostname: " + production.RegistryNode
		}
		// Production requires the registry token protocol: the 401
		// challenge points clients at the token realm on the registry
		// domain, and the registry verifies minted tokens offline against
		// the signing certificate.
		authConfig = "\n    auth:\n      token:\n        realm: https://" +
			production.RegistryDomain + "/token" +
			"\n        service: " + registrytoken.Service +
			"\n        issuer: " + registrytoken.Issuer +
			"\n        rootcertbundle: /etc/skali/registry-token/cert.pem"
		certMount = "\n            - name: token-cert\n              mountPath: /etc/skali/registry-token"
		certVolume = "\n        - name: token-cert\n          secret:\n            secretName: skali-registry-token" +
			"\n            items:\n              - key: cert.pem\n                path: cert.pem"
		tokenPrefix = registryTokenSecretYAML(production)
		ingressSuffix = registryIngressYAML(production)
	}
	return tokenPrefix + fmt.Sprintf(`apiVersion: v1
kind: ConfigMap
metadata:
  name: skali-registry-config
  namespace: %[1]s
data:
  config.yml: |
    version: 0.1
    storage:
      filesystem:
        rootdirectory: /var/lib/registry
      delete:
        enabled: true
    http:
      addr: :5000%[6]s
---
apiVersion: v1
kind: PersistentVolumeClaim
metadata:
  name: skali-registry-data
  namespace: %[1]s
spec:
  accessModes: [ReadWriteOnce]
  resources:
    requests:
      storage: %[4]s
---
apiVersion: apps/v1
kind: Deployment
metadata:
  name: skali-registry
  namespace: %[1]s
spec:
  replicas: 1
  strategy:
    type: Recreate
  selector:
    matchLabels:
      app.kubernetes.io/name: skali-registry
  template:
    metadata:
      labels:
        app.kubernetes.io/name: skali-registry
    spec:%[5]s
      priorityClassName: %[9]s
      containers:
        - name: registry
          image: %[2]s
          ports:
            - containerPort: 5000
          volumeMounts:
            - name: data
              mountPath: /var/lib/registry
            - name: config
              mountPath: /etc/docker/registry%[7]s
      volumes:
        - name: data
          persistentVolumeClaim:
            claimName: skali-registry-data
        - name: config
          configMap:
            name: skali-registry-config%[8]s
---
apiVersion: v1
kind: Service
metadata:
  name: skali-registry
  namespace: %[1]s
spec:
  type: NodePort
  selector:
    app.kubernetes.io/name: skali-registry
  ports:
    - port: 5000
      targetPort: 5000
      nodePort: %[3]d
`, Namespace, RegistryImage, RegistryNodePort, storage, nodeSelector,
		authConfig, certMount, certVolume, layout.PriorityClassCritical) + ingressSuffix
}

// registryTokenSecretYAML renders the token trust material: skalid reads
// the signing key and the node pull secret, the registry mounts only the
// certificate. It precedes the registry Deployment in the stage so the pod
// never waits on a missing mount.
func registryTokenSecretYAML(production *Production) string {
	return fmt.Sprintf(`apiVersion: v1
kind: Secret
metadata:
  name: skali-registry-token
  namespace: %[1]s
type: Opaque
data:
  key.pem: %[2]s
  cert.pem: %[3]s
  node-secret: %[4]s
---
`, Namespace,
		base64.StdEncoding.EncodeToString([]byte(production.TokenKeyPEM)),
		base64.StdEncoding.EncodeToString([]byte(production.TokenCertPEM)),
		base64.StdEncoding.EncodeToString([]byte(production.NodePullSecret)))
}

// registryIngressYAML publishes the registry on its own domain through the
// Traefik edge. The /token route targets skalid (the realm must be
// reachable by exactly the clients that can reach the registry); Traefik
// prioritizes the longer match. Plain HTTP redirects through the shared
// skali-system Middleware the skalid stage renders, and the certificate is
// explicit like every managed route's.
func registryIngressYAML(production *Production) string {
	return fmt.Sprintf(`---
apiVersion: traefik.io/v1alpha1
kind: Middleware
metadata:
  name: redirect-https
  namespace: %[1]s
spec:
  redirectScheme:
    scheme: https
    permanent: true
---
apiVersion: cert-manager.io/v1
kind: Certificate
metadata:
  name: skali-registry-tls
  namespace: %[1]s
spec:
  secretName: skali-registry-tls
  dnsNames:
    - %[3]s
  issuerRef:
    kind: ClusterIssuer
    name: %[2]s
    group: cert-manager.io
---
apiVersion: traefik.io/v1alpha1
kind: IngressRoute
metadata:
  name: skali-registry
  namespace: %[1]s
spec:
  entryPoints:
    - websecure
  routes:
    - match: Host(`+"`%[3]s`"+`) && PathPrefix(`+"`/token`"+`)
      kind: Rule
      services:
        - name: skalid
          port: 80
    - match: Host(`+"`%[3]s`"+`) && PathPrefix(`+"`/`"+`)
      kind: Rule
      services:
        - name: skali-registry
          port: 5000
  tls:
    secretName: skali-registry-tls
---
apiVersion: traefik.io/v1alpha1
kind: IngressRoute
metadata:
  name: skali-registry-http
  namespace: %[1]s
spec:
  entryPoints:
    - web
  routes:
    - match: Host(`+"`%[3]s`"+`) && PathPrefix(`+"`/`"+`)
      kind: Rule
      middlewares:
        - name: redirect-https
      services:
        - name: skali-registry
          port: 5000
`, Namespace, IssuerName, production.RegistryDomain)
}

func skalidYAML(profile Profile) string {
	// Pod-template annotations force a roll on changes the spec cannot
	// see: a re-imported image under the same tag, and the secret-backed
	// env (secretKeyRef values resolve at container start, so a rotated
	// token key or node pull secret would otherwise stay stale in the
	// running pod). The checksum is a truncated one-way hash; it reveals
	// nothing about the material.
	var annotationLines []string
	if profile.SkalidImageID != "" {
		annotationLines = append(annotationLines, "skali.dev/image-id: "+profile.SkalidImageID)
	}
	if production := profile.Production; production != nil {
		sum := sha256.Sum256([]byte(production.TokenKeyPEM + "\x00" + production.NodePullSecret))
		annotationLines = append(annotationLines,
			"skali.dev/registry-token-checksum: "+hex.EncodeToString(sum[:8]))
	}
	podAnnotations := ""
	if len(annotationLines) > 0 {
		podAnnotations = "\n      annotations:"
		for _, line := range annotationLines {
			podAnnotations += "\n        " + line
		}
	}
	// Both profiles state the installation's capabilities explicitly. Local
	// dev is one node carrying every service capability: the substrate
	// collapses every database claim onto the single dev pool and every
	// bucket onto the single all-in-one dev object store, so the
	// capabilities are always present.
	capabilitiesEnv := "\n            - name: SKALI_CAPABILITIES\n              value: application;edge;database;object-storage"
	// Locally the daemon owns the whole host on the plain web entrypoint;
	// production splits the platform domain on websecure, /api to skalid
	// (which also answers root paths for in-cluster clients) and the rest
	// to the web console (Traefik prioritizes the longer match), with a
	// shared redirect Middleware answering plain HTTP and an explicit
	// Certificate for the platform domain.
	edgeSuffix := fmt.Sprintf(`---
apiVersion: traefik.io/v1alpha1
kind: IngressRoute
metadata:
  name: skalid
  namespace: %[1]s
spec:
  entryPoints:
    - web
  routes:
    - match: Host(`+"`skali.localhost`"+`) && PathPrefix(`+"`/`"+`)
      kind: Rule
      services:
        - name: skalid
          port: 80
`, Namespace)
	if production := profile.Production; production != nil {
		// Production states the installation's capability union. The token
		// signing key and node pull secret ride the same production block:
		// with them set, skalid serves the registry token realm. The push
		// host is the public registry domain: build clients push through the
		// edge while artifact references stay on the internal name.
		capabilitiesEnv = "\n            - name: SKALI_CAPABILITIES\n              value: " +
			strings.Join(production.Capabilities, ";") +
			"\n            - name: SKALI_REGISTRY_PUSH_HOST\n              value: " + production.RegistryDomain +
			"\n            - name: SKALI_REGISTRY_TOKEN_KEY\n              valueFrom:\n                secretKeyRef:\n                  name: skali-registry-token\n                  key: key.pem" +
			"\n            - name: SKALI_REGISTRY_NODE_SECRET\n              valueFrom:\n                secretKeyRef:\n                  name: skali-registry-token\n                  key: node-secret"
		if production.S3Domain != "" {
			capabilitiesEnv += "\n            - name: SKALI_S3_DOMAIN\n              value: " + production.S3Domain
		}
		capabilitiesEnv += "\n            - name: SKALI_MANAGED_CLUSTER\n              value: \"true\"" +
			"\n            - name: SKALI_CERT_MANAGER\n              value: \"true\""
		// The shared redirect-https Middleware rides the registry stage,
		// which converges first; both platform -http routers reference it.
		edgeSuffix = fmt.Sprintf(`---
apiVersion: cert-manager.io/v1
kind: Certificate
metadata:
  name: skalid-tls
  namespace: %[1]s
spec:
  secretName: skalid-tls
  dnsNames:
    - %[2]s
  issuerRef:
    kind: ClusterIssuer
    name: %[3]s
    group: cert-manager.io
---
apiVersion: traefik.io/v1alpha1
kind: IngressRoute
metadata:
  name: skalid
  namespace: %[1]s
spec:
  entryPoints:
    - websecure
  routes:
    - match: Host(`+"`%[2]s`"+`) && PathPrefix(`+"`/api`"+`)
      kind: Rule
      services:
        - name: skalid
          port: 80
    - match: Host(`+"`%[2]s`"+`) && PathPrefix(`+"`/`"+`)
      kind: Rule
      services:
        - name: skali-web
          port: 80
  tls:
    secretName: skalid-tls
---
apiVersion: traefik.io/v1alpha1
kind: IngressRoute
metadata:
  name: skalid-http
  namespace: %[1]s
spec:
  entryPoints:
    - web
  routes:
    - match: Host(`+"`%[2]s`"+`) && PathPrefix(`+"`/`"+`)
      kind: Rule
      middlewares:
        - name: redirect-https
      services:
        - name: skalid
          port: 80
`, Namespace, production.IngressHost, IssuerName)
	}
	return fmt.Sprintf(`apiVersion: v1
kind: ServiceAccount
metadata:
  name: skalid
  namespace: %[1]s
---
apiVersion: rbac.authorization.k8s.io/v1
kind: ClusterRole
metadata:
  name: skalid
rules:
  - apiGroups: [""]
    resources: [namespaces, secrets, configmaps, services, services/proxy, pods, pods/log, pods/exec, events, persistentvolumeclaims, nodes]
    verbs: ["*"]
  - apiGroups: [apps]
    resources: [deployments, statefulsets, daemonsets]
    verbs: ["*"]
  - apiGroups: [networking.k8s.io]
    resources: [ingresses, networkpolicies]
    verbs: ["*"]
  - apiGroups: [traefik.io]
    resources: [ingressroutes, middlewares]
    verbs: ["*"]
  - apiGroups: [cert-manager.io]
    resources: [certificates]
    verbs: ["*"]
  - apiGroups: [discovery.k8s.io]
    resources: [endpointslices]
    verbs: ["*"]
  - apiGroups: [autoscaling]
    resources: [horizontalpodautoscalers]
    verbs: ["*"]
  - apiGroups: [batch]
    resources: [jobs]
    verbs: ["*"]
  - apiGroups: [postgresql.cnpg.io]
    resources: [clusters, databases]
    verbs: ["*"]
  - apiGroups: [metrics.k8s.io]
    resources: [nodes, pods]
    verbs: [get, list]
---
apiVersion: rbac.authorization.k8s.io/v1
kind: ClusterRoleBinding
metadata:
  name: skalid
roleRef:
  apiGroup: rbac.authorization.k8s.io
  kind: ClusterRole
  name: skalid
subjects:
  - kind: ServiceAccount
    name: skalid
    namespace: %[1]s
---
apiVersion: v1
kind: Secret
metadata:
  name: skali-auth
  namespace: %[1]s
type: Opaque
data:
  AUTH_SECRET: %[3]s
---
apiVersion: apps/v1
kind: Deployment
metadata:
  name: skalid
  namespace: %[1]s
spec:
  replicas: 1
  selector:
    matchLabels:
      app.kubernetes.io/name: skalid
  template:
    metadata:
      labels:
        app.kubernetes.io/name: skalid%[5]s
    spec:
      serviceAccountName: skalid
      priorityClassName: `+layout.PriorityClassCritical+`
      topologySpreadConstraints:
        - maxSkew: 1
          topologyKey: kubernetes.io/hostname
          whenUnsatisfiable: ScheduleAnyway
          labelSelector:
            matchLabels:
              app.kubernetes.io/name: skalid
      initContainers:
        - name: migrate
          image: %[2]s
          imagePullPolicy: IfNotPresent
          args: [migrate, up]
          env:
            - name: DATABASE_URL
              valueFrom:
                secretKeyRef:
                  name: skali-db-app
                  key: uri
      containers:
        - name: skalid
          image: %[2]s
          imagePullPolicy: IfNotPresent
          ports:
            - containerPort: 7070
          env:
            - name: DATABASE_URL
              valueFrom:
                secretKeyRef:
                  name: skali-db-app
                  key: uri
            - name: AUTH_SECRET
              valueFrom:
                secretKeyRef:
                  name: skali-auth
                  key: AUTH_SECRET
            - name: SKALI_REGISTRY_HOST
              value: %[4]s
            - name: SKALI_REGISTRY_ENDPOINT
              value: skali-registry.%[1]s.svc:5000
            - name: SKALI_REGISTRY_INSECURE
              value: "true"%[6]s
          readinessProbe:
            httpGet:
              path: /healthz
              port: 7070
            initialDelaySeconds: 2
            periodSeconds: 3
---
apiVersion: v1
kind: Service
metadata:
  name: skalid
  namespace: %[1]s
spec:
  selector:
    app.kubernetes.io/name: skalid
  ports:
    - port: 80
      targetPort: 7070
`, Namespace, profile.SkalidImage, base64.StdEncoding.EncodeToString([]byte(profile.AuthSecret)), profile.RegistryHost,
		podAnnotations, capabilitiesEnv) + edgeSuffix
}

// webYAML renders the web console: the SvelteKit BFF that serves the
// platform domain root while the skalid ingress path routes /api to the
// daemon. ORIGIN pins SvelteKit's form-action origin check to the public
// domain, and ADDRESS_HEADER makes getClientAddress read the client IP the
// Traefik edge forwards. Local dev runs the console from the working tree,
// so the stage renders empty there.
func webYAML(profile Profile) string {
	production := profile.Production
	if production == nil {
		return ""
	}
	podAnnotations := ""
	if production.WebImageID != "" {
		podAnnotations = "\n      annotations:\n        skali.dev/image-id: " + production.WebImageID
	}
	return fmt.Sprintf(`apiVersion: apps/v1
kind: Deployment
metadata:
  name: skali-web
  namespace: %[1]s
spec:
  replicas: 1
  selector:
    matchLabels:
      app.kubernetes.io/name: skali-web
  template:
    metadata:
      labels:
        app.kubernetes.io/name: skali-web%[3]s
    spec:
      priorityClassName: `+layout.PriorityClassCritical+`
      containers:
        - name: skali-web
          image: %[2]s
          imagePullPolicy: IfNotPresent
          ports:
            - containerPort: 3000
          env:
            - name: PORT
              value: "3000"
            - name: API_URL
              value: http://skalid
            - name: ORIGIN
              value: https://%[4]s
            - name: ADDRESS_HEADER
              value: x-forwarded-for
          readinessProbe:
            httpGet:
              path: /healthz
              port: 3000
            initialDelaySeconds: 2
            periodSeconds: 3
---
apiVersion: v1
kind: Service
metadata:
  name: skali-web
  namespace: %[1]s
spec:
  selector:
    app.kubernetes.io/name: skali-web
  ports:
    - port: 80
      targetPort: 3000
`, Namespace, production.WebImage, podAnnotations, production.IngressHost)
}

func bootstrapYAML(profile Profile) string {
	return fmt.Sprintf(`apiVersion: v1
kind: Secret
metadata:
  name: skali-bootstrap
  namespace: %[1]s
type: Opaque
data:
  ADMIN_EMAIL: %[3]s
  ADMIN_PASSWORD: %[4]s
---
apiVersion: batch/v1
kind: Job
metadata:
  name: skali-bootstrap-user
  namespace: %[1]s
spec:
  backoffLimit: 4
  template:
    spec:
      restartPolicy: OnFailure
      containers:
        - name: bootstrap
          image: %[2]s
          imagePullPolicy: IfNotPresent
          command:
            - sh
            - -c
            - >
              skalid user list 2>/dev/null | grep -q "$ADMIN_EMAIL" && exit 0;
              printf '%%s' "$ADMIN_PASSWORD" |
              skalid user create --email "$ADMIN_EMAIL" --role admin --password-stdin
          env:
            - name: DATABASE_URL
              valueFrom:
                secretKeyRef:
                  name: skali-db-app
                  key: uri
            - name: ADMIN_EMAIL
              valueFrom:
                secretKeyRef:
                  name: skali-bootstrap
                  key: ADMIN_EMAIL
            - name: ADMIN_PASSWORD
              valueFrom:
                secretKeyRef:
                  name: skali-bootstrap
                  key: ADMIN_PASSWORD
`, Namespace, profile.SkalidImage,
		base64.StdEncoding.EncodeToString([]byte(profile.AdminEmail)),
		base64.StdEncoding.EncodeToString([]byte(profile.AdminPassword)))
}

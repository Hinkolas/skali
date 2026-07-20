// Package bundle defines the installer-owned skali-system bundle: the
// blessed operators (CNPG, cert-manager; Traefik ships with k3s itself),
// the bootstrap Postgres cluster, the managed registry, and skalid, plus
// the ordered apply engine over server-side apply under the installer
// field manager. The local `skali dev` installation and the R4 production
// installer share these definitions; only the profile differs. skalid
// itself never touches these resources (section 14.5: it cannot reconcile
// or delete what it needs to run).
package bundle

import (
	_ "embed"
	"encoding/base64"
	"fmt"
	"strings"

	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"sigs.k8s.io/yaml"
)

// Pinned component versions of this bundle release.
const (
	Namespace          = "skali-system"
	CNPGVersion        = "1.25.1"
	CertManagerVersion = "1.16.3"
	RegistryImage      = "registry:2.8.3"
	// RegistryNodePort is the stable node port the host maps its loopback
	// registry port onto.
	RegistryNodePort = 30500
)

//go:embed assets/cnpg-1.25.1.yaml
var cnpgManifest []byte

//go:embed assets/cert-manager-1.16.3.yaml
var certManagerManifest []byte

// CNPGManifest is the pinned operator install manifest.
func CNPGManifest() []byte { return cnpgManifest }

// CertManagerManifest is the pinned operator install manifest.
func CertManagerManifest() []byte { return certManagerManifest }

// Profile parameterizes one installation of the bundle.
type Profile struct {
	// SkalidImage is the control-plane image reference; local dev imports
	// a working-tree build, production installs published bootstrap
	// images.
	SkalidImage string
	// AuthSecret keys skalid's at-rest encryption.
	AuthSecret string
	// AdminEmail and AdminPassword bootstrap the first operator user.
	AdminEmail    string
	AdminPassword string
	// RegistryHost names the registry in artifact references as seen by
	// build clients and nodes (localhost:5510 in the local profile).
	RegistryHost string
}

// Objects renders the skalid-independent and skalid parts of the bundle
// as ordered stages; every stage must be applied and healthy before the
// next starts.
type Objects struct {
	// Namespace precedes everything.
	Namespace []unstructured.Unstructured
	// Database is the CNPG cluster (requires the operator).
	Database []unstructured.Unstructured
	// Registry is the managed OCI registry.
	Registry []unstructured.Unstructured
	// Issuer is the self-signed ClusterIssuer named skali (requires
	// cert-manager) that keeps `tls: automatic` routes valid locally.
	Issuer []unstructured.Unstructured
	// Skalid is the control plane with its RBAC, service, and edge route.
	Skalid []unstructured.Unstructured
	// BootstrapUser creates the first operator user.
	BootstrapUser []unstructured.Unstructured
}

// Render produces every skali-owned bundle object for the profile.
func Render(profile Profile) (*Objects, error) {
	objects := &Objects{}
	stages := []struct {
		target *[]unstructured.Unstructured
		yaml   string
	}{
		{&objects.Namespace, namespaceYAML()},
		{&objects.Database, databaseYAML()},
		{&objects.Registry, registryYAML()},
		{&objects.Issuer, issuerYAML()},
		{&objects.Skalid, skalidYAML(profile)},
		{&objects.BootstrapUser, bootstrapYAML(profile)},
	}
	for _, stage := range stages {
		parsed, err := ParseManifest([]byte(stage.yaml))
		if err != nil {
			return nil, err
		}
		*stage.target = parsed
	}
	return objects, nil
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

func databaseYAML() string {
	return `apiVersion: postgresql.cnpg.io/v1
kind: Cluster
metadata:
  name: skali-db
  namespace: ` + Namespace + `
spec:
  instances: 1
  storage:
    size: 1Gi
  bootstrap:
    initdb:
      database: skali
      owner: skali
`
}

func registryYAML() string {
	return fmt.Sprintf(`apiVersion: v1
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
      addr: :5000
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
      storage: 5Gi
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
    spec:
      containers:
        - name: registry
          image: %[2]s
          ports:
            - containerPort: 5000
          volumeMounts:
            - name: data
              mountPath: /var/lib/registry
            - name: config
              mountPath: /etc/docker/registry
      volumes:
        - name: data
          persistentVolumeClaim:
            claimName: skali-registry-data
        - name: config
          configMap:
            name: skali-registry-config
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
`, Namespace, RegistryImage, RegistryNodePort)
}

func issuerYAML() string {
	return `apiVersion: cert-manager.io/v1
kind: ClusterIssuer
metadata:
  name: skali
spec:
  selfSigned: {}
`
}

func skalidYAML(profile Profile) string {
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
    resources: [namespaces, secrets, services, pods, pods/log, events, persistentvolumeclaims, nodes]
    verbs: ["*"]
  - apiGroups: [apps]
    resources: [deployments]
    verbs: ["*"]
  - apiGroups: [networking.k8s.io]
    resources: [ingresses]
    verbs: ["*"]
  - apiGroups: [autoscaling]
    resources: [horizontalpodautoscalers]
    verbs: ["*"]
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
        app.kubernetes.io/name: skalid
    spec:
      serviceAccountName: skalid
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
              value: "true"
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
---
apiVersion: networking.k8s.io/v1
kind: Ingress
metadata:
  name: skalid
  namespace: %[1]s
spec:
  ingressClassName: traefik
  rules:
    - host: skali.localhost
      http:
        paths:
          - path: /
            pathType: Prefix
            backend:
              service:
                name: skalid
                port:
                  number: 80
`, Namespace, profile.SkalidImage, base64.StdEncoding.EncodeToString([]byte(profile.AuthSecret)), profile.RegistryHost)
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

package installer

import (
	"context"
	"errors"
	"fmt"
	"strings"

	"github.com/google/uuid"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"

	"github.com/Hinkolas/skali/internal/bundle"
	"github.com/Hinkolas/skali/internal/kube"
	"github.com/Hinkolas/skali/internal/layout"
	"github.com/Hinkolas/skali/internal/version"
)

// MinExistingKubernetesMinor is the minimum Kubernetes minor version an
// existing cluster must run; the bundle assumes reasonably recent CNPG and
// networking APIs.
const MinExistingKubernetesMinor = 30

// ClusterState is what DetectCluster found in the target cluster.
type ClusterState string

const (
	// ClusterFresh has no skali-system namespace or no record.
	ClusterFresh ClusterState = "fresh"
	// ClusterInstalled carries an existing-cluster record.
	ClusterInstalled ClusterState = "installed"
	// ClusterManaged carries a managed record: skali owns this cluster's
	// hosts, so existing-cluster operations are refused.
	ClusterManaged ClusterState = "managed"
	// ClusterDamaged carries an unparseable record.
	ClusterDamaged ClusterState = "damaged"
)

// ClusterDetection is the read-only detection result.
type ClusterDetection struct {
	State  ClusterState
	Record *Record
	// Problem describes a damaged record.
	Problem string
}

// DetectCluster reads the target cluster's installation record. It never
// mutates and never consults host state.
func DetectCluster(ctx context.Context, client *kube.Client) (*ClusterDetection, error) {
	if _, err := client.Clientset.CoreV1().Namespaces().Get(ctx, bundle.Namespace, metav1.GetOptions{}); err != nil {
		if apierrors.IsNotFound(err) {
			return &ClusterDetection{State: ClusterFresh}, nil
		}
		return nil, fmt.Errorf("read %s namespace: %w", bundle.Namespace, err)
	}
	record, err := InClusterRecord(ctx, client)
	if err != nil {
		return &ClusterDetection{State: ClusterDamaged, Problem: err.Error()}, nil
	}
	if record == nil {
		return &ClusterDetection{State: ClusterFresh}, nil
	}
	if record.Ownership != OwnershipExistingCluster {
		return &ClusterDetection{State: ClusterManaged, Record: record}, nil
	}
	return &ClusterDetection{State: ClusterInstalled, Record: record}, nil
}

// ExistingClusterOptions parameterize an existing-cluster converge.
type ExistingClusterOptions struct {
	// SkalidImage and SkalidImageID name the control-plane image. Install
	// takes them from the config; upgrade resolves the running image.
	SkalidImage   string
	SkalidImageID string
	// AdminEmail and AdminPassword bootstrap the first operator account;
	// SkipAdmin bypasses it (reconverge/upgrade).
	AdminEmail    string
	AdminPassword string
	SkipAdmin     bool
	Progress      Progress
}

// PreflightExistingCluster verifies the target cluster can host the bundle
// before any mutation: a supported Kubernetes version, the named ingress
// class, a usable storage class, and no operator collision the config did
// not opt into reusing.
func PreflightExistingCluster(ctx context.Context, client *kube.Client, config *ExistingClusterConfig, progress Progress) error {
	if progress == nil {
		progress = silentProgress{}
	}
	progress.Start("Verify cluster version and storage prerequisites")

	version, err := client.Clientset.Discovery().ServerVersion()
	if err != nil {
		return fmt.Errorf("read kubernetes version: %w", err)
	}
	minor := strings.TrimSuffix(version.Minor, "+")
	if parsed, convErr := parseMinor(minor); convErr == nil && parsed < MinExistingKubernetesMinor {
		return fmt.Errorf("this cluster runs kubernetes 1.%s; existing-cluster mode requires 1.%d or newer",
			minor, MinExistingKubernetesMinor)
	}

	classes := client.Clientset.NetworkingV1().IngressClasses()
	if _, err := classes.Get(ctx, config.Ingress.ClassName, metav1.GetOptions{}); err != nil {
		if apierrors.IsNotFound(err) {
			return fmt.Errorf("no IngressClass named %q exists in this cluster", config.Ingress.ClassName)
		}
		return fmt.Errorf("read IngressClass %q: %w", config.Ingress.ClassName, err)
	}

	if config.Storage.ClassName == "" {
		if err := verifyDefaultStorageClass(ctx, client); err != nil {
			return err
		}
	} else if err := verifyStorageClass(ctx, client, config.Storage.ClassName); err != nil {
		return err
	}

	if err := verifyOperator(ctx, client, "cnpg", config.Operators.CNPG,
		"clusters.postgresql.cnpg.io"); err != nil {
		return err
	}
	if err := verifyOperator(ctx, client, "cert-manager", config.Operators.CertManager,
		"clusterissuers.cert-manager.io"); err != nil {
		return err
	}
	progress.Done("")
	return nil
}

func parseMinor(minor string) (int, error) {
	value := 0
	if _, err := fmt.Sscanf(minor, "%d", &value); err != nil {
		return 0, err
	}
	return value, nil
}

func verifyDefaultStorageClass(ctx context.Context, client *kube.Client) error {
	classes, err := client.Clientset.StorageV1().StorageClasses().List(ctx, metav1.ListOptions{})
	if err != nil {
		return fmt.Errorf("list storage classes: %w", err)
	}
	for _, class := range classes.Items {
		if class.Annotations["storageclass.kubernetes.io/is-default-class"] == "true" {
			return nil
		}
	}
	return errors.New("this cluster has no default StorageClass; set storage.className in the config")
}

func verifyStorageClass(ctx context.Context, client *kube.Client, name string) error {
	if _, err := client.Clientset.StorageV1().StorageClasses().Get(ctx, name, metav1.GetOptions{}); err != nil {
		if apierrors.IsNotFound(err) {
			return fmt.Errorf("no StorageClass named %q exists in this cluster", name)
		}
		return fmt.Errorf("read StorageClass %q: %w", name, err)
	}
	return nil
}

// verifyOperator reconciles the config choice with the cluster: install
// refuses when the operator's CRD already exists (reuse must be explicit),
// use-existing refuses when it is absent.
func verifyOperator(ctx context.Context, client *kube.Client, name, choice, crd string) error {
	_, err := client.Clientset.Discovery().RESTClient().
		Get().AbsPath("/apis/apiextensions.k8s.io/v1/customresourcedefinitions/" + crd).
		DoRaw(ctx)
	present := err == nil
	switch choice {
	case "install":
		if present {
			return fmt.Errorf("%s is already installed in this cluster; set operators.%s: use-existing "+
				"to reuse it or remove it first", name, operatorConfigKey(name))
		}
	case "use-existing":
		if !present {
			return fmt.Errorf("operators.%s is use-existing but %s is not installed in this cluster",
				operatorConfigKey(name), name)
		}
	}
	return nil
}

func operatorConfigKey(name string) string {
	if name == "cert-manager" {
		return "certManager"
	}
	return "cnpg"
}

// ConvergeExistingCluster installs or reconverges the Skali bundle into an
// existing cluster from the config or a prior record. It owns only the
// bundle: it never touches nodes, k3s, or Kubernetes versions. This is the
// entry point for both install (with admin bootstrap) and upgrade
// (SkipAdmin, inputs from the stored record).
func ConvergeExistingCluster(ctx context.Context, client *kube.Client, record *Record, opts ExistingClusterOptions) (*InitResult, error) {
	progress := opts.Progress
	if progress == nil {
		progress = silentProgress{}
	}
	if record.Existing == nil {
		return nil, errors.New("existing-cluster converge requires an existing-cluster record")
	}
	if opts.SkalidImage == "" {
		return nil, errors.New("existing-cluster converge requires a skalid image")
	}

	authSecret, err := ensureAuthSecret(ctx, client)
	if err != nil {
		return nil, err
	}
	tokenKeyPEM, tokenCertPEM, err := ensureRegistryTokenKeypair(ctx, client)
	if err != nil {
		return nil, err
	}
	pullSecret, err := ensureNodePullSecret(ctx, client)
	if err != nil {
		return nil, err
	}

	canonical, err := record.CanonicalYAML()
	if err != nil {
		return nil, err
	}
	existing := record.Existing
	profile := bundle.Profile{
		SkalidImage:   opts.SkalidImage,
		SkalidImageID: opts.SkalidImageID,
		AuthSecret:    authSecret,
		// Artifact references and pulls both travel the public registry
		// domain here: there is no in-cluster mirror on unmanaged nodes.
		RegistryHost: record.Endpoints.Registry,
		Production: &bundle.Production{
			IngressHost:        record.Endpoints.API,
			RegistryDomain:     record.Endpoints.Registry,
			TokenKeyPEM:        tokenKeyPEM,
			TokenCertPEM:       tokenCertPEM,
			NodePullSecret:     pullSecret,
			ACMEEmail:          record.TLS.IssuerEmail,
			ACMEServer:         record.TLS.ACMEServer,
			Capabilities:       existing.Capabilities,
			DatabaseTier:       layout.Tier(existing.DatabaseTier),
			DatabaseStorage:    existing.DatabaseStorage,
			RegistryStorage:    existing.RegistryStorage,
			InstallationRecord: canonical,
			External: &bundle.ExternalCluster{
				IngressClassName: existing.IngressClassName,
				StorageClassName: existing.StorageClassName,
				SkipCNPG:         existing.Operators.CNPG == "use-existing",
				SkipCertManager:  existing.Operators.CertManager == "use-existing",
			},
		},
	}

	if err := bundle.Converge(ctx, client, profile, progress); err != nil {
		return nil, err
	}
	progress.Start("Wait for skalid ready")
	if err := waitSkalidHealthy(ctx, client); err != nil {
		return nil, err
	}
	progress.Done("")

	if !opts.SkipAdmin {
		adminProfile := profile
		adminProfile.AdminEmail = opts.AdminEmail
		adminProfile.AdminPassword = opts.AdminPassword
		if err := bundle.EnsureAdminUser(ctx, client, adminProfile, progress); err != nil {
			return nil, err
		}
	}
	if err := bundle.StampHash(ctx, client, profile); err != nil {
		return nil, err
	}
	return &InitResult{
		APIURL:      "https://" + record.Endpoints.API,
		RegistryURL: "https://" + record.Endpoints.Registry,
	}, nil
}

// RunningSkalidImage reads the image (and its content id, if annotated)
// of the installed skalid deployment, for the existing-cluster upgrade
// path that reconverges the running control plane.
func RunningSkalidImage(ctx context.Context, client *kube.Client) (image, imageID string, err error) {
	deployment, err := client.Clientset.AppsV1().Deployments(bundle.Namespace).Get(ctx, "skalid", metav1.GetOptions{})
	if err != nil {
		return "", "", fmt.Errorf("read skalid deployment: %w", err)
	}
	for _, container := range deployment.Spec.Template.Spec.Containers {
		if container.Name == "skalid" {
			image = container.Image
		}
	}
	if image == "" && len(deployment.Spec.Template.Spec.Containers) > 0 {
		image = deployment.Spec.Template.Spec.Containers[0].Image
	}
	if image == "" {
		return "", "", errors.New("the skalid deployment names no image")
	}
	return image, deployment.Spec.Template.Annotations["skali.dev/image-id"], nil
}

// ensureNodePullSecret reads the shared registry node credential from the
// token secret, minting one when absent. On managed installs this lives in
// registries.yaml; here it lives only in-cluster.
func ensureNodePullSecret(ctx context.Context, client *kube.Client) (string, error) {
	secret, err := client.Clientset.CoreV1().Secrets(bundle.Namespace).Get(ctx, "skali-registry-token", metav1.GetOptions{})
	if err == nil {
		if value := secret.Data["node-secret"]; len(value) > 0 {
			return string(value), nil
		}
	} else if !apierrors.IsNotFound(err) {
		return "", fmt.Errorf("read registry token secret: %w", err)
	}
	return newPullSecret()
}

// RecordFromExistingConfig builds the installation record an
// existing-cluster install writes, reusing the InstallationID of a prior
// record so repeat installs stay convergent.
func RecordFromExistingConfig(config *ExistingClusterConfig, prior *Record) *Record {
	id := uuid.NewString()
	if prior != nil && prior.InstallationID != "" {
		id = prior.InstallationID
	}
	return &Record{
		Version:        RecordVersion,
		InstallationID: id,
		Provider:       ProviderExternal,
		Cluster:        config.Cluster,
		Ownership:      OwnershipExistingCluster,
		Endpoints:      &Endpoints{API: config.Endpoints.API, Registry: config.Endpoints.Registry},
		TLS:            &TLSConfig{IssuerEmail: config.TLS.IssuerEmail, ACMEServer: config.TLS.ACMEServer},
		Existing: &ExistingClusterRecord{
			IngressClassName: config.Ingress.ClassName,
			StorageClassName: config.Storage.ClassName,
			DatabaseTier:     config.Database.Tier,
			DatabaseStorage:  config.Database.Storage,
			RegistryStorage:  config.Registry.Storage,
			Capabilities:     config.Capabilities,
			Operators: OperatorsRecord{
				CNPG: config.Operators.CNPG, CertManager: config.Operators.CertManager,
			},
		},
		Versions: Versions{Bundle: version.Version, Installer: version.Version},
	}
}

// ClusterStatus is the read-only status of an existing-cluster
// installation.
type ClusterStatus struct {
	Record        *Record
	BundleVersion string
	BundleCurrent bool
	Nodes         int
	Components    []ComponentStatus
}

// GatherClusterStatus reads the existing-cluster installation's health
// through the kubeconfig alone.
func GatherClusterStatus(ctx context.Context, client *kube.Client, record *Record) (*ClusterStatus, error) {
	status := &ClusterStatus{Record: record, BundleVersion: record.Versions.Bundle}
	nodes, err := client.Clientset.CoreV1().Nodes().List(ctx, metav1.ListOptions{})
	if err == nil {
		status.Nodes = len(nodes.Items)
	}
	status.BundleCurrent = record.Versions.Bundle == version.Version &&
		bundle.StampedHash(ctx, client) != ""
	status.Components, _ = gatherComponents(ctx, client)
	return status, nil
}

// UninstallExistingClusterBundle removes the Skali bundle from an existing
// cluster: bundle scope only, never node or Kubernetes lifecycle. Operator
// namespaces the record marked use-existing are left alone.
func UninstallExistingClusterBundle(ctx context.Context, client *kube.Client, record *Record, progress Progress) error {
	if progress == nil {
		progress = silentProgress{}
	}
	inventory, err := GatherBundleInventory(ctx, client)
	if err != nil {
		return err
	}
	operators := ownedOperatorNamespaces(record)
	waves := [][]string{
		inventory.ProjectNamespaces,
		{"skali-platform"},
		{bundle.Namespace},
		operators,
	}
	titles := []string{
		"Remove project namespaces",
		"Remove platform namespace",
		"Remove skali-system",
		"Remove blessed operators",
	}
	for index, wave := range waves {
		progress.Start(titles[index])
		deleted, err := deleteNamespaces(ctx, client, wave, progress)
		if err != nil {
			return err
		}
		if deleted == 0 {
			progress.Skip("nothing to remove")
			continue
		}
		progress.Done(fmt.Sprintf("%d namespace(s)", deleted))
	}
	return nil
}

// ownedOperatorNamespaces lists only the operator namespaces skali
// installed: a reused operator's namespace is the cluster's, never ours.
func ownedOperatorNamespaces(record *Record) []string {
	if record.Existing == nil {
		return bundle.OperatorNamespaces
	}
	var owned []string
	for _, namespace := range bundle.OperatorNamespaces {
		if namespace == "cnpg-system" && record.Existing.Operators.CNPG == "use-existing" {
			continue
		}
		if namespace == "cert-manager" && record.Existing.Operators.CertManager == "use-existing" {
			continue
		}
		owned = append(owned, namespace)
	}
	return owned
}

package installer

import (
	"context"
	"errors"
	"fmt"

	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"

	"github.com/Hinkolas/skali/internal/bundle"
	"github.com/Hinkolas/skali/internal/installer/host"
	"github.com/Hinkolas/skali/internal/kube"
	"github.com/Hinkolas/skali/internal/layout"
)

// LiveProfile reconstructs the production bundle profile from the running
// cluster and the record, strictly read-only: every input Init would
// generate when absent is instead a named refusal here. Tier apply and
// repair reconverge through it, so its result must hash identically to
// the profile Init builds from the same inputs (pinned by unit test);
// that is why it never mutates the record and derives the tier from live
// node labels exactly like Init does.
func LiveProfile(ctx context.Context, client *kube.Client, runner host.Runner, record *Record) (bundle.Profile, layout.Layout, error) {
	var profile bundle.Profile
	if record == nil {
		return profile, layout.Layout{}, errors.New("no installation record")
	}
	if record.Endpoints == nil || record.Endpoints.API == "" || record.Endpoints.Registry == "" ||
		record.TLS == nil || record.TLS.IssuerEmail == "" {
		return profile, layout.Layout{}, errors.New("the installation record is missing endpoints or tls answers; " +
			"run skali cluster upgrade interactively to provide them")
	}

	authSecret, err := client.Clientset.CoreV1().Secrets(bundle.Namespace).Get(ctx, "skali-auth", metav1.GetOptions{})
	if err != nil || len(authSecret.Data["AUTH_SECRET"]) == 0 {
		return profile, layout.Layout{}, errors.New("the cluster auth secret is missing or empty; " +
			"run skali cluster upgrade, which regenerates it")
	}
	tokenSecret, err := client.Clientset.CoreV1().Secrets(bundle.Namespace).Get(ctx, "skali-registry-token", metav1.GetOptions{})
	if err != nil || len(tokenSecret.Data["key.pem"]) == 0 || len(tokenSecret.Data["cert.pem"]) == 0 {
		return profile, layout.Layout{}, errors.New("the registry token keypair is missing; " +
			"run skali cluster upgrade, which regenerates it")
	}

	registries, err := runner.ReadFile(ctx, K3sRegistriesPath)
	if err != nil {
		return profile, layout.Layout{}, fmt.Errorf("read %s: %w", K3sRegistriesPath, err)
	}
	pullSecret := registriesPullSecret(registries)
	if pullSecret == "" {
		return profile, layout.Layout{}, errors.New(K3sRegistriesPath + " is missing the registry pull credential; " +
			"run skali cluster upgrade, which mints it")
	}

	deployment, err := client.Clientset.AppsV1().Deployments(bundle.Namespace).Get(ctx, "skalid", metav1.GetOptions{})
	if err != nil {
		return profile, layout.Layout{}, errors.New("the skalid deployment is missing, so its image cannot be read; " +
			"run skali cluster upgrade, which resolves an image")
	}
	image := ""
	for _, container := range deployment.Spec.Template.Spec.Containers {
		if container.Name == "skalid" {
			image = container.Image
		}
	}
	if image == "" && len(deployment.Spec.Template.Spec.Containers) > 0 {
		image = deployment.Spec.Template.Spec.Containers[0].Image
	}
	if image == "" {
		return profile, layout.Layout{}, errors.New("the skalid deployment names no image; " +
			"run skali cluster upgrade, which resolves one")
	}
	imageID := deployment.Spec.Template.Annotations["skali.dev/image-id"]

	webDeployment, err := client.Clientset.AppsV1().Deployments(bundle.Namespace).Get(ctx, "skali-web", metav1.GetOptions{})
	if err != nil {
		return profile, layout.Layout{}, errors.New("the skali-web deployment is missing, so its image cannot be read; " +
			"run skali cluster upgrade, which resolves one")
	}
	webImage := ""
	for _, container := range webDeployment.Spec.Template.Spec.Containers {
		if container.Name == "skali-web" {
			webImage = container.Image
		}
	}
	if webImage == "" && len(webDeployment.Spec.Template.Spec.Containers) > 0 {
		webImage = webDeployment.Spec.Template.Spec.Containers[0].Image
	}
	if webImage == "" {
		return profile, layout.Layout{}, errors.New("the skali-web deployment names no image; " +
			"run skali cluster upgrade, which resolves one")
	}
	webImageID := webDeployment.Spec.Template.Annotations["skali.dev/image-id"]

	nodeList, err := client.Clientset.CoreV1().Nodes().List(ctx, metav1.ListOptions{})
	if err != nil {
		return profile, layout.Layout{}, fmt.Errorf("list nodes: %w", err)
	}
	if err := assertClusterMembership(nodeList.Items, record.Cluster); err != nil {
		return profile, layout.Layout{}, err
	}
	live := LayoutFromNodes(nodeList.Items, record.Cluster)
	topology := live.Topology()

	storageDriver := record.AppStorageDriver()
	registryStorageClass, err := liveRegistryStorageClass(ctx, client, storageDriver)
	if err != nil {
		return profile, layout.Layout{}, err
	}

	canonical, err := record.CanonicalYAML()
	if err != nil {
		return profile, layout.Layout{}, err
	}

	profile = bundle.Profile{
		SkalidImage:   image,
		SkalidImageID: imageID,
		AuthSecret:    string(authSecret.Data["AUTH_SECRET"]),
		RegistryHost:  bundle.RegistryInternalHost,
		Production: &bundle.Production{
			IngressHost:        record.Endpoints.API,
			RegistryDomain:     record.Endpoints.Registry,
			S3Domain:           record.Endpoints.S3,
			TokenKeyPEM:        string(tokenSecret.Data["key.pem"]),
			TokenCertPEM:       string(tokenSecret.Data["cert.pem"]),
			NodePullSecret:     pullSecret,
			ACMEEmail:          record.TLS.IssuerEmail,
			ACMEServer:         record.TLS.ACMEServer,
			Capabilities:       layout.UnionCapabilities(live.Nodes),
			DatabaseTier:       topology.DatabaseTier,
			DatabaseStorage:    DefaultDatabaseStorage,
			RegistryStorage:    DefaultRegistryStorage,
			RegistryNode:       record.RegistryNode,
			StorageDriver:      storageDriver,
			PlatformPreference: record.PlatformPreference,
			// Derived from the live topology exactly like Init, so the
			// hash-match invariant holds by construction.
			StorageReplicas:      layout.StorageReplicas(topology.Capable[layout.CapabilityApplication]),
			RegistryStorageClass: registryStorageClass,
			WebImage:             webImage,
			WebImageID:           webImageID,
			InstallationRecord:   canonical,
		},
	}
	return profile, live, nil
}

// liveRegistryStorageClass reads the registry claim's storage class so the
// profile always renders the shape the cluster already has: the class is
// immutable on an existing claim, and rendering anything else would wedge
// every future converge on a rejected apply. The storage-migrate command
// is the only sanctioned switch. Under the longhorn driver an absent claim
// reads as the Longhorn class (a fresh converge creates it there); under
// the local driver, and for any other live class, it reads as the legacy
// local-path shape.
func liveRegistryStorageClass(ctx context.Context, client *kube.Client, storageDriver string) (string, error) {
	if storageDriver != bundle.StorageDriverLonghorn {
		return "", nil
	}
	claim, err := client.Clientset.CoreV1().PersistentVolumeClaims(bundle.Namespace).
		Get(ctx, "skali-registry-data", metav1.GetOptions{})
	if apierrors.IsNotFound(err) {
		return bundle.StorageClassName, nil
	}
	if err != nil {
		return "", fmt.Errorf("read registry volume claim: %w", err)
	}
	if claim.Spec.StorageClassName != nil && *claim.Spec.StorageClassName == bundle.StorageClassName {
		return bundle.StorageClassName, nil
	}
	return "", nil
}

package bundle

import (
	"context"
	"errors"
	"fmt"
	"time"

	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/types"

	"github.com/Hinkolas/skali/internal/kube"
	"github.com/Hinkolas/skali/internal/layout"
)

// Progress narrates converge stages: Start begins a stage, Done concludes
// the running one with optional detail, Skip concludes it as not needed
// with the reason, and Note publishes transient detail inside a running
// stage (what a long wait is actually doing). A stage that errors is never
// concluded; the caller settles it from the returned error. A nil Progress
// is silent.
type Progress interface {
	Start(title string)
	Done(detail string)
	Skip(detail string)
	Note(line string)
}

type silentProgress struct{}

func (silentProgress) Start(string) {}
func (silentProgress) Done(string)  {}
func (silentProgress) Skip(string)  {}
func (silentProgress) Note(string)  {}

// Converge applies the profile's bundle in dependency order with readiness
// waits: namespace, blessed operators (CNPG, plus cert-manager under a
// production profile), the skali cluster issuer and strict-SNI edge
// policy, the bootstrap database sized to its tier, the managed registry,
// and skalid with the in-cluster installation record. It never applies the
// bootstrap-user stage (see EnsureAdminUser) and never waits on TLS
// issuance: the platform certificates converge asynchronously while the
// edge refuses handshakes for hosts without an issued certificate (strict
// SNI), so health proofs must go through skalid readiness, not chain
// validity. (Project deploys are the exception: the reconcile kernel gates
// application rollouts on their route certificates.) Converge does not
// stamp the bundle hash; the caller stamps via StampHash after its own
// end-to-end health proof.
func Converge(ctx context.Context, client *kube.Client, profile Profile, progress Progress) error {
	if progress == nil {
		progress = silentProgress{}
	}
	objects, err := Render(profile)
	if err != nil {
		return err
	}
	applier := &Applier{Client: client, WaitTimeout: 10 * time.Minute}
	production := profile.Production

	progress.Start("Apply blessed operators")
	if err := applier.ApplyObjects(ctx, objects.Namespace); err != nil {
		return err
	}
	// The PriorityClasses precede every pod that names one, the bootstrap
	// database included.
	if err := applier.ApplyObjects(ctx, objects.Priority); err != nil {
		return err
	}
	if err := applier.ApplyManifest(ctx, CNPGManifest()); err != nil {
		return err
	}
	if production != nil {
		if err := applier.ApplyManifest(ctx, CertManagerManifest()); err != nil {
			return err
		}
		// Longhorn rides the operators stage. The disk labels precede the
		// manifest so the first manager start already sees which nodes
		// hold replica data (fresh nodes get the label at registration;
		// this stamp covers nodes that joined before this bundle release
		// and self-heals a stripped label).
		if err := ensureLonghornDiskLabels(ctx, client); err != nil {
			return err
		}
		if err := applier.ApplyManifest(ctx, LonghornManifest()); err != nil {
			return err
		}
	}
	if err := applier.WaitDeploymentReady(ctx, "cnpg-system", "cnpg-controller-manager"); err != nil {
		return err
	}
	// The Traefik metrics overlay rides the operators stage: it is chart
	// configuration for the k3s edge, and the helm controller re-renders
	// asynchronously (rolling Traefik once when the overlay changes), so
	// there is nothing to wait on. The HelmChartConfig CRD ships with k3s.
	if err := applier.ApplyObjects(ctx, objects.EdgeMetrics); err != nil {
		return err
	}
	cnpgDetail := "CNPG " + CNPGVersion
	operators := cnpgDetail + ", Traefik (k3s)"
	if production != nil {
		// The cainjector wait closes a known CRD-conversion race; the
		// webhook wait covers issuer validation.
		for _, name := range []string{"cert-manager", "cert-manager-webhook", "cert-manager-cainjector"} {
			if err := applier.WaitDeploymentReady(ctx, "cert-manager", name); err != nil {
				return err
			}
		}
		// A first install pulls over a gigabyte of Longhorn images;
		// narrate the wait so a quiet console is not mistaken for a hang.
		progress.Note("waiting for Longhorn (a first install pulls its images)")
		if err := applier.WaitDaemonSetReady(ctx, "longhorn-system", "longhorn-manager"); err != nil {
			return err
		}
		if err := applier.WaitDeploymentReady(ctx, "longhorn-system", "longhorn-driver-deployer"); err != nil {
			return err
		}
		// The CSI workloads are created at runtime by the driver deployer,
		// not shipped in the manifest; the NotFound polling inside the
		// waits covers the creation race. These two gate actual
		// provisioning ability.
		if err := applier.WaitDeploymentReady(ctx, "longhorn-system", "csi-provisioner"); err != nil {
			return err
		}
		if err := applier.WaitDaemonSetReady(ctx, "longhorn-system", "longhorn-csi-plugin"); err != nil {
			return err
		}
		operators = cnpgDetail + ", cert-manager " + CertManagerVersion + ", Longhorn " + LonghornVersion + ", Traefik (k3s)"
	}
	progress.Done(operators)

	if production != nil {
		progress.Start("Apply storage class")
		err := applier.ApplyObjects(ctx, objects.Storage)
		if err != nil && apierrors.IsInvalid(err) {
			// StorageClass parameters are immutable: a replica-count
			// change (the topology grew or shrank) means delete and
			// re-apply. Deleting a class never touches bound claims;
			// existing volumes keep their creation-time replica count.
			if deleteErr := client.Clientset.StorageV1().StorageClasses().Delete(ctx, StorageClassName, metav1.DeleteOptions{}); deleteErr != nil && !apierrors.IsNotFound(deleteErr) {
				return fmt.Errorf("bundle: replace storage class %s: %w", StorageClassName, deleteErr)
			}
			err = applier.ApplyObjects(ctx, objects.Storage)
		}
		if err != nil {
			return err
		}
		progress.Done(fmt.Sprintf("%s, %d replica(s)", StorageClassName, production.StorageReplicas))
	}

	if production != nil {
		progress.Start("Apply cluster issuer")
		if err := applier.ApplyObjectsRetry(ctx, objects.Issuer, 2*time.Minute); err != nil {
			return err
		}
		server := production.ACMEServer
		if server == "" {
			server = ACMEProductionServer
		}
		progress.Done("acme " + server)

		// The retry rides out a fresh cluster where the traefik-crd chart
		// has not established the TLSOption CRD yet, mirroring the issuer
		// retry over cert-manager's CRDs.
		progress.Start("Apply edge TLS policy")
		if err := applier.ApplyObjectsRetry(ctx, objects.Edge, 2*time.Minute); err != nil {
			return err
		}
		progress.Done("strict SNI")
	}

	progress.Start("Apply bootstrap database")
	if err := applier.ApplyObjectsRetry(ctx, objects.Database, 2*time.Minute); err != nil {
		return err
	}
	instances := 1
	tier := "single"
	if production != nil {
		instances = layout.TierInstances(production.DatabaseTier)
		tier = string(production.DatabaseTier)
	}
	if err := applier.WaitClusterReady(ctx, Namespace, "skali-db", instances); err != nil {
		return err
	}
	progress.Done(fmt.Sprintf("tier %s, %d instance(s)", tier, instances))

	// The registry and skalid stages now carry Traefik CRs (and, under
	// production, Certificates), so both ride the same CRD-establishment
	// retry as the edge stage: on a fresh cluster the traefik-crd chart may
	// not have registered IngressRoute yet, locally included.
	progress.Start("Apply managed registry")
	if err := applier.ApplyObjectsRetry(ctx, objects.Registry, 2*time.Minute); err != nil {
		return err
	}
	if err := applier.WaitDeploymentReady(ctx, Namespace, "skali-registry"); err != nil {
		return err
	}
	progress.Done(profile.RegistryHost)

	progress.Start("Apply skalid")
	if err := applier.ApplyObjectsRetry(ctx, objects.Skalid, 2*time.Minute); err != nil {
		return err
	}
	// The record lands before the skalid wait so a first-boot import
	// always finds it.
	if err := applier.ApplyObjects(ctx, objects.Record); err != nil {
		return err
	}
	if err := applier.WaitDeploymentReady(ctx, Namespace, "skalid"); err != nil {
		return err
	}
	progress.Done(profile.SkalidImage)

	if production != nil {
		progress.Start("Apply web console")
		if err := applier.ApplyObjects(ctx, objects.Web); err != nil {
			return err
		}
		if err := applier.WaitDeploymentReady(ctx, Namespace, "skali-web"); err != nil {
			return err
		}
		progress.Done(production.WebImage)
	}
	return nil
}

// ensureLonghornDiskLabels stamps the Longhorn disk label onto every
// application-capable node so replica data only lands there (the vendored
// manifest is patched to create-default-disk-on-labeled-nodes). Idempotent;
// nodes already carrying the label are left untouched.
func ensureLonghornDiskLabels(ctx context.Context, client *kube.Client) error {
	selector := layout.CapabilityLabel(layout.CapabilityApplication) + "=" + layout.CapabilityLabelValue
	nodes, err := client.Clientset.CoreV1().Nodes().List(ctx, metav1.ListOptions{LabelSelector: selector})
	if err != nil {
		return fmt.Errorf("bundle: list application nodes: %w", err)
	}
	patch := fmt.Appendf(nil, `{"metadata":{"labels":{%q:%q}}}`,
		layout.LonghornDiskLabel, layout.LonghornDiskLabelValue)
	for _, node := range nodes.Items {
		if node.Labels[layout.LonghornDiskLabel] == layout.LonghornDiskLabelValue {
			continue
		}
		if _, err := client.Clientset.CoreV1().Nodes().Patch(ctx, node.Name, types.MergePatchType, patch, metav1.PatchOptions{}); err != nil {
			return fmt.Errorf("bundle: label node %s for longhorn disks: %w", node.Name, err)
		}
	}
	return nil
}

// EnsureAdminUser applies the bootstrap-user stage: an idempotent job that
// creates the first operator account unless it exists. It runs outside the
// converge because production never persists the admin credentials; they
// are collected right before this call.
func EnsureAdminUser(ctx context.Context, client *kube.Client, profile Profile, progress Progress) error {
	if progress == nil {
		progress = silentProgress{}
	}
	if profile.AdminEmail == "" || profile.AdminPassword == "" {
		return errors.New("bundle: admin email and password are required to create the admin account")
	}
	objects, err := Render(profile)
	if err != nil {
		return err
	}
	applier := &Applier{Client: client}
	progress.Start("Create admin account")
	if err := applier.ApplyObjects(ctx, objects.BootstrapUser); err != nil {
		return err
	}
	if err := applier.WaitJobComplete(ctx, Namespace, "skali-bootstrap-user"); err != nil {
		return err
	}
	progress.Done(profile.AdminEmail)
	return nil
}

// StampedHash reads the hash stamped by the last completed converge;
// absent or unreadable reads as empty, which matches no bundle.
func StampedHash(ctx context.Context, client *kube.Client) string {
	namespace, err := client.Clientset.CoreV1().Namespaces().Get(ctx, Namespace, metav1.GetOptions{})
	if err != nil {
		return ""
	}
	return namespace.Annotations[HashAnnotation]
}

// StampHash re-applies the namespace stage with the bundle hash annotation
// added, under the same installer field manager, so the plain namespace
// apply at the start of the next converge clears the stamp again.
func StampHash(ctx context.Context, client *kube.Client, profile Profile) error {
	objects, err := Render(profile)
	if err != nil {
		return err
	}
	hash := Hash(profile)
	stamped := make([]unstructured.Unstructured, 0, len(objects.Namespace))
	for _, object := range objects.Namespace {
		annotated := object.DeepCopy()
		annotations := annotated.GetAnnotations()
		if annotations == nil {
			annotations = map[string]string{}
		}
		annotations[HashAnnotation] = hash
		annotated.SetAnnotations(annotations)
		stamped = append(stamped, *annotated)
	}
	applier := &Applier{Client: client}
	return applier.ApplyObjects(ctx, stamped)
}

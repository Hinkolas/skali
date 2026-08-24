package installer

import (
	"context"
	"errors"
	"fmt"
	"time"

	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/client-go/util/retry"

	"github.com/Hinkolas/skali/internal/bundle"
	"github.com/Hinkolas/skali/internal/installer/host"
	"github.com/Hinkolas/skali/internal/kube"
)

const registryClaimName = "skali-registry-data"

// ErrRegistryStorageMigrated reports that the registry volume already lives
// on the Longhorn class and no migration is needed.
var ErrRegistryStorageMigrated = errors.New("the registry volume is already on " + bundle.StorageClassName)

// MigrateRegistryStorage moves the registry volume from the legacy
// local-path claim onto the Longhorn class: scale the registry to zero,
// delete the claim, and reconverge with the Longhorn registry shape, which
// recreates the claim on the new class, drops the node pin, and restores
// the replica count. Registry contents are deliberately not preserved:
// every image is re-pushable through skali deploy, and the registry
// rebuilds lazily. Every step is level-triggered, so an interrupted
// migration is finished by rerunning the command (or by the next plain
// converge once the claim is gone).
func MigrateRegistryStorage(ctx context.Context, client *kube.Client, runner host.Runner, record *Record, progress Progress) error {
	if progress == nil {
		progress = silentProgress{}
	}
	if published, err := InClusterRecord(ctx, client); err == nil && published != nil &&
		published.Node.Name != "" && published.Node.Name != record.Node.Name {
		return fmt.Errorf("the bundle is maintained on %s; run the migration there", published.Node.Name)
	}
	if record.AppStorageDriver() != bundle.StorageDriverLonghorn {
		return errors.New("this cluster uses the local storage driver; the registry migration " +
			"requires longhorn (enable it with skali cluster init --storage-driver longhorn)")
	}
	if _, err := client.Clientset.StorageV1().StorageClasses().Get(ctx, bundle.StorageClassName, metav1.GetOptions{}); err != nil {
		return fmt.Errorf("the %s storage class is not installed; run skali cluster upgrade first", bundle.StorageClassName)
	}

	claim, err := client.Clientset.CoreV1().PersistentVolumeClaims(bundle.Namespace).
		Get(ctx, registryClaimName, metav1.GetOptions{})
	switch {
	case apierrors.IsNotFound(err):
		// An earlier attempt already deleted the claim; the reconverge
		// below recreates it on the Longhorn class.
	case err != nil:
		return fmt.Errorf("read registry volume claim: %w", err)
	case claim.Spec.StorageClassName != nil && *claim.Spec.StorageClassName == bundle.StorageClassName:
		return ErrRegistryStorageMigrated
	default:
		if err := scaleRegistryDown(ctx, client, progress); err != nil {
			return err
		}
		progress.Start("Delete the legacy registry volume")
		uid := claim.UID
		err := client.Clientset.CoreV1().PersistentVolumeClaims(bundle.Namespace).
			Delete(ctx, registryClaimName, metav1.DeleteOptions{
				Preconditions: &metav1.Preconditions{UID: &uid},
			})
		if err != nil && !apierrors.IsNotFound(err) {
			return fmt.Errorf("delete registry volume claim: %w", err)
		}
		if err := waitClaimGone(ctx, client, registryClaimName); err != nil {
			return err
		}
		progress.Done("")
	}

	// LiveProfile reads the now-absent claim as the Longhorn class, so the
	// converge renders the new registry shape and stamps a matching hash.
	profile, _, err := LiveProfile(ctx, client, runner, record)
	if err != nil {
		return err
	}
	if err := bundle.Converge(ctx, client, profile, progress); err != nil {
		return err
	}
	return bundle.StampHash(ctx, client, profile)
}

// scaleRegistryDown stops the registry so its Recreate-strategy pod
// releases the claim before deletion.
func scaleRegistryDown(ctx context.Context, client *kube.Client, progress Progress) error {
	progress.Start("Stop the managed registry")
	err := retry.RetryOnConflict(retry.DefaultRetry, func() error {
		deployment, err := client.Clientset.AppsV1().Deployments(bundle.Namespace).
			Get(ctx, "skali-registry", metav1.GetOptions{})
		if apierrors.IsNotFound(err) {
			return nil
		}
		if err != nil {
			return err
		}
		if deployment.Spec.Replicas != nil && *deployment.Spec.Replicas == 0 {
			return nil
		}
		deployment = deployment.DeepCopy()
		replicas := int32(0)
		deployment.Spec.Replicas = &replicas
		_, err = client.Clientset.AppsV1().Deployments(bundle.Namespace).
			Update(ctx, deployment, metav1.UpdateOptions{})
		return err
	})
	if err != nil {
		return fmt.Errorf("scale registry to zero: %w", err)
	}
	deadline := time.Now().Add(2 * time.Minute)
	for {
		pods, err := client.Clientset.CoreV1().Pods(bundle.Namespace).List(ctx, metav1.ListOptions{
			LabelSelector: "app.kubernetes.io/name=skali-registry",
		})
		if err != nil {
			return fmt.Errorf("list registry pods: %w", err)
		}
		if len(pods.Items) == 0 {
			progress.Done("")
			return nil
		}
		if time.Now().After(deadline) {
			return errors.New("registry pods did not terminate; inspect with kubectl get pods -n " + bundle.Namespace)
		}
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-time.After(3 * time.Second):
		}
	}
}

func waitClaimGone(ctx context.Context, client *kube.Client, name string) error {
	deadline := time.Now().Add(2 * time.Minute)
	for {
		_, err := client.Clientset.CoreV1().PersistentVolumeClaims(bundle.Namespace).
			Get(ctx, name, metav1.GetOptions{})
		if apierrors.IsNotFound(err) {
			return nil
		}
		if err != nil {
			return fmt.Errorf("wait for claim %s deletion: %w", name, err)
		}
		if time.Now().After(deadline) {
			return fmt.Errorf("claim %s is still terminating; a pod may still mount it", name)
		}
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-time.After(3 * time.Second):
		}
	}
}

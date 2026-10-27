package installer

import (
	"context"
	"fmt"
	"os"
	"time"

	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"

	"github.com/Hinkolas/skali/internal/installer/host"
	"github.com/Hinkolas/skali/internal/kube"
)

// KubeClient builds a Kubernetes client from the k3s-written kubeconfig,
// read through the runner and staged into a process-local temp file.
// Explicit on purpose: the installer never consults an ambient kubeconfig
// (that is also kube.New's contract). Commands run on the Linux server,
// so the local API address in its kubeconfig is used unchanged.
func KubeClient(ctx context.Context, runner host.Runner) (*kube.Client, error) {
	data, err := runner.ReadFile(ctx, K3sKubeconfigPath)
	if err != nil {
		return nil, fmt.Errorf("read %s: %w", K3sKubeconfigPath, err)
	}
	staged, err := os.CreateTemp("", "skali-kubeconfig-*")
	if err != nil {
		return nil, fmt.Errorf("stage kubeconfig: %w", err)
	}
	path := staged.Name()
	defer os.Remove(path)
	if _, err := staged.Write(data); err != nil {
		staged.Close()
		return nil, fmt.Errorf("stage kubeconfig: %w", err)
	}
	if err := staged.Close(); err != nil {
		return nil, fmt.Errorf("stage kubeconfig: %w", err)
	}
	return kube.New(path)
}

// A joining server must have loaded the admission contract before enrollment
// completes. Older installations without the policy retain version-guarded
// applies; the daemon also gates new API endpoints independently.
func waitJoinedOwnership(ctx context.Context, runner host.Runner) error {
	client, err := KubeClient(ctx, runner)
	if err != nil {
		return err
	}
	_, err = client.Clientset.AdmissionregistrationV1().ValidatingAdmissionPolicies().Get(ctx, kube.OwnershipPolicyName, metav1.GetOptions{})
	if apierrors.IsNotFound(err) {
		return nil
	}
	if err != nil {
		return err
	}
	waitCtx, cancel := context.WithTimeout(ctx, 2*time.Minute)
	defer cancel()
	ticker := time.NewTicker(2 * time.Second)
	defer ticker.Stop()
	for {
		err := client.VerifyOwnershipPolicy(waitCtx)
		if err == nil {
			return nil
		}
		select {
		case <-waitCtx.Done():
			return fmt.Errorf("joining server ownership policy is not enforced: %w", err)
		case <-ticker.C:
		}
	}
}

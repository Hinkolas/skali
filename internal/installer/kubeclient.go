package installer

import (
	"context"
	"fmt"
	"os"
	"time"

	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/client-go/tools/clientcmd"

	"github.com/Hinkolas/skali/internal/installer/host"
	"github.com/Hinkolas/skali/internal/kube"
)

// KubeClient builds a Kubernetes client from the k3s-written kubeconfig,
// read through the runner and staged into a process-local temp file.
// Explicit on purpose: the installer never consults an ambient kubeconfig
// (that is also kube.New's contract). A remote runner (Lima) implements
// host.APIAddresser, and its address replaces the kubeconfig's
// 127.0.0.1:6443, which is valid only inside the VM.
func KubeClient(ctx context.Context, runner host.Runner) (*kube.Client, error) {
	data, err := runner.ReadFile(ctx, K3sKubeconfigPath)
	if err != nil {
		return nil, fmt.Errorf("read %s: %w", K3sKubeconfigPath, err)
	}
	if addresser, ok := runner.(host.APIAddresser); ok {
		address, err := addresser.APIAddress(ctx)
		if err != nil {
			return nil, fmt.Errorf("resolve kubernetes api address: %w", err)
		}
		data, err = rewriteKubeconfigAddress(data, address)
		if err != nil {
			return nil, err
		}
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

// rewriteKubeconfigAddress points every cluster entry at the given
// host:port, preserving certificate data and everything else.
func rewriteKubeconfigAddress(data []byte, address string) ([]byte, error) {
	config, err := clientcmd.Load(data)
	if err != nil {
		return nil, fmt.Errorf("parse kubeconfig: %w", err)
	}
	for _, cluster := range config.Clusters {
		cluster.Server = "https://" + address
	}
	rewritten, err := clientcmd.Write(*config)
	if err != nil {
		return nil, fmt.Errorf("rewrite kubeconfig: %w", err)
	}
	return rewritten, nil
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

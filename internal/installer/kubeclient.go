package installer

import (
	"context"
	"fmt"
	"os"

	"github.com/Hinkolas/skali/internal/installer/host"
	"github.com/Hinkolas/skali/internal/kube"
)

// KubeClient builds a Kubernetes client from the k3s-written kubeconfig,
// read through the runner and staged into a process-local temp file.
// Explicit on purpose: the installer never consults an ambient kubeconfig
// (that is also kube.New's contract). This is the one seam a remote
// runner (Lima) will need to extend with an address rewrite or forwarded
// port.
func KubeClient(ctx context.Context, runner host.Runner) (*kube.Client, error) {
	data, err := runner.ReadFile(ctx, K3sKubeconfigPath)
	if err != nil {
		return nil, fmt.Errorf("read %s: %w", K3sKubeconfigPath, err)
	}
	staged, err := os.CreateTemp("", "skali-installer-kubeconfig-*")
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

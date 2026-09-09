package updates

import (
	"context"
	"fmt"

	"github.com/Hinkolas/skali/internal/bundle"
	"github.com/Hinkolas/skali/internal/version"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
)

// RecoveryStatus uses Kubernetes rather than the product database. This is
// intentionally only used by the explicit privileged CLI recovery path.
func (c *Cluster) RecoveryStatus(ctx context.Context) (*Status, error) {
	snapshot, err := c.Snapshot(ctx)
	if err != nil {
		return nil, err
	}
	deployment, err := c.Client.AppsV1().Deployments(bundle.Namespace).Get(ctx, "skalid", metav1.GetOptions{})
	if err != nil {
		return nil, err
	}
	installed := ""
	for _, container := range deployment.Spec.Template.Spec.Containers {
		if container.Name == "skalid" {
			installed, _ = version.PublishedSkalidVersion(container.Image)
		}
	}
	if installed == "" {
		return nil, fmt.Errorf("cannot determine the deployed skalid release; refusing an unverified recovery target")
	}
	status := &Status{Installed: Installed{Version: installed, PlatformVersion: snapshot.PlatformVersion},
		Managed: true, Manageable: snapshot.Manageable, Reason: snapshot.Reason, Nodes: snapshot.Nodes, Operation: snapshot.Operation}
	status.Summary = summarize(status, snapshot.ExpectedK3s, c.clock())
	return status, nil
}

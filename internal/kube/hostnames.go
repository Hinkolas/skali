package kube

import (
	"context"
	"fmt"
	"github.com/Hinkolas/skali/internal/edge"
	rendering "github.com/Hinkolas/skali/internal/kubernetes"
	"github.com/google/uuid"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
)

// LiveRouteHosts deliberately reads the API, never an informer cache. Missing
// annotations prevent claim release rather than treating unknown routes as gone.
func (c *Client) LiveRouteHosts(ctx context.Context, env uuid.UUID) (map[string]bool, error) {
	list, err := c.Dynamic.Resource(edge.IngressRouteGVR).Namespace(rendering.NamespaceName(env.String())).List(ctx, metav1.ListOptions{})
	if err != nil {
		return nil, err
	}
	hosts := map[string]bool{}
	for _, obj := range list.Items {
		host := obj.GetAnnotations()["skali.dev/route-hostname"]
		if host == "" {
			return nil, fmt.Errorf("cannot release hostname claims while an unrecognized ingress route remains")
		}
		hosts[host] = true
	}
	return hosts, nil
}

package bundle

import (
	"context"
	"errors"
	"fmt"

	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/client-go/kubernetes"
)

// SkalidImage resolves the image the daemon's own helpers run in (the
// backup data mover, the release Job's reachability wait): override when
// set, else the image of the live skalid Deployment, which exists
// everywhere the bundle deployed skalid (production and local dev alike).
func SkalidImage(ctx context.Context, clientset kubernetes.Interface, override string) (string, error) {
	if override != "" {
		return override, nil
	}
	deployment, err := clientset.AppsV1().Deployments(Namespace).Get(ctx, "skalid", metav1.GetOptions{})
	if err != nil {
		return "", fmt.Errorf("resolve the skalid image from its deployment: %w", err)
	}
	for _, container := range deployment.Spec.Template.Spec.Containers {
		if container.Name == "skalid" {
			return container.Image, nil
		}
	}
	if len(deployment.Spec.Template.Spec.Containers) > 0 {
		return deployment.Spec.Template.Spec.Containers[0].Image, nil
	}
	return "", errors.New("the skalid deployment has no containers")
}

package kubernetes

import (
	"fmt"

	corev1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/api/meta"
	"k8s.io/apimachinery/pkg/runtime"
)

// ValidateObjects fails before any desired state is applied. API versions do
// not distinguish identities: two versions address the same stored object.
func ValidateObjects(objects []runtime.Object) error {
	seen := map[string]bool{}
	for _, obj := range objects {
		m, err := meta.Accessor(obj)
		if err != nil {
			return err
		}
		g := obj.GetObjectKind().GroupVersionKind()
		key := g.Group + "/" + g.Kind + "/" + m.GetNamespace() + "/" + m.GetName()
		if seen[key] {
			return fmt.Errorf("duplicate Kubernetes identity: %s", key)
		}
		seen[key] = true
		if svc, ok := obj.(*corev1.Service); ok {
			ports := map[string]bool{}
			for _, port := range svc.Spec.Ports {
				if ports[port.Name] {
					return fmt.Errorf("duplicate service port name in %s", key)
				}
				ports[port.Name] = true
			}
		}
	}
	return nil
}

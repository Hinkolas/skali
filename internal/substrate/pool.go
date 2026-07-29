package substrate

import (
	"context"
	"fmt"

	apierrors "k8s.io/apimachinery/pkg/api/errors"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"

	"github.com/Hinkolas/skali/internal/dbstore"
	"github.com/Hinkolas/skali/internal/kube"
	"github.com/Hinkolas/skali/internal/store"
	"github.com/Hinkolas/skali/internal/substrate/cnpg"
)

// clusterHealthyPhase is CNPG's steady-state phase string, the same probe
// the installer bundle uses for the bootstrap database.
const clusterHealthyPhase = "Cluster in healthy state"

// ensurePool applies one pool's CNPG Cluster from its row plus the live
// tenant roles. Idempotent server-side apply under the platform manager.
func (c *Controller) ensurePool(ctx context.Context, pool store.DatabaseCluster) error {
	tenants, err := c.deps.DB.ListClusterTenants(ctx, pool.ID)
	if err != nil {
		return err
	}
	roles := make([]cnpg.Role, 0, len(tenants))
	for _, tenant := range tenants {
		roles = append(roles, cnpg.Role{Name: tenant.RoleName, SecretName: tenant.CredentialSecret})
	}
	object := cnpg.RenderCluster(cnpg.ClusterSpec{
		Namespace:    Namespace,
		Name:         pool.Name,
		Image:        pool.Image,
		Instances:    int(pool.Instances),
		StorageBytes: pool.StorageBytes,
		Synchronous:  pool.Instances >= 3,
		Managed:      c.cfg.Managed,
		Hibernated:   pool.State == dbstore.StateHibernated,
		Roles:        roles,
	})
	if _, err := c.deps.Cluster.ApplyAs(ctx, object, kube.FieldManagerPlatform, false); err != nil {
		return fmt.Errorf("substrate: apply pool %s: %w", pool.Name, err)
	}
	return nil
}

// poolReady reports whether the pool's CNPG cluster is healthy enough for
// tenant work, with a human reason when it is not. It reads the live object;
// the dynamic observation source is the wake-up signal while this stays the
// authoritative pre-provisioning check.
func (c *Controller) poolReady(ctx context.Context, pool store.DatabaseCluster) (bool, string, error) {
	object, err := c.deps.Cluster.GetObject(ctx, cnpg.ClusterGVR, Namespace, pool.Name)
	if err != nil {
		if apierrors.IsNotFound(err) {
			return false, "pool object not created yet", nil
		}
		return false, "", fmt.Errorf("substrate: read pool %s: %w", pool.Name, err)
	}
	phase, _, _ := unstructured.NestedString(object.Object, "status", "phase")
	ready, _, _ := unstructured.NestedInt64(object.Object, "status", "readyInstances")
	if phase != clusterHealthyPhase || ready < 1 {
		reason := phase
		if reason == "" {
			reason = "cluster starting"
		}
		return false, fmt.Sprintf("pool %s: %s (%d ready)", pool.Name, reason, ready), nil
	}
	return true, "", nil
}

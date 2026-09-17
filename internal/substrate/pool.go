package substrate

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"time"

	apierrors "k8s.io/apimachinery/pkg/api/errors"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"

	"github.com/Hinkolas/skali/internal/bundle"
	"github.com/Hinkolas/skali/internal/dbstore"
	"github.com/Hinkolas/skali/internal/kube"
	"github.com/Hinkolas/skali/internal/pgtune"
	"github.com/Hinkolas/skali/internal/store"
	"github.com/Hinkolas/skali/internal/substrate/cnpg"
)

// clusterHealthyPhase is CNPG's steady-state phase string, the same probe
// the installer bundle uses for the bootstrap database.
const clusterHealthyPhase = "Cluster in healthy state"

// ensurePool applies one pool's CNPG Cluster from its row plus the live
// tenant roles and its tuned parameter set. Idempotent server-side apply
// under the platform manager. On a managed cluster the pool waits (requeue)
// until a database node's memory is observed: applying an untuned spec
// first and the tuned one a minute later would restart the pool twice.
func (c *Controller) ensurePool(ctx context.Context, pool store.DatabaseCluster) (time.Duration, error) {
	tenants, err := c.deps.DB.ListClusterTenants(ctx, pool.ID)
	if err != nil {
		return 0, err
	}
	roles := make([]cnpg.Role, 0, len(tenants))
	for _, tenant := range tenants {
		roles = append(roles, cnpg.Role{Name: tenant.RoleName, SecretName: tenant.CredentialSecret})
	}
	budgets, ok, err := c.PoolBudgets(ctx)
	if err != nil {
		return 0, err
	}
	if !ok {
		slog.Info("substrate: pool waits for database node memory", "pool", pool.Name)
		return requeueWait, nil
	}
	budget, ok := budgets[pool.ID]
	if !ok {
		// The row is live (the caller checked) but raced the listing;
		// size it alone rather than render it untuned.
		budgets, _ = c.resolveBudgets([]store.DatabaseCluster{pool})
		budget = budgets[pool.ID]
	}
	parameters := pgtune.Effective(budget.Bytes, pool.StorageBytes, c.cfg.Managed, dbstore.ClusterParameters(pool))
	object := cnpg.RenderCluster(cnpg.ClusterSpec{
		Namespace:          Namespace,
		Name:               pool.Name,
		Image:              pool.Image,
		Instances:          int(pool.Instances),
		StorageBytes:       pool.StorageBytes,
		Synchronous:        pool.Instances >= 3,
		Managed:            c.cfg.Managed,
		Roles:              roles,
		Parameters:         parameters,
		MemoryRequestBytes: budget.Bytes,
	})
	if _, err := c.deps.Cluster.ApplyAs(ctx, object, kube.FieldManagerPlatform, false); err != nil {
		return 0, fmt.Errorf("substrate: apply pool %s: %w", pool.Name, err)
	}
	// The exporter Service feeds the storage sampler's database-size
	// scrape; both platform shapes carry it.
	metricsService := cnpg.RenderMetricsService(Namespace, pool.Name)
	if _, err := c.deps.Cluster.ApplyAs(ctx, metricsService, kube.FieldManagerPlatform, false); err != nil {
		return 0, fmt.Errorf("substrate: apply pool %s metrics service: %w", pool.Name, err)
	}
	if !c.cfg.Managed {
		if err := c.ensurePoolNodePort(ctx, pool); err != nil {
			return 0, err
		}
	}
	return 0, nil
}

// ensurePoolNodePort gives a dev pool its loopback NodePort Service. An
// exhausted range degrades to a cluster-internal pool with a warning rather
// than failing the pool converge.
func (c *Controller) ensurePoolNodePort(ctx context.Context, pool store.DatabaseCluster) error {
	nodePort, err := c.deps.DB.AllocateClusterNodePort(ctx, pool.ID, bundle.PoolNodePortMin, bundle.PoolNodePortMax)
	if errors.Is(err, dbstore.ErrNodePortsExhausted) {
		slog.Warn("substrate: loopback node ports exhausted; pool stays cluster-internal", "pool", pool.Name)
		return nil
	}
	if err != nil {
		return fmt.Errorf("substrate: allocate node port for pool %s: %w", pool.Name, err)
	}
	service := cnpg.RenderPrimaryNodePortService(Namespace, pool.Name, int32(nodePort))
	if _, err := c.deps.Cluster.ApplyAs(ctx, service, kube.FieldManagerPlatform, false); err != nil {
		return fmt.Errorf("substrate: apply pool %s external service: %w", pool.Name, err)
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
		if detail := poolConditionDetail(object); detail != "" {
			reason += "; " + detail
		}
		return false, fmt.Sprintf("pool %s: %s (%d/%d ready)", pool.Name, reason, ready, pool.Instances), nil
	}
	return true, "", nil
}

// poolConditionDetail extracts the first failing CNPG condition's message:
// free detail on the already-fetched object (image pulls, volume binding,
// and similar stalls surface here).
func poolConditionDetail(object *unstructured.Unstructured) string {
	conditions, _, _ := unstructured.NestedSlice(object.Object, "status", "conditions")
	for _, entry := range conditions {
		condition, ok := entry.(map[string]any)
		if !ok {
			continue
		}
		if status, _ := condition["status"].(string); status != "False" {
			continue
		}
		if message, _ := condition["message"].(string); message != "" {
			return message
		}
	}
	return ""
}

// PoolMembers lists a pool's live instance pods as members. Instance pods
// carry no managed label and never enter the observed store, so this is a
// direct read; nil without a cluster (API-only, tests).
func (c *Controller) PoolMembers(ctx context.Context, pool string) ([]cnpg.Instance, error) {
	if c.deps.Cluster == nil {
		return nil, nil
	}
	pods, err := c.deps.Cluster.ListPods(ctx, Namespace, cnpg.InstanceSelector(pool))
	if err != nil {
		return nil, fmt.Errorf("substrate: list pool members: %w", err)
	}
	return cnpg.InstancesFromPods(pods), nil
}

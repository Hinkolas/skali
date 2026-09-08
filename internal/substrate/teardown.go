package substrate

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"time"

	"github.com/google/uuid"
	"k8s.io/apimachinery/pkg/runtime/schema"

	"github.com/Hinkolas/skali/internal/dbstore"
	"github.com/Hinkolas/skali/internal/kube"
	"github.com/Hinkolas/skali/internal/kubernetes"
	"github.com/Hinkolas/skali/internal/observe"
	"github.com/Hinkolas/skali/internal/store"
	"github.com/Hinkolas/skali/internal/substrate/cnpg"
)

var secretGVK = schema.GroupVersionKind{Version: "v1", Kind: "Secret"}

// Release implements the purge side of reconcile.ClaimManager: every live
// claim of the environment enters releasing (the destructive gate ran at
// deploy open; environment purge is itself a persisted destructive
// decision). It reports whether all claims are fully released and, while
// not, which are still going.
func (c *Controller) Release(ctx context.Context, environmentID uuid.UUID) (bool, []string, error) {
	live, err := c.deps.DB.ListEnvironmentClaims(ctx, environmentID)
	if err != nil {
		return false, nil, err
	}
	liveBuckets, err := c.deps.DB.ListEnvironmentBucketClaims(ctx, environmentID)
	if err != nil {
		return false, nil, err
	}
	if len(live)+len(liveBuckets) == 0 {
		return true, nil, nil
	}
	detail := make([]string, 0, len(live)+len(liveBuckets))
	for _, row := range live {
		released, err := c.deps.DB.ReleaseClaim(ctx, row.ID)
		if err != nil {
			return false, nil, err
		}
		c.EnqueueClaim(row.ID)
		c.publishClaim(*released)
		detail = append(detail, "releasing databases."+row.ServiceKey)
	}
	for _, row := range liveBuckets {
		released, err := c.deps.DB.ReleaseBucketClaim(ctx, row.ID)
		if err != nil {
			return false, nil, err
		}
		c.EnqueueBucketClaim(row.ID)
		c.publishBucketClaim(*released)
		detail = append(detail, "releasing buckets."+row.ServiceKey)
	}
	return false, detail, nil
}

// teardownClaim walks a releasing claim to released: the logical database
// is dropped declaratively, the substrate objects and credentials are
// removed, the tenant and placement close, and empty non-shared pools are
// collected.
func (c *Controller) teardownClaim(ctx context.Context, row store.DatabaseClaim) (time.Duration, error) {
	tenant, err := c.deps.DB.LiveTenant(ctx, row.ID)
	if errors.Is(err, dbstore.ErrNotFound) {
		// Never provisioned (or already cleaned): close the claim.
		return 0, c.finishClaimRelease(ctx, row, uuid.Nil)
	}
	if err != nil {
		return 0, err
	}
	pool, err := c.deps.DB.GetCluster(ctx, tenant.ClusterID)
	if err != nil {
		return 0, err
	}

	// Drop the logical database declaratively (ensure: absent, reclaim
	// delete), wait until CNPG confirms, then remove the objects.
	object := cnpg.RenderDatabase(cnpg.DatabaseSpec{
		Namespace:    Namespace,
		ObjectName:   databaseObjectName(*tenant),
		ClusterName:  pool.Name,
		DatabaseName: tenant.DatabaseName,
		Owner:        tenant.RoleName,
		Absent:       true,
		Labels:       claimLabels(row),
	})
	if _, err := c.deps.Cluster.ApplyAs(ctx, object, kube.FieldManagerPlatform, false); err != nil {
		return 0, fmt.Errorf("substrate: apply database removal: %w", err)
	}
	dropped, reason, err := c.databaseApplied(ctx, *tenant)
	if err != nil {
		return 0, err
	}
	if !dropped {
		c.setWaiting(row.ID, "dropping the database: "+reason)
		return requeueWait, nil
	}
	if _, err := c.deps.Cluster.Delete(ctx, kube.ObjectRef{
		GVK: cnpg.DatabaseGVK, Namespace: Namespace, Name: databaseObjectName(*tenant),
	}); err != nil {
		return 0, fmt.Errorf("substrate: delete database object: %w", err)
	}
	if _, err := c.deps.Cluster.Delete(ctx, kube.ObjectRef{
		GVK: secretGVK, Namespace: Namespace, Name: tenant.CredentialSecret,
	}); err != nil {
		return 0, fmt.Errorf("substrate: delete credential secret: %w", err)
	}
	if row.OwnerKind == dbstore.OwnerService {
		if _, _, service, ok := ownerNames(row.OwnerRef); ok && row.EnvironmentID != nil {
			if _, err := c.deps.Cluster.Delete(ctx, kube.ObjectRef{
				GVK:       secretGVK,
				Namespace: kubernetes.NamespaceName(row.EnvironmentID.String()),
				Name:      kubernetes.OutputSecretName("databases", service),
			}); err != nil {
				return 0, fmt.Errorf("substrate: delete output mirror: %w", err)
			}
		}
	}
	return 0, c.finishClaimRelease(ctx, row, pool.ID)
}

// finishClaimRelease closes the claim (tenant released, placement
// superseded, phase released), shrinks the pool's role list, and collects
// empty non-shared pools.
func (c *Controller) finishClaimRelease(ctx context.Context, row store.DatabaseClaim, poolID uuid.UUID) error {
	if err := c.deps.DB.CompleteClaimRelease(ctx, row.ID); err != nil {
		return err
	}
	c.setWaiting(row.ID, "")
	c.deps.Observed.Remove(observe.ClaimRef(row.ID))
	if row.EnvironmentID != nil && c.deps.Enqueue != nil {
		c.deps.Enqueue(*row.EnvironmentID)
	}
	if poolID == uuid.Nil {
		return nil
	}
	pool, err := c.deps.DB.GetCluster(ctx, poolID)
	if err != nil {
		return err
	}
	remaining, err := c.deps.DB.CountClusterTenants(ctx, poolID)
	if err != nil {
		return err
	}
	if pool.Class != dbstore.ClassShared && remaining == 0 {
		return c.releasePool(ctx, *pool)
	}
	// The shared pool survives its tenants; re-apply so the role list
	// shrinks.
	if err := c.ensurePool(ctx, *pool); err != nil {
		return err
	}
	c.EnqueuePool(poolID)
	return nil
}

// releasePool deletes an empty non-shared pool's CNPG cluster (its volumes
// go with it) and retires the row.
func (c *Controller) releasePool(ctx context.Context, pool store.DatabaseCluster) error {
	if pool.State != dbstore.StateReleasing {
		if _, err := c.deps.DB.TransitionCluster(ctx, pool.ID, dbstore.StateReleasing); err != nil {
			return err
		}
	}
	if _, err := c.deps.Cluster.Delete(ctx, kube.ObjectRef{
		GVK: cnpg.ClusterGVK, Namespace: Namespace, Name: pool.Name,
	}); err != nil {
		return fmt.Errorf("substrate: delete pool: %w", err)
	}
	// The dev-only loopback Service goes with the pool; deleting a Service
	// that was never rendered is a tolerated no-op.
	if _, err := c.deps.Cluster.Delete(ctx, kube.ObjectRef{
		GVK:       schema.GroupVersionKind{Version: "v1", Kind: "Service"},
		Namespace: Namespace, Name: pool.Name + "-external",
	}); err != nil {
		return fmt.Errorf("substrate: delete pool external service: %w", err)
	}
	if _, err := c.deps.Cluster.Delete(ctx, kube.ObjectRef{
		GVK:       schema.GroupVersionKind{Version: "v1", Kind: "Service"},
		Namespace: Namespace, Name: cnpg.MetricsServiceName(pool.Name),
	}); err != nil {
		return fmt.Errorf("substrate: delete pool metrics service: %w", err)
	}
	if _, err := c.deps.DB.TransitionCluster(ctx, pool.ID, dbstore.StateReleased); err != nil {
		return err
	}
	slog.Info("substrate: pool released", "pool", pool.Name)
	return nil
}

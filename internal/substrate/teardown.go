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
	"github.com/Hinkolas/skali/internal/reconcile"
	"github.com/Hinkolas/skali/internal/store"
	"github.com/Hinkolas/skali/internal/substrate/cnpg"
)

var secretGVK = schema.GroupVersionKind{Version: "v1", Kind: "Secret"}

// Release implements the purge side of reconcile.ClaimManager: every live
// claim of the environment enters releasing (the destructive gate ran at
// deploy open; environment purge is itself a persisted destructive
// decision). It reports whether all claims are fully released and, while
// not, which are still going and what each teardown waits for.
func (c *Controller) Release(ctx context.Context, environmentID uuid.UUID) (bool, []reconcile.ClaimRelease, error) {
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
	pending := make([]reconcile.ClaimRelease, 0, len(live)+len(liveBuckets))
	for _, row := range live {
		released, err := c.deps.DB.ReleaseClaim(ctx, row.ID)
		if err != nil {
			return false, nil, err
		}
		name := databaseClaimName(row)
		if released.Phase != row.Phase {
			logReleaseStarted(name, row.ID, row.EnvironmentID, released.Phase)
		}
		c.EnqueueClaim(row.ID, reasonTeardown)
		c.publishClaim(*released)
		pending = append(pending, reconcile.ClaimRelease{Service: name, Waiting: c.WaitingReason(row.ID)})
	}
	for _, row := range liveBuckets {
		released, err := c.deps.DB.ReleaseBucketClaim(ctx, row.ID)
		if err != nil {
			return false, nil, err
		}
		name := bucketClaimName(row)
		if released.Phase != row.Phase {
			logReleaseStarted(name, row.ID, row.EnvironmentID, released.Phase)
		}
		c.EnqueueBucketClaim(row.ID, reasonTeardown)
		c.publishBucketClaim(*released)
		pending = append(pending, reconcile.ClaimRelease{Service: name, Waiting: c.WaitingReason(row.ID)})
	}
	return false, pending, nil
}

func databaseClaimName(row store.DatabaseClaim) string { return "databases." + row.ServiceKey }
func bucketClaimName(row store.BucketClaim) string     { return "buckets." + row.ServiceKey }

// Claim release logs one line per transition: started, each changed
// waiting reason, released. A reason repeated across passes logs nothing.
func logReleaseStarted(name string, id uuid.UUID, environmentID *uuid.UUID, phase string) {
	slog.Info("substrate: claim release started", "claim", name, "claim_id", id,
		"environment", environmentString(environmentID), "phase", phase)
}

func logReleased(name string, id uuid.UUID, environmentID *uuid.UUID) {
	slog.Info("substrate: claim released", "claim", name, "claim_id", id,
		"environment", environmentString(environmentID))
}

// environmentString renders a claim's environment for a log line; system
// claims have none.
func environmentString(environmentID *uuid.UUID) string {
	if environmentID == nil {
		return ""
	}
	return environmentID.String()
}

// releaseWaiting records a releasing claim's waiting reason. A changed
// reason is logged and pokes the claim's environment, so the purge's
// teardown step shows it without waiting out the environment's requeue.
func (c *Controller) releaseWaiting(name string, id uuid.UUID, environmentID *uuid.UUID, reason string) {
	if !c.setWaiting(id, reason) {
		return
	}
	slog.Info("substrate: claim release waiting", "claim", name, "claim_id", id,
		"environment", environmentString(environmentID), "reason", reason)
	if environmentID != nil && c.deps.Enqueue != nil {
		c.deps.Enqueue(*environmentID)
	}
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
	// A session still open on the database (a host process holding
	// revealed credentials; the consumers are gone) would keep the drop
	// failing; the tenant's roles are done for, so their sessions end.
	if err := c.execPrimarySQL(ctx, *pool, "postgres", cnpg.TerminateSessionsSQL(tenantRoleNames(*tenant))); err != nil {
		var waiting errWaiting
		if errors.As(err, &waiting) {
			c.releaseWaiting(databaseClaimName(row), row.ID, row.EnvironmentID, waiting.reason)
			return requeueWait, nil
		}
		return 0, err
	}
	dropped, reason, err := c.databaseApplied(ctx, *tenant)
	if err != nil {
		return 0, err
	}
	if !dropped {
		c.releaseWaiting(databaseClaimName(row), row.ID, row.EnvironmentID, "dropping the database: "+reason)
		return requeueWait, nil
	}
	if _, err := c.deps.Cluster.Delete(ctx, kube.ObjectRef{
		GVK: cnpg.DatabaseGVK, Namespace: Namespace, Name: databaseObjectName(*tenant),
	}); err != nil {
		return 0, fmt.Errorf("substrate: delete database object: %w", err)
	}
	// The roles go the way a retired login role does: out of the Cluster
	// spec first (the releasing claim's tenant is not rendered), their
	// Secrets deleted, then dropped by SQL, login roles before the owner,
	// and confirmed gone on a later pass since a stale operator sync can
	// recreate what the previous spec listed. The row keeps their names
	// until the claim closes, so a crash here is redone.
	if _, err := c.ensurePool(ctx, *pool, time.Now()); err != nil {
		return 0, err
	}
	for _, name := range tenantSecretNames(*tenant) {
		if _, err := c.deps.Cluster.Delete(ctx, kube.ObjectRef{
			GVK: secretGVK, Namespace: Namespace, Name: name,
		}); err != nil {
			return 0, fmt.Errorf("substrate: delete credential secret: %w", err)
		}
	}
	roles := tenantRoleNames(*tenant)
	present, err := c.queryPrimary(ctx, *pool, "postgres", cnpg.RolesPresentQuery(roles))
	if err == nil && present != "0" {
		if !c.settled(row.ID, time.Now()) {
			err = errWaiting{reason: "waiting for the pool to settle before dropping the roles"}
		} else if err = c.execPrimarySQL(ctx, *pool, "postgres", cnpg.DropTenantRolesSQL(roles)); err == nil {
			err = errWaiting{reason: "roles dropped, confirming on the next pass"}
		}
	}
	if err != nil {
		var waiting errWaiting
		if errors.As(err, &waiting) {
			c.releaseWaiting(databaseClaimName(row), row.ID, row.EnvironmentID, waiting.reason)
			return requeueWait, nil
		}
		return 0, err
	}
	c.forgetSettle(row.ID)
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
	logReleased(databaseClaimName(row), row.ID, row.EnvironmentID)
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
	// shrinks. A pool still waiting for node memory is picked up by the
	// enqueue below.
	if _, err := c.ensurePool(ctx, *pool, time.Now()); err != nil {
		return err
	}
	c.enqueuePool(poolID, reasonTeardown)
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
	if _, err := c.deps.Cluster.Delete(ctx, kube.ObjectRef{
		GVK:       schema.GroupVersionKind{Group: "networking.k8s.io", Version: "v1", Kind: "NetworkPolicy"},
		Namespace: Namespace, Name: cnpg.AccessPolicyName(pool.Name),
	}); err != nil {
		return fmt.Errorf("substrate: delete pool access policy: %w", err)
	}
	if _, err := c.deps.DB.TransitionCluster(ctx, pool.ID, dbstore.StateReleased); err != nil {
		return err
	}
	slog.Info("substrate: pool released", "pool", pool.Name)
	return nil
}

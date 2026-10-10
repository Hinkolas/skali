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
// now decides which rotating roles are still inside their window.
func (c *Controller) ensurePool(ctx context.Context, pool store.DatabaseCluster, now time.Time) (time.Duration, error) {
	// The access policy converges first: it does not depend on the memory
	// budget below, and a released environment must not stay admitted (nor
	// a new holder stay blocked) while the pool waits for node memory.
	if err := c.ensurePoolAccess(ctx, pool); err != nil {
		return 0, err
	}
	tenants, err := c.deps.DB.ListManagedClusterTenants(ctx, pool.ID)
	if err != nil {
		return 0, err
	}
	roles := make([]cnpg.Role, 0, len(tenants))
	for _, tenant := range tenants {
		roles = append(roles, tenantRoles(tenant, now)...)
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

// tenantRoles renders one tenant's managed roles as the pool must hold
// them now. The owner role (role_name) owns the database; it logs in only
// while it is the login role (every tenant before its first rotation) or
// the previous login role inside a window. A login role other than the
// owner is a member of it with its own password Secret: the current one,
// a pending one the worker has not taken yet, and the previous one until
// its window ends. A role that left the window is no longer rendered at
// all: CNPG never drops roles, the worker does by SQL, and a role still in
// the spec would be recreated behind it.
func tenantRoles(tenant store.DatabaseTenant, now time.Time) []cnpg.Role {
	inWindow := tenant.CredentialRetireAt != nil && now.Before(*tenant.CredentialRetireAt)
	previous := ""
	previousSecret := ""
	if tenant.PreviousLoginRole != nil && inWindow {
		previous = *tenant.PreviousLoginRole
		if tenant.PreviousCredentialSecret != nil {
			previousSecret = *tenant.PreviousCredentialSecret
		}
	}
	owner := cnpg.Role{Name: tenant.RoleName}
	switch {
	case tenant.LoginRole == tenant.RoleName:
		owner.Login = true
		owner.SecretName = tenant.CredentialSecret
	case previous == tenant.RoleName:
		owner.Login = true
		owner.SecretName = previousSecret
	default:
		owner.DisablePassword = true
	}
	roles := []cnpg.Role{owner}
	login := func(name, secret string) cnpg.Role {
		return cnpg.Role{Name: name, SecretName: secret, Login: true, InRoles: []string{tenant.RoleName}}
	}
	if tenant.LoginRole != tenant.RoleName {
		roles = append(roles, login(tenant.LoginRole, tenant.CredentialSecret))
	}
	if tenant.PendingLoginRole != nil && tenant.PendingCredentialSecret != nil {
		roles = append(roles, login(*tenant.PendingLoginRole, *tenant.PendingCredentialSecret))
	}
	if previous != "" && previous != tenant.RoleName {
		roles = append(roles, login(previous, previousSecret))
	}
	return roles
}

// ensurePoolAccess applies the policy admitting the pool's instance pods to
// the environments holding a claim placed on it (plus the operator, the
// platform namespace, and skalid's probe sources). Serialised with the S3
// policy: see accessMu.
func (c *Controller) ensurePoolAccess(ctx context.Context, pool store.DatabaseCluster) error {
	c.accessMu.Lock()
	defer c.accessMu.Unlock()
	claims, err := c.deps.DB.ListClusterClaims(ctx, pool.ID)
	if err != nil {
		return fmt.Errorf("substrate: list holders of pool %s: %w", pool.Name, err)
	}
	peers, err := c.accessPeers(ctx, holderEnvironments(claims, nil))
	if err != nil {
		return err
	}
	policy := cnpg.RenderAccessPolicy(Namespace, pool.Name, peers)
	if _, err := c.deps.Cluster.ApplyAs(ctx, policy, kube.FieldManagerPlatform, false); err != nil {
		return fmt.Errorf("substrate: apply pool %s access policy: %w", pool.Name, err)
	}
	return nil
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

package substrate

import (
	"context"
	"log/slog"
	"sort"

	"github.com/google/uuid"

	"github.com/Hinkolas/skali/internal/layout"
	"github.com/Hinkolas/skali/internal/pgtune"
	"github.com/Hinkolas/skali/internal/store"
)

// DatabaseNodeUsable reports the memory pools may share on the smallest
// database-capable node: the node's name and its usable bytes. ok is false
// on a managed cluster until the node informer has delivered a capable
// node's allocatable memory; the local platform always answers with the
// fixed dev budget and no node.
func (c *Controller) DatabaseNodeUsable() (node string, usable int64, ok bool) {
	if !c.cfg.Managed {
		return "", pgtune.DevBudgetBytes, true
	}
	node, allocatable := c.deps.Observed.SmallestCapableNodeMemory(layout.CapabilityDatabase)
	if allocatable <= 0 {
		return "", 0, false
	}
	return node, pgtune.Usable(allocatable), true
}

// PoolBudgets resolves every live pool's budget. ok mirrors
// DatabaseNodeUsable: false means a managed cluster has not observed its
// database nodes yet and nothing can be sized.
func (c *Controller) PoolBudgets(ctx context.Context) (map[uuid.UUID]pgtune.Budget, bool, error) {
	pools, err := c.deps.DB.ListLiveClusters(ctx)
	if err != nil {
		return nil, false, err
	}
	budgets, ok := c.resolveBudgets(pools)
	return budgets, ok, nil
}

// resolveBudgets sizes the given pools together. An explicit budget is
// taken as set. Automatic budgets are the class share of the node's usable
// memory (pgtune.AutoBudget), handed out in creation order: each pool gets
// min(share, what the earlier pools left), so adding a pool never shrinks
// an existing one and the sum never exceeds the node. The local platform
// gives every pool the fixed dev budget; it has one pool by design.
func (c *Controller) resolveBudgets(pools []store.DatabaseCluster) (map[uuid.UUID]pgtune.Budget, bool) {
	node, usable, ok := c.DatabaseNodeUsable()
	if !ok {
		return nil, false
	}
	ordered := append([]store.DatabaseCluster(nil), pools...)
	sort.Slice(ordered, func(i, j int) bool {
		if !ordered[i].CreatedAt.Equal(ordered[j].CreatedAt) {
			return ordered[i].CreatedAt.Before(ordered[j].CreatedAt)
		}
		return ordered[i].ID.String() < ordered[j].ID.String()
	})
	budgets := make(map[uuid.UUID]pgtune.Budget, len(ordered))
	committed := int64(0)
	for _, pool := range ordered {
		if pool.MemoryBytes != nil && *pool.MemoryBytes > 0 {
			budgets[pool.ID] = pgtune.Budget{Bytes: *pool.MemoryBytes}
			committed += *pool.MemoryBytes
			continue
		}
		if !c.cfg.Managed {
			budgets[pool.ID] = pgtune.Budget{Bytes: pgtune.DevBudgetBytes, Auto: true}
			continue
		}
		budget := pgtune.Budget{Bytes: pgtune.AutoBudget(pool.Class, usable), Auto: true, Node: node}
		if remaining := usable - committed; remaining < budget.Bytes {
			budget.Bytes = pgtune.Quantize(remaining)
			budget.Capped = true
			slog.Warn("substrate: pool budget capped so the live pools fit the database node",
				"pool", pool.Name, "node", node, "budget_bytes", budget.Bytes, "usable_bytes", usable)
		}
		budgets[pool.ID] = budget
		committed += budget.Bytes
	}
	return budgets, true
}

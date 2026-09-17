package substrate

import (
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/stretchr/testify/require"

	"github.com/Hinkolas/skali/internal/layout"
	"github.com/Hinkolas/skali/internal/observe"
	"github.com/Hinkolas/skali/internal/pgtune"
	"github.com/Hinkolas/skali/internal/store"
)

const testMiB = int64(1 << 20)

func budgetPool(name, class string, created time.Time, memory *int64) store.DatabaseCluster {
	id, _ := uuid.NewV7()
	return store.DatabaseCluster{ID: id, Name: name, Class: class, CreatedAt: created, MemoryBytes: memory}
}

func TestResolveBudgetsDev(t *testing.T) {
	t.Parallel()
	c := &Controller{cfg: Config{Managed: false}, deps: Deps{Observed: observe.NewStore(nil)}}
	pool := budgetPool("pg17-shared", pgtune.ClassShared, time.Now(), nil)
	budgets, ok := c.resolveBudgets([]store.DatabaseCluster{pool})
	require.True(t, ok, "the local platform never waits for node memory")
	require.Equal(t, pgtune.Budget{Bytes: pgtune.DevBudgetBytes, Auto: true}, budgets[pool.ID])

	node, usable, ok := c.DatabaseNodeUsable()
	require.True(t, ok)
	require.Empty(t, node)
	require.Equal(t, pgtune.DevBudgetBytes, usable)
}

func TestResolveBudgetsManagedWaitsForNodeMemory(t *testing.T) {
	t.Parallel()
	observed := observe.NewStore(nil)
	c := &Controller{cfg: Config{Managed: true}, deps: Deps{Observed: observed}}
	pool := budgetPool("pg17-shared", pgtune.ClassShared, time.Now(), nil)

	_, ok := c.resolveBudgets([]store.DatabaseCluster{pool})
	require.False(t, ok, "no capable node with a size yet")

	// A capable node without a reported size is still unknown.
	observed.SetNodeCapabilities("db-1", []string{layout.CapabilityDatabase})
	_, ok = c.resolveBudgets([]store.DatabaseCluster{pool})
	require.False(t, ok)

	observed.SetNodeRecord(observe.NodeRecord{Name: "db-1", MemoryAllocatableBytes: 7782 * testMiB})
	budgets, ok := c.resolveBudgets([]store.DatabaseCluster{pool})
	require.True(t, ok)
	require.Equal(t, pgtune.Budget{Bytes: 3328 * testMiB, Auto: true, Node: "db-1"}, budgets[pool.ID])
}

func TestResolveBudgetsCreationOrderAndCap(t *testing.T) {
	t.Parallel()
	observed := observe.NewStore(nil)
	observed.SetNodeCapabilities("db-1", []string{layout.CapabilityDatabase})
	observed.SetNodeCapabilities("db-2", []string{layout.CapabilityDatabase})
	observed.SetNodeRecord(observe.NodeRecord{Name: "db-1", MemoryAllocatableBytes: 16 << 30})
	observed.SetNodeRecord(observe.NodeRecord{Name: "db-2", MemoryAllocatableBytes: 8 << 30})
	c := &Controller{cfg: Config{Managed: true}, deps: Deps{Observed: observed}}

	// The smallest database node bounds everyone: 8 GiB - 1 GiB reserve.
	node, usable, ok := c.DatabaseNodeUsable()
	require.True(t, ok)
	require.Equal(t, "db-2", node)
	require.Equal(t, int64(7<<30), usable)

	base := time.Date(2026, 9, 1, 0, 0, 0, 0, time.UTC)
	explicit := int64(2 << 30)
	shared := budgetPool("pg17-shared", pgtune.ClassShared, base, nil)
	env1 := budgetPool("pg17-env-a", pgtune.ClassEnvironment, base.Add(time.Hour), nil)
	dedicated := budgetPool("pg17-ded-b", pgtune.ClassDedicated, base.Add(2*time.Hour), &explicit)
	env2 := budgetPool("pg17-env-c", pgtune.ClassEnvironment, base.Add(3*time.Hour), nil)
	// Listed out of order: creation order decides, not list order.
	budgets, ok := c.resolveBudgets([]store.DatabaseCluster{env2, dedicated, shared, env1})
	require.True(t, ok)

	// shared: half of 7 GiB = 3.5 GiB; env1: a quarter = 1792 MiB; the
	// dedicated pool is explicit at 2 GiB; env2 would take another 1792
	// MiB but only 7168 - 3584 - 1792 - 2048 = -256 MiB is left, so it is
	// capped at the floor.
	require.Equal(t, pgtune.Budget{Bytes: 3584 * testMiB, Auto: true, Node: "db-2"}, budgets[shared.ID])
	require.Equal(t, pgtune.Budget{Bytes: 1792 * testMiB, Auto: true, Node: "db-2"}, budgets[env1.ID])
	require.Equal(t, pgtune.Budget{Bytes: explicit}, budgets[dedicated.ID])
	require.Equal(t, pgtune.Budget{Bytes: pgtune.MinBudgetBytes, Auto: true, Node: "db-2", Capped: true}, budgets[env2.ID])

	// Earlier pools never move when a later one appears.
	budgets, _ = c.resolveBudgets([]store.DatabaseCluster{shared, env1})
	require.Equal(t, int64(3584*testMiB), budgets[shared.ID].Bytes)
	require.Equal(t, int64(1792*testMiB), budgets[env1.ID].Bytes)
}
